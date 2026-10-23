// Command ck-karaoke-server serves a mobile web UI for a karaoke device.
//
// It holds one long-lived session to the device and a read-only copy of the
// song catalogue, which it downloads from the device on first run.
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
	"sort"
	"strings"
	"syscall"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/server"
)

func main() {
	// SIGTERM as well as interrupt: the Android wrapper stops the server with
	// Process.destroy(), which sends SIGTERM. Without it the server dies on the
	// spot, never closes its session, and the karaoke device goes on showing a
	// client connected until its own timeout expires.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// And survive our own stdout being closed, which is the other half of that
	// same story. Java's Process.destroy() sends the signal and then immediately
	// closes all three streams, so the pipe this server logs down is gone before
	// it has finished reacting. Go's default for EPIPE on fd 1 or 2 is to die by
	// SIGPIPE, so the first line the shutdown logged killed it mid-shutdown —
	// exit 141, no disconnect sent, and the "shutting down" line never even
	// reached logcat to say so.
	//
	// Notify is what turns that back into an ordinary failed write: with a
	// listener registered the runtime delivers SIGPIPE here instead of killing
	// the process, and the write just returns EPIPE. The channel is drained and
	// discarded, because there is nothing to do about it — losing log output to
	// a parent that has stopped reading is not an error worth acting on.
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
		addr     = flag.String("addr", "", "karaoke device address; found by broadcast if empty")
		discTO   = flag.Duration("discover-timeout", client.DefaultDiscoverTimeout, "how long to listen for a device when -addr is not given")
		listen   = flag.String("listen", ":8080", "address to serve on")
		dataDirF = flag.String("data", "", "directory for the databases; defaults to data/ beside the binary")
		dbPath   = flag.String("catalog", "", "song database file; defaults to data/song.db beside the binary, downloaded from the device if missing")
		favPath  = flag.String("favorites", "", "favorites database file; defaults to data/favorites.db beside the binary")
		allowDel = flag.Bool("allow-folder-delete", false, "allow deleting favorites folders, which cannot be undone and is off by default")
		name     = flag.String("name", "", "name the device shows beside songs queued here; the hostname if empty")
		refresh  = flag.Bool("refresh-db", false, "re-download the song database even if one exists")
		verbose  = flag.Bool("v", false, "verbose logging")
	)
	flag.Parse()

	dir, err := dataDir(*dataDirF)
	if err != nil {
		return err
	}
	songPath := dataFile(*dbPath, dir, "song.db")
	favoritesPath := dataFile(*favPath, dir, "favorites.db")

	if *refresh {
		if err := os.Remove(songPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", songPath, err)
		}
	}

	cfg := server.Config{
		Addr:              *addr,
		DiscoverTimeout:   *discTO,
		Listen:            *listen,
		SongDB:            songPath,
		FavDB:             favoritesPath,
		LastAddrFile:      filepath.Join(dir, "device.addr"),
		AllowFolderDelete: *allowDel,
		Name:              *name,
		Verbose:           *verbose,

		// Nil everywhere but Android, which forks this binary and sets the
		// variables first, because Go on Android believes it is in UTC.
		Local: server.LocalZone(os.Getenv),

		Ready: func(a net.Addr, device string) {
			// One machine-readable line, always, before anything starts serving:
			// the Android wrapper binds to port 0 and needs to know what it got,
			// and waiting for this line beats polling for the port to open.
			fmt.Println("listening", a)

			// The LAN listing only makes sense when the server is actually
			// reachable from the LAN. Bound to loopback — which is how it runs on
			// a phone, so that nothing else on the WiFi can reach it —
			// enumerating interfaces would be noise at best, and Android
			// restricts that enumeration anyway.
			if !isLoopback(a) {
				printAddresses(a, device)
			}
		},
	}

	err = server.Run(ctx, cfg)
	// The hints are flag-specific, so they are added here rather than raised
	// from a server that may not have a command line at all.
	switch {
	case errors.Is(err, client.ErrNoDevice):
		return fmt.Errorf("%w pass -addr to skip the search", err)
	case errors.Is(err, server.ErrCatalogue):
		return fmt.Errorf("%w\n(supply one with -catalog if you already have song.db)", err)
	}
	return err
}

// dataPath resolves a database location. An explicit path is used as given;
// otherwise the file lives in data/ beside the executable.
//
// Beside the binary rather than in the working directory, so starting the
// server from anywhere reaches the same catalogue and the same favorites —
// otherwise a stray song.db is downloaded into whichever directory the server
// happened to be launched from, and the favorites that go with it are invisible
// from the next launch. Under `go run` the binary is in a temporary directory,
// so pass the flags explicitly there.
func dataDir(explicit string) (string, error) {
	dir := explicit
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locating the binary to place the databases: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir = filepath.Join(filepath.Dir(exe), "data")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}

// dataFile is one database inside that directory, unless it was named outright.
func dataFile(explicit, dir, name string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(dir, name)
}

// isLoopback reports whether the listener is reachable only from this machine.
func isLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP != nil && tcp.IP.IsLoopback()
}

// printAddresses tells the user which URL to open on their phone. Guessing the
// right interface is the single most annoying part of using a LAN service, so
// every candidate is listed rather than just one.
func printAddresses(listenAddr net.Addr, device string) {
	port := "8080"
	if tcp, ok := listenAddr.(*net.TCPAddr); ok {
		port = fmt.Sprint(tcp.Port)
	}

	fmt.Println()
	fmt.Printf("  CK Karaoke web UI — device %s\n\n", device)

	urls := lanURLs(port, device)
	if len(urls) == 0 {
		fmt.Printf("    http://localhost:%s\n", port)
		fmt.Println("\n  No LAN address found; the phone will not be able to reach this.")
	} else {
		fmt.Println("  Open on your phone:")
		for _, u := range urls {
			fmt.Printf("    %s\n", u)
		}
		fmt.Printf("\n  On this machine: http://localhost:%s\n", port)
	}
	fmt.Println()
}

// lanURLs returns a URL for each usable IPv4 address, most likely first.
//
// device is the karaoke unit's address: an interface on the same subnet as the
// device is almost certainly the one the phone is on too, which beats any
// guess based on address ranges alone.
func lanURLs(port, device string) []string {
	deviceIP := parseHostIP(device)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	type candidate struct {
		url  string
		rank int
	}
	var found []candidate

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			rank := rankInterface(iface.Name, ip)
			if deviceIP != nil && sameSubnet(ipnet, deviceIP) {
				rank = -1 // same subnet as the device
			}
			found = append(found, candidate{
				url:  fmt.Sprintf("http://%s:%s", ip, port),
				rank: rank,
			})
		}
	}

	sort.SliceStable(found, func(i, j int) bool { return found[i].rank < found[j].rank })
	out := make([]string, len(found))
	for i, c := range found {
		out[i] = c.url
	}
	return out
}

// rankInterface puts the address a phone is most likely to reach first. Virtual
// adapters from WSL, Docker and Hyper-V are on private ranges too but are not
// reachable from the network the phone is on, so they sink to the bottom.
func rankInterface(name string, ip net.IP) int {
	switch {
	case containsAny(name, "vEthernet", "WSL", "Docker", "VirtualBox", "VMware", "Hyper-V", "Loopback"):
		return 3
	case ip[0] == 192 && ip[1] == 168:
		return 0
	case ip[0] == 10:
		return 1
	case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
		return 2
	default:
		return 4
	}
}

// sameSubnet reports whether the device shares a network with this address.
//
// The mask has to be checked, not just Contains: VPN and tunnel adapters can
// report a 0.0.0.0 mask, whose network trivially contains every address and
// would otherwise win the ranking outright.
func sameSubnet(n *net.IPNet, ip net.IP) bool {
	ones, bits := n.Mask.Size()
	if bits != 32 || ones < 8 || ones >= 32 {
		return false
	}
	return n.Contains(ip)
}

// parseHostIP extracts an IP from a bare address or a host:port pair.
func parseHostIP(addr string) net.IP {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	return net.ParseIP(addr).To4()
}

func containsAny(s string, subs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
