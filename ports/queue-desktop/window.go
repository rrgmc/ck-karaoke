package main

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// windowFlag starts the app as the configuration window instead of the tray.
//
// The window runs in a process of its own. Both the tray and the window need
// the main thread and its event loop (on macOS the one NSApplication), so they
// cannot share a process.
const windowFlag = "window"

//go:embed window.html
var windowHTML string

// appIconPNG is the window's icon on Linux. Windows takes it from the
// executable's resources (internal/winres), macOS from the app bundle.
//
//go:embed appicon.png
var appIconPNG []byte

var windowPage = template.Must(template.New("window").Parse(windowHTML))

// openWindow starts the configuration window on url. A window already open
// for the same page comes to the front instead, through Wails' single
// instance lock, and the new process exits.
//
// The window reads its standard input and closes at end of file, so it closes
// when the tray app quits, however it quits. If the window cannot run, for
// example because the system has no web view, the page opens in the browser.
func (s *service) openWindow(url string) {
	exe, err := os.Executable()
	if err != nil {
		s.log.Warn("cannot find the executable; opening the browser", "error", err)
		s.openBrowser(url)
		return
	}
	cmd := exec.Command(exe, "-"+windowFlag, url, "-data", s.dir)
	cmd.Stdout, cmd.Stderr = s.logOut, s.logOut
	if _, err := cmd.StdinPipe(); err != nil {
		s.log.Warn("cannot start the configuration window; opening the browser", "error", err)
		s.openBrowser(url)
		return
	}
	if err := cmd.Start(); err != nil {
		s.log.Warn("cannot start the configuration window; opening the browser", "error", err)
		s.openBrowser(url)
		return
	}
	go func() {
		// Wait also closes the pipe once the window has exited.
		if err := cmd.Wait(); err != nil {
			s.log.Warn("the configuration window failed; opening the browser", "error", err)
			s.openBrowser(url)
		}
	}()
}

// runWindow is the configuration window's process: a Wails window that shows
// a waiting page from its own assets until the queue manager answers on url,
// then navigates to it. From there on it is a web view onto the page.
func runWindow(url, dir string, log *slog.Logger) error {
	var ctx context.Context
	front := func(options.SecondInstanceData) {
		if ctx == nil {
			return
		}
		wailsruntime.WindowUnminimise(ctx)
		wailsruntime.WindowShow(ctx)
		// Windows lets a process take the foreground only in some cases; a
		// window that is briefly always on top comes forward in all of them.
		wailsruntime.WindowSetAlwaysOnTop(ctx, true)
		wailsruntime.WindowSetAlwaysOnTop(ctx, false)
	}

	// macOS gives the window process its own menu bar. Without an Edit menu,
	// the copy and paste shortcuts do nothing in the page's fields.
	var bar *menu.Menu
	if runtime.GOOS == "darwin" {
		bar = menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu(), menu.WindowMenu())
	}

	return wails.Run(&options.App{
		Title:     "CK Queue Manager",
		Width:     1024,
		Height:    760,
		MinWidth:  420,
		MinHeight: 400,
		Menu:      bar,
		AssetServer: &assetserver.Options{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if err := windowPage.Execute(w, url); err != nil {
					log.Warn("cannot render the waiting page", "error", err)
				}
			}),
		},
		OnStartup: func(c context.Context) {
			ctx = c
			go func() {
				_, _ = io.Copy(io.Discard, os.Stdin)
				wailsruntime.Quit(c)
			}()
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "com.rrgmc.ckqueuemanager.window." + instanceKey(url),
			OnSecondInstanceLaunch: front,
		},
		Windows: &windows.Options{
			// Beside the queue manager's data, not in a folder named after
			// the executable.
			WebviewUserDataPath: filepath.Join(dir, "webview"),
		},
		Linux:    &linux.Options{Icon: appIconPNG, ProgramName: "ck-queue-manager"},
		Logger:   logger.NewDefaultLogger(),
		LogLevel: logger.WARNING,
	})
}

// instanceKey makes url safe for the single instance lock's id, which names a
// window class, a socket or a D-Bus name depending on the platform.
func instanceKey(url string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.TrimPrefix(url, "http://"))
}

// errNoURL is returned for -window without a page.
var errNoURL = errors.New("-window needs the configuration page's URL")
