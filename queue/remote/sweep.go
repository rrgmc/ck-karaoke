package remote

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
)

// sweepWorkers is how many connections a sweep has open at once, and
// sweepDial how long each waits. A host that is not there either refuses at
// once or stays silent until the deadline.
const (
	sweepWorkers = 64
	sweepDial    = 400 * time.Millisecond
)

// Sweep finds a CK Queue Manager by trying the API port, kmapi.DefaultPort, on
// every host of the local networks, nearest first, and returns the first
// that answers Check.
//
// This is for iOS, where mDNS from Go needs the multicast entitlement, which
// Apple grants only on request. Unicast needs nothing beyond the local network
// prompt. It is also why the queue manager must listen on 8177.
func Sweep(ctx context.Context, timeout time.Duration) ([]Found, error) {
	return SweepNetworks(ctx, timeout, nil)
}

// SweepNetworks is Sweep over the given networks, each written as this
// machine's address and mask. With none, it walks this machine's interfaces as
// Sweep does.
//
// Android needs the networks passed in: an app there may not list the
// interfaces, so Go finds none, but the app can read its own network's address
// and prefix from the system.
func SweepNetworks(ctx context.Context, timeout time.Duration, nets []*net.IPNet) ([]Found, error) {
	var hosts []net.IP
	if len(nets) > 0 {
		hosts = client.NetworkHosts(nets)
	} else {
		var err error
		if hosts, err = client.LocalHosts(); err != nil {
			return nil, err
		}
	}
	// LocalHosts leaves this machine out, which is right for the unit. A queue
	// manager may run on this machine, though, so it is tried first.
	hosts = append([]net.IP{net.IPv4(127, 0, 0, 1)}, hosts...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	jobs := make(chan net.IP)
	found := make(chan Found, 1)
	var wg sync.WaitGroup
	for range sweepWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := net.Dialer{Timeout: sweepDial}
			for ip := range jobs {
				addr := net.JoinHostPort(ip.String(), strconv.Itoa(kmapi.DefaultPort))
				conn, err := d.DialContext(ctx, "tcp", addr)
				if err != nil {
					continue
				}
				conn.Close()
				if f, err := Check(ctx, BaseURL(addr)); err == nil {
					select {
					case found <- f:
						cancel()
					default:
					}
				}
			}
		}()
	}
feed:
	for _, ip := range hosts {
		select {
		case jobs <- ip:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	select {
	case f := <-found:
		return []Found{f}, nil
	default:
		return nil, ErrNoQueueManager
	}
}
