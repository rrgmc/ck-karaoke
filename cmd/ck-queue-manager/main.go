// Command ck-queue-manager runs the CK Karaoke queue manager: the one program
// that holds a session with the karaoke unit. It keeps the song queue, with an
// optional fair order, feeds the unit one song ahead, and serves
// KaraokeMachine's API so that KM Remote connects to it unchanged.
//
// The API listens on the LAN, on port 8177. The configuration page listens on
// loopback only.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rrgmc/ck-karaoke/queue/manager"
	"github.com/rrgmc/ck-karaoke/version"
)

func main() {
	// SIGTERM as well as interrupt, so a service manager's stop still closes
	// the unit session. And SIGPIPE is caught, so a write to a closed stdout
	// fails instead of killing the process mid-shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sigpipe := make(chan os.Signal, 1)
	signal.Notify(sigpipe, syscall.SIGPIPE)
	go func() {
		for range sigpipe {
		}
	}()

	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var (
		addr    = flag.String("addr", "", "karaoke unit address; found by model and database id if empty")
		listen  = flag.String("listen", ":8177", "address to serve the KaraokeMachine API on")
		admin   = flag.String("admin", manager.DefaultAdminListen, "loopback address to serve the configuration page on")
		data    = flag.String("data", "", "data directory; defaults to the user configuration directory")
		verbose = flag.Bool("v", false, "verbose logging")
		showVer = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version.Version)
		return nil
	}
	dir := *data
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(base, "CK Karaoke Queue Manager")
	}
	return manager.Run(ctx, manager.Config{
		DataDir: dir, Listen: *listen, AdminListen: *admin, Addr: *addr, Verbose: *verbose,
		Ready: func(api, adm net.Addr) {
			fmt.Println("listening", api)
			fmt.Printf("configuration page: http://%s/\n", adm)
		},
	})
}
