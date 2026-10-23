package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// MaxSweepHosts is the widest network Sweep will walk. Home networks are often
// a /22, so 1022 hosts has to work; a /16 would be 65534 pings and a /8 is not
// worth attempting at all.
const MaxSweepHosts = 4094 // a /20

// sweepBatch is how many pings go out before pausing. A thousand packets sent
// back to back is indistinguishable from a port scan to a consumer access point,
// and the ones it drops are the reason a sweep would otherwise miss the device.
const (
	sweepBatch = 64
	sweepPause = 20 * time.Millisecond
)

// Sweep finds devices by pinging every host on the local subnets in turn, rather
// than by broadcasting.
//
// This exists for iOS. Broadcast and multicast there need the
// com.apple.developer.networking.multicast entitlement, which Apple grants only
// after a manual request, while ordinary unicast to a LAN address needs nothing
// beyond the local network prompt every app gets. Discover is still the right
// choice everywhere else: one packet against a thousand.
//
// The ping and the reply parsing are the same as Discover's, so a device that
// answers one answers the other.
func Sweep(ctx context.Context, timeout time.Duration) ([]Device, error) {
	return sweep(ctx, timeout, false)
}

// sweep walks the local subnets. stopEarly returns as soon as anything answers,
// which is what resolving an address wants: waiting out the full timeout to see
// whether a second device exists is several seconds of staring at a splash
// screen, and a home network almost always has one unit on it.
func sweep(ctx context.Context, timeout time.Duration, stopEarly bool) ([]Device, error) {
	if timeout <= 0 {
		timeout = DefaultSweepTimeout
	}

	targets, err := sweepTargets()
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("no local IPv4 network to sweep")
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, fmt.Errorf("opening discovery socket: %w", err)
	}
	defer conn.Close()

	start := time.Now()
	deadline := start.Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	// Replies start arriving while the rest of the pings are still going out, so
	// the reader runs alongside the sender rather than after it.
	type result struct {
		devices []Device
		err     error
	}
	done := make(chan result, 1)
	stopped := make(chan struct{})
	go func() {
		devices, err := readPongs(conn, start, deadline, stopEarly)
		done <- result{devices, err}
		close(stopped)
	}()

	// stopped closes when the reader has what it needs, which lets the rest of a
	// thousand pings be abandoned rather than sent to no purpose.
	sendPings(ctx, conn, unconnectedPing(start), targets, stopped)

	res := <-done
	if res.err != nil {
		return res.devices, res.err
	}
	if len(res.devices) == 0 {
		return nil, ErrNoDevice
	}
	return res.devices, nil
}

// sendPings paces the pings out, giving up if the reader finishes or the context
// ends first.
func sendPings(ctx context.Context, conn *net.UDPConn, ping []byte, targets []*net.UDPAddr, stopped <-chan struct{}) {
	for i, dst := range targets {
		select {
		case <-stopped:
			return
		case <-ctx.Done():
			return
		default:
		}
		// A silent or unreachable host is the normal case here, not a failure.
		_, _ = conn.WriteToUDP(ping, dst)
		if (i+1)%sweepBatch == 0 {
			select {
			case <-stopped:
				return
			case <-ctx.Done():
				return
			case <-time.After(sweepPause):
			}
		}
	}
}

// DefaultSweepTimeout is how long Sweep listens. It is longer than
// DefaultDiscoverTimeout because the pings are paced rather than sent at once,
// and because the answer may come from the very last address tried.
const DefaultSweepTimeout = 6 * time.Second

// readPongs collects replies until the deadline passes, or until the first one
// arrives when stopEarly is set.
func readPongs(conn *net.UDPConn, start time.Time, deadline time.Time, stopEarly bool) ([]Device, error) {
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	var (
		devices []Device
		seen    = map[string]bool{}
		buf     = make([]byte, 2048)
	)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || isTimeout(err) {
				return devices, nil
			}
			return devices, err
		}
		dev, ok := parsePong(buf[:n])
		if !ok || seen[from.String()] {
			continue
		}
		seen[from.String()] = true
		dev.Addr = from.String()
		dev.RTT = time.Since(start)
		devices = append(devices, *dev)
		if stopEarly {
			return devices, nil
		}
	}
}

// sweepTargets lists every host address worth pinging on every up, non-loopback
// IPv4 interface, nearest first.
//
// Nearest first because the device is usually a handful of addresses from the
// phone on a home network, and the sweep can then answer in well under a second
// instead of walking a thousand addresses to get there.
func sweepTargets() ([]*net.UDPAddr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing interfaces to sweep: %w", err)
	}

	var (
		targets []*net.UDPAddr
		seen    = map[string]bool{}
	)
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
			for _, ip := range subnetHosts(ipnet) {
				s := ip.String()
				if seen[s] {
					continue
				}
				seen[s] = true
				targets = append(targets, &net.UDPAddr{IP: ip, Port: Port})
			}
		}
	}
	return targets, nil
}

// subnetHosts returns the pingable addresses of an IPv4 network, ordered
// outwards from the host's own address, or nil if the network is unusable or too
// large.
func subnetHosts(n *net.IPNet) []net.IP {
	self := n.IP.To4()
	mask := net.IP(n.Mask).To4()
	if self == nil || mask == nil {
		return nil
	}
	ones, bits := n.Mask.Size()
	// A /31 or /32 has no other hosts, and an all-zero mask — which some VPN
	// adapters report — would nominally cover the whole internet.
	if bits != 32 || ones >= 31 || ones == 0 {
		return nil
	}
	if hosts := 1<<(32-ones) - 2; hosts > MaxSweepHosts {
		return nil
	}

	first := ip4ToUint(self) & ip4ToUint(mask) // network address
	last := first | ^ip4ToUint(mask)           // broadcast address
	selfN := ip4ToUint(self)

	// Walk outwards from self, skipping the network and broadcast addresses and
	// the sweeping host itself.
	out := make([]net.IP, 0, last-first-1)
	for step := uint32(1); ; step++ {
		added := false
		if up := selfN + step; up < last {
			out = append(out, uint32ToIP4(up))
			added = true
		}
		if down := selfN - step; down > first && selfN >= step {
			out = append(out, uint32ToIP4(down))
			added = true
		}
		if !added {
			break
		}
	}
	return out
}

func ip4ToUint(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func uint32ToIP4(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// ResolveSweep is Resolve with the unicast sweep in place of the broadcast. It
// returns as soon as one device answers rather than waiting out the timeout.
func ResolveSweep(ctx context.Context, address string, timeout time.Duration) (string, []Device, error) {
	if address != "" {
		return WithDefaultPort(address), nil, nil
	}
	devices, err := sweep(ctx, timeout, true)
	if err != nil {
		return "", nil, err
	}
	return devices[0].Addr, devices, nil
}
