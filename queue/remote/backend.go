// Package remote drives CK Karaoke's pages from a CK Queue Manager, through
// its KaraokeMachine API. It is the backend of the CK Queue Remote build,
// which never opens a session with the karaoke unit itself.
package remote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
	"github.com/rrgmc/ck-karaoke/webui"
)

// Backend is a webui.Backend over a queue manager's API. Call Run to keep the
// event stream open.
type Backend struct {
	client *kmclient.Client
	log    *slog.Logger

	mu           sync.Mutex
	id, name     string // the queue manager's, once known
	tokenFile    string
	streaming    bool
	state        *wire.State
	queue        *wire.Queue
	err          error // why it is not online; nil when online
	onStatus     func(*webui.Status)
	onQueue      func(*webui.Queue)
	onConnection func(bool, error)
}

var _ webui.Backend = (*Backend)(nil)

// New returns a backend for the queue manager c talks to.
func New(c *kmclient.Client, logger *slog.Logger) *Backend {
	if logger == nil {
		logger = slog.Default()
	}
	return &Backend{client: c, log: logger, err: webui.ErrQueueManager}
}

// Client is the API client the backend uses.
func (b *Backend) Client() *kmclient.Client { return b.client }

// Run follows the event stream until ctx ends, reconnecting with a backoff.
func (b *Backend) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for ctx.Err() == nil {
		b.identify(ctx)
		// The queue is read once per connection: the stream only carries it
		// when it changes.
		if q, err := b.client.Queue(ctx); err == nil {
			b.setQueue(&q)
		}
		err := b.client.Follow(ctx, func(e kmclient.Event) {
			backoff = time.Second
			b.handle(ctx, e)
		})
		if ctx.Err() != nil {
			return
		}
		b.setStream(false, err)
		b.log.Warn("queue manager stream ended, retrying", "addr", b.client.Base(), "error", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (b *Backend) handle(ctx context.Context, e kmclient.Event) {
	switch e.Kind {
	case "state":
		if e.State != nil {
			b.setState(e.State)
		}
	case "queue_changed":
		if e.Queue != nil {
			b.setQueue(e.Queue)
		}
	case "desync":
		if q, err := b.client.Queue(ctx); err == nil {
			b.setQueue(&q)
		}
	}
}

func (b *Backend) setState(st *wire.State) {
	b.mu.Lock()
	b.streaming, b.state = true, st
	changed, online, reason := b.updateErrLocked()
	onStatus, onConnection := b.onStatus, b.onConnection
	status := b.statusLocked()
	b.mu.Unlock()
	if changed && onConnection != nil {
		onConnection(online, reason)
	}
	if onStatus != nil {
		onStatus(status)
	}
}

func (b *Backend) setQueue(q *wire.Queue) {
	b.mu.Lock()
	b.queue = q
	fn := b.onQueue
	out := queueOf(q)
	b.mu.Unlock()
	if fn != nil {
		fn(out)
	}
}

func (b *Backend) setStream(up bool, cause error) {
	b.mu.Lock()
	b.streaming = up
	if !up && cause != nil {
		b.log.Debug("queue manager stream down", "error", cause)
	}
	changed, online, reason := b.updateErrLocked()
	fn := b.onConnection
	b.mu.Unlock()
	if changed && fn != nil {
		fn(online, reason)
	}
}

// updateErrLocked recomputes why the backend is not online and reports
// whether that changed.
func (b *Backend) updateErrLocked() (changed, online bool, reason error) {
	var err error
	switch {
	case !b.streaming:
		err = webui.ErrQueueManager
	case b.state != nil && !b.state.PlayerOnline:
		err = webui.ErrAway
	}
	// Both are nil or one of the sentinels, so identity is the comparison.
	changed = err != b.err
	b.err = err
	return changed, err == nil, err
}

// Online reports whether the queue manager answers and has the unit.
func (b *Backend) Online() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err == nil
}

// Addr is the queue manager's address.
func (b *Backend) Addr() string { return b.client.Base() }

// LastError is webui.ErrQueueManager, webui.ErrAway or nil.
func (b *Backend) LastError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Status is the last state the queue manager sent.
func (b *Backend) Status() *webui.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.statusLocked()
}

func (b *Backend) statusLocked() *webui.Status {
	if b.state == nil {
		return nil
	}
	st := &webui.Status{Position: time.Duration(b.state.PositionMS) * time.Millisecond}
	if np := b.state.NowPlaying; np != nil {
		st.Title = np.Title
		if np.Origin.Number != nil {
			st.SongID = strconv.FormatUint(uint64(*np.Origin.Number), 10)
		}
	}
	if b.queue != nil && len(b.queue.Entries) > 0 {
		st.Next = b.queue.Entries[0].Title
	}
	return st
}

// Queue is the last queue the queue manager sent.
func (b *Backend) Queue() *webui.Queue {
	b.mu.Lock()
	defer b.mu.Unlock()
	return queueOf(b.queue)
}

func queueOf(q *wire.Queue) *webui.Queue {
	if q == nil {
		return nil
	}
	out := &webui.Queue{Total: q.Len, Entries: make([]webui.QueueEntry, len(q.Entries))}
	for i, e := range q.Entries {
		out.Entries[i] = webui.QueueEntry{SongID: strconv.FormatUint(uint64(e.Number), 10)}
		if e.Singer != nil {
			out.Entries[i].By = *e.Singer
		}
	}
	return out
}

// RefreshQueue reads the queue and publishes it.
func (b *Backend) RefreshQueue(ctx context.Context) error {
	q, err := b.client.Queue(ctx)
	if err != nil {
		return wrap(err)
	}
	b.setQueue(&q)
	return nil
}

// Control runs a transport action. The mixer, key and tempo are the unit's
// own and are not offered here.
func (b *Backend) Control(ctx context.Context, action string, _, _ int) error {
	var verb string
	switch action {
	case "pause":
		// The unit's button toggles; the API has a verb for each way.
		verb = "pause"
		b.mu.Lock()
		if b.state != nil && b.state.Transport != "playing" {
			verb = "play"
		}
		b.mu.Unlock()
	case "stop":
		verb = "stop"
	case "next", "skip":
		verb = "skip"
	case "repeat":
		verb = "restart"
	default:
		return fmt.Errorf("%w: %s", webui.ErrUnsupported, action)
	}
	st, err := b.client.Transport(ctx, verb)
	if err != nil {
		return wrap(err)
	}
	b.setState(&st)
	return nil
}

// SongAction queues a song, or removes it from the queue.
func (b *Backend) SongAction(ctx context.Context, action, songID string) error {
	song, err := strconv.Atoi(songID)
	if err != nil {
		return fmt.Errorf("bad song id %q", songID)
	}
	switch action {
	case "reserve":
		_, err = b.client.AddToQueue(ctx, song)
	case "remove":
		entry, ok := b.entryOf(song)
		if !ok {
			return fmt.Errorf("song %d is not in the queue", song)
		}
		var q wire.Queue
		if q, err = b.client.RemoveEntry(ctx, entry); err == nil {
			b.setQueue(&q)
		}
	default:
		return fmt.Errorf("%w: %s", webui.ErrUnsupported, action)
	}
	return wrap(err)
}

// entryOf finds the first queue entry for a song.
func (b *Backend) entryOf(song int) (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.queue == nil {
		return 0, false
	}
	for _, e := range b.queue.Entries {
		if int(e.Number) == song {
			return e.ID, true
		}
	}
	return 0, false
}

// OnStatus registers the status callback.
func (b *Backend) OnStatus(fn func(*webui.Status)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onStatus = fn
}

// OnQueue registers the queue callback.
func (b *Backend) OnQueue(fn func(*webui.Queue)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onQueue = fn
}

// OnConnection registers the connection callback.
func (b *Backend) OnConnection(fn func(online bool, reason error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onConnection = fn
}

// wrap turns the API's errors into the ones the pages word.
func wrap(err error) error {
	var e *kmclient.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, kmclient.ErrUnauthorized):
		return fmt.Errorf("%w: %v", webui.ErrNeedsCode, err)
	case errors.As(err, &e) && e.Code == "player_offline":
		return fmt.Errorf("%w: %v", webui.ErrAway, err)
	case errors.As(err, &e):
		return err
	default:
		return fmt.Errorf("%w: %v", webui.ErrQueueManager, err)
	}
}
