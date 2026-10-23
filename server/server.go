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
	"github.com/rrgmc/ck-karaoke/webui"
)

// ErrCatalogue reports that the song database is missing and the device could
// not be reached to download it. It is a sentinel so a caller can add its own
// advice — the command line can suggest a flag, an app cannot.
var ErrCatalogue = errors.New("cannot reach the device to fetch the song database")

// Config is everything the server needs. The zero value is not usable: SongDB
// and FavDB have no defaults here, because working them out from the location of
// the executable is a desktop idea — on a phone the binary lives in a read-only
// bundle and the caller knows where writable storage is.
type Config struct {
	// Addr is the device address. Empty means discover it.
	Addr string
	// DiscoverTimeout bounds discovery when Addr is empty.
	DiscoverTimeout time.Duration
	// Listen is the address to serve on.
	Listen string
	// SongDB is the song catalogue file, downloaded from the device if missing.
	SongDB string
	// FavDB is the favorites database file.
	FavDB string
	// AllowFolderDelete permits deleting favorites folders, which cannot be
	// undone.
	AllowFolderDelete bool
	// SaveFile saves a file through the host's own dialog; see
	// webui.Options.SaveFile. Nil sends files to the browser as downloads.
	SaveFile func(name string, data []byte) (string, error)
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

	// WaitForDevice keeps searching when a search fails and there is no
	// remembered address to fall back on, instead of returning an error. Run
	// then returns only when a device answers or ctx is cancelled.
	//
	// The apps set it. On a first launch nothing is remembered, and macOS and iOS
	// block the app's local network traffic until the user answers the permission
	// prompt. A search then either hears nothing or fails to send, and it ends
	// before the user has read the prompt.
	WaitForDevice bool

	// Searching is called each time a search finds nothing and WaitForDevice
	// starts another one, so a caller can say what the wait is for.
	Searching func()

	// Progress reports how much of the song catalogue has been downloaded.
	//
	// Only a first run has anything to report — the catalogue is kept — but that
	// run takes upwards of twenty seconds, which is long enough that a caller
	// showing a bare spinner cannot be told apart from one that has hung.
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

	// Generated, not chosen: the device accepts any UUID.
	identity := protocol.NewIdentity()
	// The device tags each reserved song with the name of the session that added
	// it, so everything queued through this server carries this one name.
	identity.Name = protocol.HostName()
	if cfg.Name != "" {
		identity.Name = protocol.CleanName(cfg.Name)
	}

	// Find the device before anything else needs its address. Hardcoding one
	// only ever worked on the network it was written on.
	resolve := cfg.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context) (string, []client.Device, error) {
			return client.Resolve(ctx, cfg.Addr, cfg.DiscoverTimeout)
		}
	}
	device, found, err := findDevice(ctx, cfg, resolve, logger)
	if err != nil {
		// Nothing answered. If we have been here before and still hold a
		// catalogue, serve it: the unit being off is no reason to withhold the
		// song list and the favorites, and Device keeps retrying in the background
		// so it connects by itself when the unit comes back.
		fallback := fallbackAddr(err, cfg.LastAddrFile, cfg.SongDB)
		switch {
		case fallback != "":
			logger.Warn("no device answered; starting offline with the last known address",
				"addr", fallback)
			device, found = fallback, nil
		case errors.Is(err, client.ErrNoDevice):
			return fmt.Errorf("%w; is it switched on and on this network?", err)
		default:
			return err
		}
	} else if cfg.LastAddrFile != "" {
		rememberAddr(cfg.LastAddrFile, device, logger)
	}
	switch {
	case len(found) == 1:
		logger.Info("found device", "addr", device, "info", found[0].Info.String())
	case len(found) > 1:
		others := make([]string, 0, len(found)-1)
		for _, d := range found[1:] {
			others = append(others, d.Addr)
		}
		logger.Info("found several devices; using the first",
			"addr", device, "info", found[0].Info.String(), "others", strings.Join(others, ", "))
	}

	songs, err := openCatalogue(ctx, device, identity, cfg.SongDB, logger, cfg.Progress)
	if err != nil {
		return err
	}
	defer songs.Close()

	favs, err := favorites.Open(cfg.FavDB)
	if err != nil {
		return err
	}
	defer favs.Close()
	logger.Info("favorites ready", "file", favs.Path(), "folderDelete", cfg.AllowFolderDelete)

	info := songs.Info()
	logger.Info("song catalogue ready",
		"file", songs.Path(), "songs", info.Songs, "artists", info.Singers, "version", info.DBVersion)

	// The server is built first so the device's status callback can publish into
	// it from the moment the session opens.
	var srv *webui.Server
	dev := webui.NewDevice(device, identity, logger, func(st *protocol.Status) {
		if srv != nil {
			srv.PublishStatus(st)
		}
	})
	srv, err = webui.New(dev, songs, favs, logger, webui.Options{
		AllowFolderDelete: cfg.AllowFolderDelete,
		SaveFile:          cfg.SaveFile,
	})
	if err != nil {
		return err
	}
	srv.WatchConnection(dev)
	srv.WatchQueue(dev)

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

		// Cancelling ctx is what tells the session to close, but closing it is
		// not instant: client.Close waits for the disconnect notification to
		// actually go out, because go-raknet defers sending it to a tick loop a
		// second later. Returning here without waiting would let the process exit
		// first, and the unit would go on showing a client connected until its own
		// timeout expired — which main's SIGTERM handling exists to prevent.
		//
		// It matters on Android, which stops the server
		// every time the phone has been away from it for ten minutes.
		select {
		case <-devDone:
		case <-time.After(client.CloseTimeout + time.Second):
			logger.Warn("device session did not close in time")
		}
		return err
	}
}

// searchPause is the wait between searches while WaitForDevice keeps looking.
var searchPause = 2 * time.Second

// findDevice runs resolve, and with cfg.WaitForDevice runs it again for as long
// as it fails and there is no remembered address to fall back on.
func findDevice(ctx context.Context, cfg Config,
	resolve func(context.Context) (string, []client.Device, error),
	logger *slog.Logger) (string, []client.Device, error) {
	for {
		device, found, err := resolve(ctx)
		if err == nil || !cfg.WaitForDevice || ctx.Err() != nil ||
			fallbackAddr(err, cfg.LastAddrFile, cfg.SongDB) != "" {
			return device, found, err
		}
		logger.Info("no device found; still looking", "error", err)
		if cfg.Searching != nil {
			cfg.Searching()
		}
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(searchPause):
		}
	}
}

// openCatalogue opens the song database, downloading it from the device if it is
// missing or unusable.
func openCatalogue(ctx context.Context, addr string, identity protocol.Identity, path string,
	logger *slog.Logger, progress func(have, total int)) (*catalog.DB, error) {
	if db, err := catalog.Open(path); err == nil {
		return db, nil
	} else if !os.IsNotExist(errors.Unwrap(err)) && !os.IsNotExist(err) {
		logger.Warn("existing song database unusable, re-downloading", "file", path, "error", err)
	}

	logger.Info("downloading song database from the device", "device", addr)
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	c, err := client.Dial(dialCtx, addr, client.Options{Identity: &identity, Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalogue, err)
	}
	defer c.Close()

	var lastPct int
	err = catalog.Fetch(ctx, c, path, func(have, total int) {
		if progress != nil {
			progress(have, total)
		}
		if total == 0 {
			return
		}
		// Logged in tenths, because every chunk would be hundreds of lines.
		if pct := have * 100 / total; pct >= lastPct+10 {
			lastPct = pct
			logger.Info("downloading", "percent", pct, "bytes", have, "of", total)
		}
	})
	if err != nil {
		return nil, err
	}
	return catalog.Open(path)
}
