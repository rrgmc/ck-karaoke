package remote_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/kmapitest"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
	"github.com/rrgmc/ck-karaoke/queue/remote"
	"github.com/rrgmc/ck-karaoke/webui"
)

// runBackend runs b until the test ends.
func runBackend(t *testing.T, b *remote.Backend) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// With no address, the backend keeps searching and reports that nothing has
// been found, rather than that the queue manager does not answer.
func TestTheBackendSearchesUntilItFindsOne(t *testing.T) {
	var calls atomic.Int32
	b := remote.New(kmclient.New("", nil), slog.New(slog.DiscardHandler))
	b.SetResolver(func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", errors.New("nothing answered")
	})
	runBackend(t, b)

	require.Eventually(t, func() bool { return calls.Load() >= 1 }, wait, tick)
	assert.ErrorIs(t, b.LastError(), webui.ErrNoQueueManager)
	assert.Empty(t, b.Addr())
}

// A search that finds the queue manager moves the backend there, and the
// first event stream runs the OnReached hook.
func TestTheBackendMovesToTheAddressFound(t *testing.T) {
	qm := kmapitest.New(t, kmapi.Queue, "")
	b := remote.New(kmclient.New("", nil), slog.New(slog.DiscardHandler))
	b.SetResolver(func(context.Context, string) (string, error) { return qm.URL, nil })
	reached := make(chan struct{}, 1)
	b.OnReached(func() {
		select {
		case reached <- struct{}{}:
		default:
		}
	})
	runBackend(t, b)

	select {
	case <-reached:
	case <-time.After(wait):
		t.Fatal("the queue manager was never reached")
	}
	assert.Equal(t, qm.URL, b.Addr())
}
