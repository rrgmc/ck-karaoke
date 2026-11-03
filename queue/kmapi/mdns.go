package kmapi

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// ServiceType is KaraokeMachine's DNS-SD service type.
const ServiceType = "_karaokemachine._tcp"

// maxInstanceBytes is the longest DNS-SD instance label.
const maxInstanceBytes = 63

// Announcement is what mDNS tells the network about this server.
type Announcement struct {
	ID   string
	Name string
	Port int
	// Addrs are the IPv4 LAN addresses, best first.
	Addrs []string
}

// TXT returns the TXT records: v, id, name, api and url, the URL being the
// best address.
func (a Announcement) TXT() []string {
	txt := []string{"v=1", "id=" + a.ID, "name=" + a.Name, "api=/api/v1"}
	if len(a.Addrs) > 0 {
		txt = append(txt, fmt.Sprintf("url=http://%s:%d", a.Addrs[0], a.Port))
	}
	return txt
}

// Instance is the DNS-SD instance name: the name with dots and control
// characters replaced, or "karaokemachine" when nothing is left.
func (a Announcement) Instance() string {
	n := strings.Map(func(r rune) rune {
		if r == '.' || r < 0x20 || r == 0x7f {
			return '-'
		}
		return r
	}, strings.TrimSpace(a.Name))
	for len(n) > maxInstanceBytes {
		_, size := lastRune(n)
		n = n[:len(n)-size]
	}
	n = strings.TrimSpace(n)
	if n == "" {
		return App
	}
	return n
}

func lastRune(s string) (rune, int) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i]&0xc0 != 0x80 {
			return rune(s[i]), len(s) - i
		}
	}
	return 0, 1
}

func (a Announcement) equal(b Announcement) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Port == b.Port && slices.Equal(a.Addrs, b.Addrs)
}

// Announce advertises the server over mDNS until ctx is cancelled, announcing
// again whenever the name or the addresses change. current is read every five
// seconds.
func Announce(ctx context.Context, current func() Announcement, logf func(msg string, args ...any)) {
	var (
		srv  *zeroconf.Server
		last Announcement
	)
	stop := func() {
		if srv != nil {
			srv.Shutdown()
			srv = nil
		}
	}
	defer stop()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		a := current()
		if srv == nil || !a.equal(last) {
			stop()
			if len(a.Addrs) > 0 {
				s, err := zeroconf.RegisterProxy(a.Instance(), ServiceType, "local.", a.Port,
					a.ID, a.Addrs, a.TXT(), nil)
				if err != nil {
					logf("cannot announce over mDNS", "error", err)
				} else {
					srv = s
					logf("announced over mDNS", "name", a.Instance(), "addrs", a.Addrs)
				}
			}
			last = a
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Announcement returns what this server announces now.
func (s *Server) Announcement() Announcement {
	a := Announcement{ID: s.opts.ID, Name: s.config().Name, Port: s.opts.Port}
	for _, ip := range s.opts.Addrs() {
		a.Addrs = append(a.Addrs, ip.String())
	}
	return a
}
