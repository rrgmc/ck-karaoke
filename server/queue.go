package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
	"github.com/rrgmc/ck-karaoke/queue/remote"
	"github.com/rrgmc/ck-karaoke/webui"
)

// The backends a build can have.
const (
	// BackendUnit drives the karaoke unit over RakNet: CK Karaoke.
	BackendUnit = "unit"
	// BackendQueue drives a CK Queue Manager over its HTTP API: CK Queue
	// Remote.
	BackendQueue = "queue"
)

func (c Config) backend() string {
	if c.Backend == "" {
		return BackendUnit
	}
	return c.Backend
}

// queueSearch is how long one mDNS search for a queue manager listens, and
// queueSweep how long one sweep of the local networks may take.
const (
	queueSearch = 3 * time.Second
	queueSweep  = 10 * time.Second
)

// runQueue is Run for BackendQueue. It never opens a session with the unit.
//
// Like the unit backend it serves at once: the song list and the favorites
// need no queue manager. The backend finds the queue manager in the
// background, and the song list comes from it, or from an imported file,
// when there is none yet.
func runQueue(ctx context.Context, cfg Config, logger *slog.Logger) error {
	promoteSongs(cfg.SongDB, logger)
	songs := openSongs(cfg.SongDB, logger)

	base := ""
	switch {
	case cfg.Addr != "":
		base = remote.BaseURL(cfg.Addr)
	case cfg.LastAddrFile != "":
		if addr := readAddr(cfg.LastAddrFile); addr != "" {
			base = remote.BaseURL(addr)
		}
	}
	c := kmclient.New(base, &http.Client{Timeout: 10 * time.Second})

	favs, err := favorites.Open(cfg.FavDB)
	if err != nil {
		if songs != nil {
			songs.Close()
		}
		return err
	}
	defer favs.Close()

	backend := remote.New(c, logger)
	// Beside the song database, which every port puts in the app's own
	// storage, so the code entered on the Setup page is kept with the rest.
	backend.UseTokenFile(filepath.Join(filepath.Dir(cfg.SongDB), "access.json"))
	backend.UseSingerFile(filepath.Join(filepath.Dir(cfg.SongDB), "singer.json"))
	if cfg.Addr == "" {
		// An address the caller gave is the caller's to keep.
		backend.SetResolver(queueResolver(cfg, logger))
	}
	srv, err := webui.New(backend, songs, favs, logger, webui.Options{
		AllowFolderDelete: cfg.AllowFolderDelete,
		SaveFile:          cfg.SaveFile,
		AppName:           "CK Queue Remote",
		PeerApp:           cfg.PeerApp,
		SongListPath:      cfg.SongDB,
		FetchSongList: func(ctx context.Context, path string, _ func(have, total int)) error {
			_, err := remote.SyncCatalogueTo(ctx, c, path, path)
			return err
		},
	})
	if err != nil {
		if songs != nil {
			songs.Close()
		}
		return err
	}
	defer srv.Close()

	// Each time the queue manager is reached: fetch the song list if there
	// is none, or else check the one there is, once per run.
	var checked atomic.Bool
	backend.OnReached(func() {
		if srv.Catalogue() == nil {
			srv.FetchSongList()
			return
		}
		if checked.Swap(true) {
			return
		}
		stageSongs(ctx, c, cfg.SongDB, logger)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		backend.Run(ctx)
	}()
	return serve(ctx, cfg, logger, srv, base, done, time.Second)
}

// stagedSuffix names a newer song list waiting for the next start. The one in
// use stays open while the app runs, and Windows cannot replace an open file.
const stagedSuffix = ".new"

// stageSongs downloads the queue manager's song list beside the one in use
// when the two differ, for promoteSongs to install at the next start.
func stageSongs(ctx context.Context, c *kmclient.Client, path string, logger *slog.Logger) {
	staged := path + stagedSuffix
	changed, err := remote.SyncCatalogueTo(ctx, c, path, staged)
	switch {
	case err != nil:
		logger.Warn("cannot check the song database against the queue manager", "error", err)
	case changed:
		logger.Info("downloaded a newer song database; it is used from the next start", "file", staged)
	default:
		// The queue manager has the list in use, so an older download that
		// is still waiting would only go backwards.
		os.Remove(staged)
	}
}

// promoteSongs installs a song list stageSongs left, before anything opens
// the one in use. A staged file that does not verify is dropped.
func promoteSongs(path string, logger *slog.Logger) {
	if path == "" {
		return
	}
	staged := path + stagedSuffix
	if !exists(staged) {
		return
	}
	if err := catalog.Verify(staged); err != nil {
		logger.Warn("dropping a damaged song database download", "file", staged, "error", err)
		os.Remove(staged)
		return
	}
	if err := os.Rename(staged, path); err != nil {
		logger.Warn("cannot install the newer song database", "file", staged, "error", err)
		return
	}
	logger.Info("installed the newer song database", "file", path)
}

// queueResolver is the backend's search. It keeps the current address while
// the queue manager answers there, and remembers whatever it settles on.
func queueResolver(cfg Config, logger *slog.Logger) remote.Resolver {
	search := remote.Browse
	timeout := queueSearch
	if cfg.QueueSweep {
		search = func(ctx context.Context, t time.Duration) ([]remote.Found, error) {
			return remote.SweepNetworks(ctx, t, cfg.SweepNetworks)
		}
		timeout = queueSweep
	}
	if cfg.DiscoverTimeout > 0 {
		timeout = cfg.DiscoverTimeout
	}
	return func(ctx context.Context, current string) (string, error) {
		if current != "" {
			if _, err := remote.Check(ctx, current); err == nil {
				return current, nil
			}
		}
		found, err := search(ctx, timeout)
		if err != nil {
			return "", err
		}
		base := found[0].URL
		for _, f := range found {
			if f.URL == current {
				base = current
			}
		}
		logger.Info("found the queue manager", "addr", base, "name", found[0].Name, "others", len(found)-1)
		if cfg.LastAddrFile != "" {
			if u, err := url.Parse(base); err == nil {
				rememberAddr(cfg.LastAddrFile, u.Host, logger)
			}
		}
		return base, nil
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return path != "" && err == nil
}
