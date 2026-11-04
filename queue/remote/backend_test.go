package remote_test

import (
	"context"
	"log/slog"
	"sync"
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

const wait, tick = 5 * time.Second, 10 * time.Millisecond

// recorder keeps what the backend pushed.
type recorder struct {
	mu          sync.Mutex
	queue       *webui.Queue
	connections []error // nil for online
}

func (r *recorder) lastQueue() *webui.Queue {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queue
}

func (r *recorder) lastConnection() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.connections) == 0 {
		return 0, nil
	}
	return len(r.connections), r.connections[len(r.connections)-1]
}

// start runs a backend against a queue manager until the test ends.
func start(t *testing.T, url string) (*remote.Backend, *recorder) {
	t.Helper()
	b := remote.New(kmclient.New(url, nil), slog.New(slog.DiscardHandler))
	rec := &recorder{}
	b.OnQueue(func(q *webui.Queue) { rec.mu.Lock(); rec.queue = q; rec.mu.Unlock() })
	b.OnConnection(func(online bool, reason error) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if online {
			reason = nil
		}
		rec.connections = append(rec.connections, reason)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return b, rec
}

func TestTheBackendComesOnlineAndQueues(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	b, rec := start(t, srv.URL)
	ctx := context.Background()

	require.Eventually(t, b.Online, wait, tick)
	n, reason := rec.lastConnection()
	assert.NoError(t, reason)
	assert.Equal(t, 1, n)
	assert.Equal(t, srv.URL, b.Addr())

	err := b.SongAction(ctx, "reserve", "1001")
	assert.ErrorIs(t, err, webui.ErrNeedsCode, "the room level cannot add a song with no singer")

	_, err = b.Client().Login(ctx, kmapitest.QueueCode)
	require.NoError(t, err)
	require.NoError(t, b.SongAction(ctx, "reserve", "1001"))
	require.NoError(t, b.SongAction(ctx, "reserve", "6541"))

	require.Eventually(t, func() bool { q := rec.lastQueue(); return q != nil && len(q.Entries) == 2 }, wait, tick)
	q := b.Queue()
	require.NotNil(t, q)
	assert.Equal(t, 2, q.Total)
	assert.Equal(t, "1001", q.Entries[0].SongID)
	require.Eventually(t, func() bool { st := b.Status(); return st != nil && st.Next == "PRESENTE DE DEUS" }, wait, tick)

	_, err = b.Client().Login(ctx, kmapitest.ControlCode)
	require.NoError(t, err)
	require.NoError(t, b.SongAction(ctx, "remove", "1001"))
	q = b.Queue()
	require.Len(t, q.Entries, 1)
	assert.Equal(t, "6541", q.Entries[0].SongID)

	assert.Error(t, b.SongAction(ctx, "remove", "1001"), "no longer in the queue")
	assert.ErrorIs(t, b.SongAction(ctx, "play", "1001"), webui.ErrUnsupported)
}

func TestTheTransportMapsOntoTheAPI(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Control, "")
	b, _ := start(t, srv.URL)
	ctx := context.Background()
	require.Eventually(t, b.Online, wait, tick)

	require.NoError(t, b.Control(ctx, "stop", 0, 0))
	_, err := b.Client().AddToQueue(ctx, 1001)
	require.NoError(t, err)
	// Nothing is playing, so the unit's pause button means play.
	require.NoError(t, b.Control(ctx, "pause", 0, 0))
	assert.Equal(t, []string{"stop", "play"}, srv.Player.Calls())

	assert.ErrorIs(t, b.Control(ctx, "key", 1, 0), webui.ErrUnsupported)
	assert.ErrorIs(t, b.Control(ctx, "volume", 50, 0), webui.ErrUnsupported)
}

func TestAnAbsentUnitTakesTheBackendOffline(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Control, "")
	b, rec := start(t, srv.URL)
	require.Eventually(t, b.Online, wait, tick)

	srv.Player.SetConnected(false)
	require.Eventually(t, func() bool { return !b.Online() }, wait, tick)
	assert.ErrorIs(t, b.LastError(), webui.ErrAway)
	_, reason := rec.lastConnection()
	assert.ErrorIs(t, reason, webui.ErrAway)

	assert.ErrorIs(t, b.Control(context.Background(), "stop", 0, 0), webui.ErrAway)

	srv.Player.SetConnected(true)
	require.Eventually(t, b.Online, wait, tick)
}

func TestAQueueManagerThatDoesNotAnswer(t *testing.T) {
	b, _ := start(t, "http://127.0.0.1:1")

	assert.False(t, b.Online())
	assert.ErrorIs(t, b.LastError(), webui.ErrQueueManager)
	assert.Nil(t, b.Status())
	assert.Nil(t, b.Queue())
	assert.ErrorIs(t, b.RefreshQueue(context.Background()), webui.ErrQueueManager)
}
