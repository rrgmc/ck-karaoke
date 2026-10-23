package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/sandertv/go-raknet"
)

// magic is RakNet's offline message identifier, present in every unconnected
// message.
var magic = [16]byte{0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe, 0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78}

const (
	idUnconnectedPing byte = 0x01
	idUnconnectedPong byte = 0x1c
)

// Device is a karaoke device that answered an unconnected ping.
type Device struct {
	// Addr is the address that replied.
	Addr string
	// ServerGUID is the device's RakNet GUID.
	ServerGUID int64
	// MOTD is the raw offline ping response: base64 over DES-encrypted text.
	MOTD []byte
	// Info is the decoded contents of MOTD — product, model and catalogue id.
	// Zero if the payload could not be decoded.
	Info protocol.DeviceInfo
	// RTT is how long the reply took to arrive.
	RTT time.Duration
}

func (d Device) String() string {
	desc := d.Info.String()
	if desc == "" {
		desc = fmt.Sprintf("%d bytes of undecodable info", len(d.MOTD))
	}
	return fmt.Sprintf("%s  %s  guid=%#016x rtt=%s",
		d.Addr, desc, d.ServerGUID, d.RTT.Round(time.Millisecond))
}

// WithDefaultPort appends the device port to address if it has none.
func WithDefaultPort(address string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(address, strconv.Itoa(Port))
}

// Ping sends an unconnected ping to a single device and returns what it reports.
// It does not establish a connection, so it works regardless of whether another
// client is already connected.
func Ping(ctx context.Context, address string) (*Device, error) {
	start := time.Now()
	dialer := raknet.Dialer{}
	pong, addr, err := dialer.PingContext(ctx, WithDefaultPort(address))
	if err != nil {
		return nil, fmt.Errorf("pinging %s: %w", address, err)
	}
	dev := &Device{
		Addr:       addr.String(),
		ServerGUID: pong.ServerGUID,
		MOTD:       pong.Data,
		RTT:        time.Since(start),
	}
	dev.Info, _ = protocol.DecodeDeviceInfo(dev.MOTD)
	return dev, nil
}

// Discover broadcasts an unconnected ping and collects every device that answers
// within timeout.
//
// When broadcastAddr is empty the ping goes to the broadcast address of every
// suitable local interface as well as 255.255.255.255. Relying on a single
// address is fragile: a /22 network broadcasts to x.x.71.255 rather than the
// x.x.68.255 a /24 assumption would produce, and some networks drop the global
// broadcast entirely.
func Discover(ctx context.Context, broadcastAddr string, timeout time.Duration) ([]Device, error) {
	var targets []string
	if broadcastAddr != "" {
		targets = []string{broadcastAddr}
	} else {
		targets = broadcastTargets()
	}

	conn, err := listenBroadcast()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	start := time.Now()
	ping := unconnectedPing(start)
	var sent int
	var lastErr error
	for _, t := range targets {
		dst, err := net.ResolveUDPAddr("udp4", WithDefaultPort(t))
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := conn.WriteToUDP(ping, dst); err != nil {
			lastErr = fmt.Errorf("pinging %s: %w", t, err)
			continue
		}
		sent++
	}
	if sent == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("no broadcast address available")
	}

	deadline := start.Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
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
	}
}

// listenBroadcast opens an ephemeral UDP socket with SO_BROADCAST enabled. Go
// does not set that option by default, and without it broadcast sends fail or
// are silently dropped.
func listenBroadcast() (*net.UDPConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = setBroadcast(fd)
			}); err != nil {
				return err
			}
			return serr
		},
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("opening discovery socket: %w", err)
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		pc.Close()
		return nil, fmt.Errorf("expected *net.UDPConn, got %T", pc)
	}
	return conn, nil
}

// broadcastTargets returns the directed broadcast address of every up, non-
// loopback IPv4 interface, plus the global broadcast address as a fallback.
func broadcastTargets() []string {
	targets := []string{"255.255.255.255"}
	seen := map[string]bool{"255.255.255.255": true}

	ifaces, err := net.Interfaces()
	if err != nil {
		return targets
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagBroadcast == 0 {
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
			b := directedBroadcast(ipnet)
			if b == nil || seen[b.String()] {
				continue
			}
			seen[b.String()] = true
			targets = append(targets, b.String())
		}
	}
	return targets
}

// directedBroadcast computes the broadcast address for an IPv4 network, or nil
// if the address is not usable IPv4. A /32 (or an all-zero mask, as some VPN
// adapters report) has no meaningful broadcast address.
func directedBroadcast(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	mask := net.IP(n.Mask).To4()
	if ip == nil || mask == nil {
		return nil
	}
	ones, bits := n.Mask.Size()
	if bits != 32 || ones == 0 || ones == 32 {
		return nil
	}
	out := make(net.IP, net.IPv4len)
	for i := range out {
		out[i] = ip[i] | ^mask[i]
	}
	return out
}

// unconnectedPing builds an ID_UNCONNECTED_PING: id, ping time, magic, client GUID.
func unconnectedPing(now time.Time) []byte {
	b := make([]byte, 0, 33)
	b = append(b, idUnconnectedPing)
	b = binary.BigEndian.AppendUint64(b, uint64(now.UnixMilli()))
	b = append(b, magic[:]...)
	b = binary.BigEndian.AppendUint64(b, uint64(now.UnixNano()))
	return b
}

// parsePong decodes an ID_UNCONNECTED_PONG. The MOTD runs to the end of the
// packet with no length prefix, which is what stock RakNet sends and what these
// devices do — see the matching fix in go-raknet's UnconnectedPong.
func parsePong(b []byte) (*Device, bool) {
	// id(1) + time(8) + guid(8) + magic(16)
	const headerLen = 33
	if len(b) < headerLen || b[0] != idUnconnectedPong {
		return nil, false
	}
	if [16]byte(b[17:33]) != magic {
		return nil, false
	}
	dev := &Device{
		ServerGUID: int64(binary.BigEndian.Uint64(b[9:17])),
		MOTD:       append([]byte(nil), b[headerLen:]...),
	}
	dev.Info, _ = protocol.DecodeDeviceInfo(dev.MOTD)
	return dev, true
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// ErrNoDevice reports that discovery went unanswered. Deliberately not phrased
// in terms of a broadcast: Sweep reports it too, and gets there by unicast.
var ErrNoDevice = errors.New("no karaoke device answered discovery")

// DefaultDiscoverTimeout is how long Resolve listens for replies. A unit
// answers in a few milliseconds; the margin is for a busy wireless network.
const DefaultDiscoverTimeout = 3 * time.Second

// Resolve returns the address to dial, discovering it when address is empty.
//
// An explicit address is taken as given and *not* pinged first: the device may
// legitimately be off or rebooting at startup, and callers that reconnect should
// get the chance to, rather than failing here. Discovery is the opposite case —
// there is no address to fall back on, so a silent network is a hard error.
//
// The devices found are returned as well, so a caller can report which one it
// picked when a network has more than one.
func Resolve(ctx context.Context, address string, timeout time.Duration) (string, []Device, error) {
	if address != "" {
		return WithDefaultPort(address), nil, nil
	}
	if timeout <= 0 {
		timeout = DefaultDiscoverTimeout
	}
	devices, err := Discover(ctx, "", timeout)
	if err != nil {
		return "", nil, fmt.Errorf("discovering a device: %w", err)
	}
	if len(devices) == 0 {
		return "", nil, ErrNoDevice
	}
	return devices[0].Addr, devices, nil
}
