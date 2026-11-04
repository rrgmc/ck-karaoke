package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
)

// ErrNoQueueManager reports a search that found no CK Queue Manager.
var ErrNoQueueManager = errors.New("no CK Queue Manager found")

// Found is a CK Queue Manager that answered.
type Found struct {
	// URL is its API's base address, such as http://192.168.1.10:8177.
	URL     string
	Name    string
	Catalog wire.CKCatalog
}

// BaseURL turns an address as a person types it ("192.168.1.10",
// "host:8177", "http://host:8177/") into the API's base address.
func BaseURL(addr string) string {
	addr = strings.TrimRight(strings.TrimSpace(addr), "/")
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	scheme, host, _ := strings.Cut(addr, "://")
	host, _, _ = strings.Cut(host, "/")
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(kmapi.DefaultPort))
	}
	return scheme + "://" + host
}

// Check asks the server at base whether it is a CK Queue Manager.
func Check(ctx context.Context, base string) (Found, error) {
	c := kmclient.New(base, &http.Client{Timeout: 5 * time.Second})
	cat, err := c.CKCatalog(ctx)
	if err != nil {
		return Found{}, err
	}
	f := Found{URL: base, Catalog: cat}
	if d, err := c.Discover(ctx); err == nil {
		f.Name = d.Name
	}
	return f, nil
}

// Browse looks for CK Queue Managers on the local network by mDNS for the
// given time, and returns those that answer Check, in the order found.
func Browse(ctx context.Context, timeout time.Duration) ([]Found, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("mDNS: %w", err)
	}
	entries := make(chan *zeroconf.ServiceEntry, 16)
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := resolver.Browse(bctx, kmapi.ServiceType, "local.", entries); err != nil {
		return nil, fmt.Errorf("mDNS: %w", err)
	}

	var out []Found
	seen := map[string]bool{}
	for e := range entries {
		for _, base := range candidates(e) {
			if seen[base] {
				continue
			}
			seen[base] = true
			if f, err := Check(ctx, base); err == nil {
				out = append(out, f)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, ErrNoQueueManager
	}
	return out, nil
}

// candidates lists the addresses an announcement offers, its url record first.
func candidates(e *zeroconf.ServiceEntry) []string {
	var out []string
	for _, t := range e.Text {
		if u, ok := strings.CutPrefix(t, "url="); ok {
			out = append(out, BaseURL(u))
		}
	}
	for _, ip := range e.AddrIPv4 {
		out = append(out, BaseURL(net.JoinHostPort(ip.String(), strconv.Itoa(e.Port))))
	}
	return out
}

// SyncCatalogue makes path hold the queue manager's song database. It
// downloads nothing when the local file has the same hash, and replaces the
// file only after the new one passes catalog.Verify.
func SyncCatalogue(ctx context.Context, c *kmclient.Client, path string) (downloaded bool, err error) {
	local, _ := fileHash(path)
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	changed, err := c.DownloadSongDB(ctx, local, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || !changed {
		return false, err
	}
	if err := catalog.Verify(tmp.Name()); err != nil {
		return false, fmt.Errorf("the downloaded song database is damaged: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
