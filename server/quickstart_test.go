package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/client"
)

// blockingSearch never finds anything and never ends by itself, and reports
// that it started.
func blockingSearch(started chan<- struct{}) func(context.Context) (string, []client.Device, error) {
	return func(ctx context.Context) (string, []client.Device, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
}

// runUntilReady starts Run and returns the address it serves on and the
// device address it reported. The test ends Run when it finishes.
func runUntilReady(t *testing.T, cfg Config) (listen, device string) {
	t.Helper()
	type ready struct{ listen, device string }
	readyC := make(chan ready, 1)
	cfg.Listen = "127.0.0.1:0"
	cfg.Logger = slog.New(slog.DiscardHandler)
	cfg.Ready = func(a net.Addr, device string) { readyC <- ready{a.String(), device} }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})

	select {
	case r := <-readyC:
		return r.listen, r.device
	case err := <-done:
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not serve")
	}
	return "", ""
}

// With a catalogue and a remembered address, Run serves before any search has
// finished, and the device session does the searching.
func TestRunServesBeforeTheSearchEnds(t *testing.T) {
	dir := t.TempDir()
	lastAddrFile := filepath.Join(dir, "device.addr")
	require.NoError(t, os.WriteFile(lastAddrFile, []byte("127.0.0.1:9\n"), 0o644))
	searching := make(chan struct{}, 1)

	_, device := runUntilReady(t, Config{
		SongDB:       songDB(t),
		FavDB:        filepath.Join(dir, "favorites.db"),
		LastAddrFile: lastAddrFile,
		Resolve:      blockingSearch(searching),
	})
	assert.Equal(t, "127.0.0.1:9", device)

	select {
	case <-searching:
	case <-time.After(10 * time.Second):
		t.Fatal("the device session did not search")
	}
}

// A first run, which has never found a device, still serves, and the Songs tab
// says what it is waiting for.
func TestRunServesWithoutACatalogue(t *testing.T) {
	dir := t.TempDir()
	searching := make(chan struct{}, 1)

	listen, device := runUntilReady(t, Config{
		SongDB:       filepath.Join(dir, "song.db"),
		FavDB:        filepath.Join(dir, "favorites.db"),
		LastAddrFile: filepath.Join(dir, "device.addr"),
		Resolve:      blockingSearch(searching),
	})
	assert.Empty(t, device)

	resp, err := http.Get("http://" + listen + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body := new(strings.Builder)
	_, err = io.Copy(body, resp.Body)
	require.NoError(t, err)
	assert.Contains(t, body.String(), `href="/songlist/import"`)

	select {
	case <-searching:
	case <-time.After(10 * time.Second):
		t.Fatal("the device session did not search")
	}
}

func TestStartAddr(t *testing.T) {
	dir := t.TempDir()
	lastAddrFile := filepath.Join(dir, "device.addr")
	require.NoError(t, os.WriteFile(lastAddrFile, []byte("10.0.0.9:35379\n"), 0o644))

	assert.Equal(t, "10.0.0.5:35379", startAddr(Config{Addr: "10.0.0.5", LastAddrFile: lastAddrFile}))
	assert.Equal(t, "10.0.0.9:35379", startAddr(Config{LastAddrFile: lastAddrFile}))
	assert.Empty(t, startAddr(Config{LastAddrFile: filepath.Join(dir, "none.addr")}))
}

func TestDeviceResolverKeepsTheCurrentAddress(t *testing.T) {
	lastAddrFile := filepath.Join(t.TempDir(), "device.addr")
	found := []client.Device{{Addr: "10.0.0.5:35379"}, {Addr: "10.0.0.9:35379"}}
	resolve := func(context.Context) (string, []client.Device, error) {
		return found[0].Addr, found, nil
	}
	fn := deviceResolver(Config{LastAddrFile: lastAddrFile}, resolve, quietLogger())

	addr, err := fn(context.Background(), "10.0.0.9:35379")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.9:35379", addr)

	addr, err = fn(context.Background(), "10.0.0.7:35379")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5:35379", addr)
	assert.Equal(t, "10.0.0.5:35379", readAddr(lastAddrFile))
}
