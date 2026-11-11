// Package server assembles and runs the CK Karaoke web UI.
//
// It exists so the server can be started by something other than a command
// line. The desktop binary parses flags into a Config; the iOS app fills one in
// from Swift and calls Run on a goroutine, because iOS forbids exec and so
// cannot do what the Android wrapper does — ship this same server as a separate
// executable and fork it.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/rrgmc/ck-karaoke/version"
	"github.com/rrgmc/ck-karaoke/webui"
)

// Config is everything the server needs. The zero value is not usable: SongDB
// and FavDB have no defaults here, because working them out from the location of
// the executable is a desktop idea — on a phone the binary lives in a read-only
// bundle and the caller knows where writable storage is.
type Config struct {
	// Backend is what the pages drive: BackendUnit, the default, or
	// BackendQueue. A port sets it when it is built; it is never the user's
	// choice, so the queue build has no way to reach the unit.
	Backend string

	// Addr is the device address, or with BackendQueue the queue manager's.
	// Empty means discover it.
	Addr string
	// DiscoverTimeout bounds discovery when Addr is empty.
	DiscoverTimeout time.Duration
	// Listen is the address to serve on.
	Listen string
	// SongDB is the song catalogue file. When it is missing, the web UI
	// downloads it over the first device session, or imports a copy.
	SongDB string
	// FavDB is the favorites database file.
	FavDB string
	// AllowFolderDelete permits deleting favorites folders, which cannot be
	// undone.
	AllowFolderDelete bool
	// SaveFile saves a file through the host's own dialog; see
	// webui.Options.SaveFile. Nil sends files to the browser as downloads.
	SaveFile func(name string, data []byte) (string, error)
	// PeerApp names the other app on this phone when it is installed, so the
	// favorites pages offer to send to it; see webui.Options.PeerApp. Only the
	// phone ports set it.
	PeerApp string
	// Name is what the device shows beside songs queued here. Empty means the
	// hostname.
	Name string
	// Verbose raises the default logger to debug level. Ignored when Logger is
	// set.
	Verbose bool

	// LastAddrFile remembers the address the device was last found at.
	//
	// Without it, switching the karaoke unit off stops the whole server coming up:
	// discovery finds nothing and Run gives up before it has even opened the
	// catalogue, so there is no browsing, no favorites and no UI — despite none of
	// those needing the device at all. With it, a later start falls back to the
	// remembered address, serves everything local straight away, and connects
	// whenever the unit comes back.
	//
	// Empty disables both the reading and the writing of it.
	LastAddrFile string

	// Logger receives everything the server has to say. Nil logs to stderr,
	// which is wrong on a phone — iOS passes a handler that writes to os_log.
	Logger *slog.Logger

	// Resolve finds the device. Nil uses client.Resolve, which broadcasts.
	//
	// iOS supplies client.Sweep instead: broadcast there needs an entitlement
	// Apple grants only on request, while plain unicast needs only the local
	// network prompt.
	Resolve func(ctx context.Context) (string, []client.Device, error)

	// Ready is called once the listener is open, before anything is served. It
	// receives the address being served on and the device that was found.
	//
	// Neither is known in advance: both phone apps bind to port 0 so that nothing
	// else on the WiFi can reach an unauthenticated remote control, and the device
	// address usually comes from discovery. This is how the caller learns both.
	Ready func(listen net.Addr, device string)

	// WaitForDevice is ignored.
	//
	// Deprecated: both backends serve at once and search in the background,
	// for as long as they run.
	WaitForDevice bool

	// QueueSweep finds a queue manager by trying port 8177 on every local
	// host rather than by mDNS. iOS sets it: mDNS from Go needs an entitlement
	// there. Android sets it too: Go cannot list the interfaces there, and
	// mDNS has none to join. Only BackendQueue reads it.
	QueueSweep bool

	// SweepNetworks are the networks QueueSweep walks, each this machine's
	// address and mask. Empty means the networks of this machine's interfaces.
	// Android passes its network, because Go cannot list the interfaces there.
	SweepNetworks []*net.IPNet

	// Searching is never called.
	//
	// Deprecated: the searching happens after the server is serving, and the
	// web UI shows it.
	Searching func()

	// Progress reports how much of the song catalogue has been downloaded.
	//
	// Deprecated: nothing calls it. The song list now downloads after the
	// server is serving, and the web UI shows the progress.
	Progress func(have, total int)

	// OnDevice hands over the session as soon as it exists, so a caller can
	// Suspend and Resume it. iOS needs that: it has no equivalent of Android's
	// foreground service, so the session has to be given up when the app stops
	// running and re-established when it comes back.
	OnDevice func(*webui.Device)

	// Local is the time zone to run in. Nil leaves time.Local alone, which is
	// right on a desktop and wrong on both phones — see LocalZone, which is how
	// a caller builds this.
	Local *time.Location
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func Run(ctx context.Context, cfg Config) error {
	logger := cfg.Logger
	if logger == nil {
		level := slog.LevelInfo
		if cfg.Verbose {
			level = slog.LevelDebug
		}
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	}

	// Before the first timestamp is written. Assigning a package-level variable
	// is a heavy-handed thing for a library to do, but this package is where the
	// application is assembled rather than a library the application uses — the
	// same reason everything else here lives in Run rather than in main — and
	// the alternative is threading a location through every caller that formats
	// a time.
	if cfg.Local != nil {
		time.Local = cfg.Local
		logger.Debug("time zone", "zone", cfg.Local.String())
	}
	logger.Info("CK Karaoke", "version", version.Version, "backend", cfg.backend())

	if cfg.backend() == BackendQueue {
		return runQueue(ctx, cfg, logger)
	}

	// Generated, not chosen: the device accepts any UUID.
	identity := protocol.NewIdentity()
	// The device tags each reserved song with the name of the session that added
	// it, so everything queued through this server carries this one name.
	identity.Name = protocol.HostName()
	if cfg.Name != "" {
		identity.Name = protocol.CleanName(cfg.Name)
	}

	// The device is found by searching. Hardcoding an address only ever worked
	// on the network it was written on.
	resolve := cfg.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context) (string, []client.Device, error) {
			return client.Resolve(ctx, cfg.Addr, cfg.DiscoverTimeout)
		}
	}

	// Nothing here waits for the device. The song list and the favorites need
	// none, and a search lasts the whole discovery timeout. The session
	// searches in the background, and the song list, when there is none yet,
	// comes over that session or from an imported file.
	device := startAddr(cfg)
	songs := openSongs(cfg.SongDB, logger)

	favs, err := favorites.Open(cfg.FavDB)
	if err != nil {
		if songs != nil {
			songs.Close()
		}
		return err
	}
	defer favs.Close()
	logger.Info("favorites ready", "file", favs.Path(), "folderDelete", cfg.AllowFolderDelete)

	// webui.New registers its callbacks on the device, and the session opens
	// only when dev.Run starts below, so no update is missed.
	dev := webui.NewDevice(device, identity, logger, nil)
	if cfg.Addr == "" {
		// A discovered address is searched for before the first dial, because
		// it is only remembered, and again whenever it stops answering. An
		// address the caller gave is the caller's to keep.
		dev.SetResolver(deviceResolver(cfg, resolve, logger), true)
	}
	srv, err := webui.New(webui.UnitBackend(dev), songs, favs, logger, webui.Options{
		AllowFolderDelete: cfg.AllowFolderDelete,
		SaveFile:          cfg.SaveFile,
		PeerApp:           cfg.PeerApp,
		SongListPath:      cfg.SongDB,
	})
	if err != nil {
		if songs != nil {
			songs.Close()
		}
		return err
	}
	// The song list may arrive while serving, so it is the server's to hand
	// back rather than this function's.
	defer func() {
		if db := srv.Catalogue(); db != nil {
			db.Close()
		}
	}()
	if cfg.OnDevice != nil {
		cfg.OnDevice(dev)
	}

	// Closed when the session has finished closing itself. Nothing waited for
	// this before, which quietly defeated the whole reason main handles SIGTERM
	// — see the shutdown branch below.
	devDone := make(chan struct{})
	go func() {
		defer close(devDone)
		dev.Run(ctx)
	}()

	// Cancelling ctx is what tells the session to close, but closing it is not
	// instant: client.Close waits for the disconnect notification to actually
	// go out, because go-raknet defers sending it to a tick loop a second later.
	// Returning without waiting would let the process exit first, and the unit
	// would go on showing a client connected until its own timeout expired —
	// which main's SIGTERM handling exists to prevent.
	//
	// It matters on Android, which stops the server every time the phone has
	// been away from it for ten minutes.
	return serve(ctx, cfg, logger, srv, device, devDone, client.CloseTimeout+time.Second)
}

// serve listens, reports the address through cfg.Ready, and serves srv until
// ctx ends. It then shuts down and waits up to closeWait for sessionDone, so
// the backend's session is closed before the caller returns.
func serve(ctx context.Context, cfg Config, logger *slog.Logger, srv *webui.Server, device string,
	sessionDone <-chan struct{}, closeWait time.Duration) error {
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the event stream is meant to stay open.
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Listen, err)
	}

	if cfg.Ready != nil {
		cfg.Ready(ln.Addr(), device)
	}

	// Without this the event streams hold the server open until the shutdown
	// deadline expires, and a clean stop reports a timeout.
	httpSrv.RegisterOnShutdown(srv.BeginShutdown)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := httpSrv.Shutdown(shutdownCtx)
		select {
		case <-sessionDone:
		case <-time.After(closeWait):
			logger.Warn("the session did not close in time")
		}
		return err
	}
}

// startAddr is the address the device session starts on: the one the caller
// gave, or the one the device was last found at, or empty when it has never
// been found.
func startAddr(cfg Config) string {
	if cfg.Addr != "" {
		return client.WithDefaultPort(cfg.Addr)
	}
	return readAddr(cfg.LastAddrFile)
}

// openSongs opens the song list, or returns nil when there is none yet. An
// unusable file is left for the next download or import to replace.
func openSongs(path string, logger *slog.Logger) *catalog.DB {
	if path == "" {
		return nil
	}
	db, err := catalog.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("song database unusable; it will be downloaded again", "file", path, "error", err)
		} else {
			logger.Info("no song database yet; it will be downloaded", "file", path)
		}
		return nil
	}
	info := db.Info()
	logger.Info("song catalogue ready",
		"file", db.Path(), "songs", info.Songs, "artists", info.Singers, "version", info.DBVersion)
	return db
}

// deviceResolver is the device session's search. It keeps the current address
// when the device still answers there, and remembers whatever it settles on.
func deviceResolver(cfg Config, resolve func(context.Context) (string, []client.Device, error),
	logger *slog.Logger) webui.Resolver {
	return func(ctx context.Context, current string) (string, error) {
		device, found, err := resolve(ctx)
		if err != nil {
			return "", err
		}
		for _, d := range found {
			if d.Addr == current {
				device = current
				break
			}
		}
		if cfg.LastAddrFile != "" {
			rememberAddr(cfg.LastAddrFile, device, logger)
		}
		logFound(logger, device, found)
		return device, nil
	}
}

// logFound reports what a search found.
func logFound(logger *slog.Logger, device string, found []client.Device) {
	switch {
	case len(found) == 1:
		logger.Info("found device", "addr", device, "info", found[0].Info.String())
	case len(found) > 1:
		others := make([]string, 0, len(found)-1)
		for _, d := range found {
			if d.Addr != device {
				others = append(others, d.Addr)
			}
		}
		logger.Info("found several devices", "using", device, "others", strings.Join(others, ", "))
	}
}
