// Command ck-queue-manager-tray runs the CK Karaoke queue manager from the
// system tray, with Open configuration, Start/Stop and Quit. Open
// configuration shows the configuration page in a window of its own, a Wails
// web view, run by the same executable with -window.
//
// It is its own module, like ports/desktop, so that the library does not depend
// on a tray or web view library. Wails needs the desktop and production build
// tags, and webkit2_41 on Linux. Build it on Windows with -ldflags=-H=windowsgui,
// so that no console window opens; it then logs to queue-manager.log in its
// data directory.
package main

//go:generate go run ./internal/winres

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	"fyne.io/systray"

	"github.com/rrgmc/ck-karaoke/queue/manager"
)

//go:embed icon.png
var iconPNG []byte

//go:embed icon.ico
var iconICO []byte

func main() {
	data := flag.String("data", "", "data directory; defaults to the user configuration directory")
	listen := flag.String("listen", "", "address to serve the KaraokeMachine API on; defaults to :8177")
	window := flag.Bool(windowFlag, false, "show the configuration page at the URL argument in a window, instead of the tray; the tray app starts it")
	flag.Parse()
	dir, err := dataDir(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if *window {
		// The tray app takes the window's output into its own log.
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		err := errNoURL
		if url := flag.Arg(0); url != "" {
			err = runWindow(url, dir, logger)
		}
		if err != nil {
			logger.Error("the configuration window failed", "error", err)
			os.Exit(1)
		}
		return
	}
	logger, logOut, closeLog := openLog(dir)
	defer closeLog()
	svc := &service{dir: dir, listen: *listen, log: logger, logOut: logOut}
	systray.Run(svc.ready, svc.stop)
}

func dataDir(dir string) (string, error) {
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "CK Karaoke Queue Manager")
	}
	return dir, os.MkdirAll(dir, 0o755)
}

// openLog logs to stderr and to queue-manager.log, since a tray app built for
// the Windows GUI subsystem has no console.
// The writer is the log's, for the window process's output.
func openLog(dir string) (*slog.Logger, io.Writer, func()) {
	f, err := os.OpenFile(filepath.Join(dir, "queue-manager.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stderr, nil)), os.Stderr, func() {}
	}
	out := io.MultiWriter(os.Stderr, f)
	return slog.New(slog.NewTextHandler(out, nil)), out, func() { f.Close() }
}

// service runs one queue manager at a time.
type service struct {
	dir    string
	listen string
	log    *slog.Logger
	logOut io.Writer

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	admin  string

	status, toggle *systray.MenuItem
}

func (s *service) ready() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(iconICO)
	} else {
		systray.SetIcon(iconPNG)
	}
	systray.SetTitle("")
	systray.SetTooltip("CK Karaoke queue manager")
	s.status = systray.AddMenuItem("Starting…", "")
	s.status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem("Open configuration", "Open the configuration page in a window")
	s.toggle = systray.AddMenuItem("Stop", "Stop or start the queue manager")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Stop the queue manager and quit")

	// A service manager's stop, or Ctrl+C from a console, quits cleanly too.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	s.start()
	go func() {
		for {
			select {
			case <-open.ClickedCh:
				s.openPage()
			case <-s.toggle.ClickedCh:
				if s.running() {
					s.stop()
				} else {
					s.start()
				}
			case <-quit.ClickedCh:
				systray.Quit()
				return
			case <-sig:
				systray.Quit()
				return
			}
		}
	}()
}

func (s *service) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel != nil
}

// start runs the queue manager on a goroutine.
func (s *service) start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.mu.Unlock()

	s.setStatus("Starting…", "Stop")
	go func() {
		defer close(done)
		err := manager.Run(ctx, manager.Config{
			DataDir: s.dir, Listen: s.listen, Logger: s.log,
			Ready: func(api, admin net.Addr) {
				s.mu.Lock()
				s.admin = admin.String()
				s.mu.Unlock()
				s.setStatus("Running on "+api.String(), "Stop")
			},
		})
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			s.log.Error("the queue manager stopped", "error", err)
			s.setStatus("Stopped: "+err.Error(), "Start")
			return
		}
		s.setStatus("Stopped", "Start")
	}()
}

// stop stops the queue manager and waits until the unit session has closed.
func (s *service) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	s.setStatus("Stopping…", "Start")
	cancel()
	<-done
}

func (s *service) setStatus(text, toggle string) {
	if s.status != nil {
		s.status.SetTitle(text)
	}
	if s.toggle != nil {
		s.toggle.SetTitle(toggle)
	}
}

func (s *service) openPage() {
	s.mu.Lock()
	url := "http://" + s.admin + "/"
	if s.admin == "" {
		url = "http://" + manager.DefaultAdminListen + "/"
	}
	s.mu.Unlock()
	s.openWindow(url)
}

// openBrowser opens url in the default browser, for when the window cannot
// run.
func (s *service) openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		s.log.Warn("cannot open the browser", "url", url, "error", err)
	}
}
