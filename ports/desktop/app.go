package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/i18n"
	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/rrgmc/ck-karaoke/server"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Events the startup page listens for.
const (
	eventReady     = "server:ready"     // the URL to navigate to
	eventProgress  = "server:progress"  // catalogue download percent
	eventSearching = "server:searching" // a search found nothing; still looking
	eventFailed    = "server:failed"    // why the server stopped
)

// stopWait bounds how long closing the window waits for the device session to
// close. The client waits up to client.CloseTimeout for the disconnect to go
// out; the margin is for the HTTP shutdown before it.
const stopWait = client.CloseTimeout + 3*time.Second

// App owns the server for the life of the window. Its exported methods are
// bound to the startup page.
type App struct {
	ctx    context.Context
	logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	url string
	err string
	// lang is the language of the native dialogs, set by the startup page from
	// the web view's languages so it matches the web UI.
	lang i18n.Lang
}

func newApp() *App {
	// CK_DEBUG=1 logs every request, which is the way to see what the window
	// is doing.
	level := slog.LevelInfo
	if os.Getenv("CK_DEBUG") != "" {
		level = slog.LevelDebug
	}
	return &App{
		done:   make(chan struct{}),
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
	}
}

// startup starts the server once the window exists.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	runCtx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	dir, err := dataDir()
	if err != nil {
		a.fail(err)
		close(a.done)
		return
	}
	logger := a.logger
	logger.Info("data directory", "path", dir)

	cfg := server.Config{
		Listen:       "127.0.0.1:0",
		SongDB:       filepath.Join(dir, "song.db"),
		FavDB:        filepath.Join(dir, "favorites.db"),
		LastAddrFile: filepath.Join(dir, "device.addr"),
		Name:         protocol.HostName(),
		Logger:       logger,
		SaveFile:     a.saveFile,
		// A first launch waits for the user to answer the local network prompt.
		WaitForDevice: true,
		Searching: func() {
			runtime.EventsEmit(ctx, eventSearching)
		},
		Ready: func(listen net.Addr, device string) {
			url := "http://" + listen.String() + "/"
			a.mu.Lock()
			a.url = url
			a.mu.Unlock()
			logger.Info("listening", "url", url, "device", device)
			runtime.EventsEmit(ctx, eventReady, url)
		},
		Progress: func(have, total int) {
			if total > 0 {
				runtime.EventsEmit(ctx, eventProgress, have*100/total)
			}
		},
	}

	go func() {
		defer close(a.done)
		err := server.Run(runCtx, cfg)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.fail(err)
		}
	}()
}

// fail reports a server that stopped on its own. Before the page has moved to
// the server the startup page shows it; after, only a dialog can.
func (a *App) fail(err error) {
	a.logger.Error("server stopped", "error", err)
	a.mu.Lock()
	a.err = err.Error()
	navigated := a.url != ""
	lang := a.lang
	a.mu.Unlock()

	if !navigated {
		runtime.EventsEmit(a.ctx, eventFailed, err.Error())
		return
	}
	_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:    runtime.ErrorDialog,
		Title:   lang.T("CK Karaoke stopped"),
		Message: err.Error(),
	})
	runtime.Quit(a.ctx)
}

// shutdown stops the server and waits for it to close the device session, so
// the device does not hold a connection slot for a client that has gone.
func (a *App) shutdown(context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
	select {
	case <-a.done:
	case <-time.After(stopWait):
	}
}

// ServerURL returns the server's URL once it is listening, or "" before that.
// The startup page asks on load, in case the ready event fired first.
func (a *App) ServerURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.url
}

// SetLanguage takes the web view's preferred languages, which the startup page
// reads from navigator.languages, and returns that page's text in the best
// match. The native dialogs use the same language from then on.
//
// The web view is asked rather than the system, because it is what the web UI
// follows through Accept-Language, and the dialogs should agree with it.
func (a *App) SetLanguage(prefs []string) map[string]string {
	lang := i18n.Match(prefs...)
	a.mu.Lock()
	a.lang = lang
	a.mu.Unlock()
	return map[string]string{
		"looking":     lang.T("Looking for your karaoke device…"),
		"hint":        lang.T("No device has answered yet. Check that it is switched on and on the same network as this computer. On a Mac, also check that CK Karaoke is allowed in System Settings, Privacy & Security, Local Network."),
		"downloading": lang.T("Downloading the song list…"),
		"stopped":     lang.T("The server stopped:"),
	}
}

// ServerError returns why the server stopped, or "" if it has not.
func (a *App) ServerError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

// saveFile is the server's native save path: the web view has no download
// handling of its own on every platform, so a backup goes through the system's
// save dialog instead.
func (a *App) saveFile(name string, data []byte) (string, error) {
	a.mu.Lock()
	lang := a.lang
	a.mu.Unlock()
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           lang.T("Save favorites backup"),
		DefaultFilename: name,
		Filters:         []runtime.FileFilter{{DisplayName: lang.T("Favorites backup (*.xml)"), Pattern: "*.xml"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// dataDir is where the catalogue and the favorites live: the per-user
// configuration directory, so they survive reinstalling the app.
func dataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("finding the configuration directory: %w", err)
	}
	dir := filepath.Join(base, "CK Karaoke")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}
