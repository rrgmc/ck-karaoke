package server

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/client"
)

// With a catalogue and a remembered address, Run serves before any search has
// finished, and the device session does the searching.
func TestRunServesBeforeTheSearchEnds(t *testing.T) {
	dir := t.TempDir()
	lastAddrFile := filepath.Join(dir, "device.addr")
	require.NoError(t, os.WriteFile(lastAddrFile, []byte("127.0.0.1:9\n"), 0o644))

	searching := make(chan struct{}, 1)
	ready := make(chan string, 1)
	cfg := Config{
		Listen:       "127.0.0.1:0",
		SongDB:       songDB(t),
		FavDB:        filepath.Join(dir, "favorites.db"),
		LastAddrFile: lastAddrFile,
		Logger:       slog.New(slog.DiscardHandler),
		// A search that never finds anything and never ends by itself.
		Resolve: func(ctx context.Context) (string, []client.Device, error) {
			select {
			case searching <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return "", nil, ctx.Err()
		},
		Ready: func(_ net.Addr, device string) { ready <- device },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	select {
	case device := <-ready:
		assert.Equal(t, "127.0.0.1:9", device)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not serve while the search was running")
	}
	select {
	case <-searching:
	case <-time.After(10 * time.Second):
		t.Fatal("the device session did not search")
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestQuickStartNeedsEverything(t *testing.T) {
	dir := t.TempDir()
	lastAddrFile := filepath.Join(dir, "device.addr")
	require.NoError(t, os.WriteFile(lastAddrFile, []byte("10.0.0.9:35379\n"), 0o644))
	songs := songDB(t)
	logger := quietLogger()

	db, addr := quickStart(Config{LastAddrFile: lastAddrFile, SongDB: songs}, logger)
	require.NotNil(t, db)
	db.Close()
	assert.Equal(t, "10.0.0.9:35379", addr)

	// An address the caller gave is used as it is, with no search to skip.
	db, _ = quickStart(Config{Addr: "10.0.0.5", LastAddrFile: lastAddrFile, SongDB: songs}, logger)
	assert.Nil(t, db)

	// Nothing remembered.
	db, _ = quickStart(Config{LastAddrFile: filepath.Join(dir, "none.addr"), SongDB: songs}, logger)
	assert.Nil(t, db)

	// A catalogue that does not open has to be downloaded, which needs the device.
	broken := filepath.Join(dir, "broken.db")
	require.NoError(t, os.WriteFile(broken, []byte("not really sqlite"), 0o644))
	db, _ = quickStart(Config{LastAddrFile: lastAddrFile, SongDB: broken}, logger)
	assert.Nil(t, db)
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
