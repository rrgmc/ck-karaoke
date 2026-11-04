package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
func runQueue(ctx context.Context, cfg Config, logger *slog.Logger) error {
	base, reached, err := findQueueManager(ctx, cfg, logger)
	if err != nil {
		return err
	}
	c := kmclient.New(base, &http.Client{Timeout: 10 * time.Second})

	if reached {
		if cfg.LastAddrFile != "" {
			if u, err := url.Parse(base); err == nil {
				rememberAddr(cfg.LastAddrFile, u.Host, logger)
			}
		}
		downloaded, err := remote.SyncCatalogue(ctx, c, cfg.SongDB)
		switch {
		case err != nil && !exists(cfg.SongDB):
			return fmt.Errorf("%w: %w", ErrCatalogue, err)
		case err != nil:
			logger.Warn("cannot update the song database; keeping the one there is", "error", err)
		case downloaded:
			logger.Info("downloaded the song database from the queue manager", "file", cfg.SongDB)
		}
	}

	songs, err := catalog.Open(cfg.SongDB)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCatalogue, err)
	}
	defer songs.Close()
	favs, err := favorites.Open(cfg.FavDB)
	if err != nil {
		return err
	}
	defer favs.Close()
	info := songs.Info()
	logger.Info("song catalogue ready", "file", songs.Path(), "songs", info.Songs, "version", info.DBVersion)

	backend := remote.New(c, logger)
	// Beside the song database, which every port puts in the app's own
	// storage, so the code entered on the Setup page is kept with the rest.
	backend.UseTokenFile(filepath.Join(filepath.Dir(cfg.SongDB), "access.json"))
	backend.UseSingerFile(filepath.Join(filepath.Dir(cfg.SongDB), "singer.json"))
	srv, err := webui.New(backend, songs, favs, logger, webui.Options{
		AllowFolderDelete: cfg.AllowFolderDelete,
		SaveFile:          cfg.SaveFile,
		AppName:           "CK Queue Remote",
	})
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		backend.Run(ctx)
	}()
	return serve(ctx, cfg, logger, srv, base, done, time.Second)
}

// findQueueManager returns the queue manager's base URL and whether it
// answered. One that did not answer is returned only when a song database is
// already there to browse, so the pages can open and the backend keeps
// trying; without one, and with cfg.WaitForDevice, the search goes on.
func findQueueManager(ctx context.Context, cfg Config, logger *slog.Logger) (string, bool, error) {
	if cfg.Addr != "" {
		base := remote.BaseURL(cfg.Addr)
		if _, err := remote.Check(ctx, base); err != nil {
			if !exists(cfg.SongDB) {
				return "", false, fmt.Errorf("the queue manager at %s: %w", base, err)
			}
			logger.Warn("the queue manager does not answer; starting with the song database there is",
				"addr", base, "error", err)
			return base, false, nil
		}
		return base, true, nil
	}

	remembered := ""
	if cfg.LastAddrFile != "" {
		if addr := readAddr(cfg.LastAddrFile); addr != "" {
			remembered = remote.BaseURL(addr)
			if f, err := remote.Check(ctx, remembered); err == nil {
				logger.Info("found the queue manager", "addr", f.URL, "name", f.Name)
				return remembered, true, nil
			}
		}
	}

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
	for {
		found, err := search(ctx, timeout)
		if err == nil {
			logger.Info("found the queue manager", "addr", found[0].URL, "name", found[0].Name,
				"others", len(found)-1)
			return found[0].URL, true, nil
		}
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if remembered != "" && exists(cfg.SongDB) {
			logger.Warn("no queue manager answered; starting with the last known address", "addr", remembered)
			return remembered, false, nil
		}
		if !cfg.WaitForDevice {
			if errors.Is(err, remote.ErrNoQueueManager) {
				return "", false, fmt.Errorf("%w; is it running on this network?", err)
			}
			return "", false, err
		}
		logger.Info("no queue manager found; still looking", "error", err)
		if cfg.Searching != nil {
			cfg.Searching()
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(searchPause):
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return path != "" && err == nil
}
