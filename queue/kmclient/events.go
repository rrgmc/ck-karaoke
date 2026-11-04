package kmclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
)

// Event is one frame of the event stream. Kind names it; the field that
// carries its payload is set and the others are nil.
type Event struct {
	Kind string `json:"event"`

	// "state": at once, then several times a second.
	State *wire.State `json:"state,omitempty"`
	// "queue_changed".
	Queue *wire.Queue `json:"queue,omitempty"`
	// "requests_changed".
	Requests *wire.Requests `json:"requests,omitempty"`
	// "singers_changed".
	Singers *wire.Singers `json:"singers,omitempty"`
	// "settings_changed".
	Settings *wire.Settings `json:"settings,omitempty"`
	// "song_started".
	NowPlaying *wire.NowPlaying `json:"now_playing,omitempty"`
	// "song_ended".
	Reason string `json:"reason,omitempty"`
	// "desync": this many frames were dropped, so re-read what matters.
	Skipped uint64 `json:"skipped,omitempty"`
}

// Silence is how long the stream may stay quiet before Follow gives it up.
// The server sends the state several times a second, and KM Remote gives up
// after the same five seconds.
const Silence = 5 * time.Second

// ErrSilent reports a stream that stopped sending.
var ErrSilent = errors.New("the event stream went silent")

// Follow reads the event stream and calls fn for each event, in order, until
// ctx ends or the stream fails. It always returns an error: ctx's, ErrSilent,
// or why the stream closed. Reconnecting is the caller's choice.
func (c *Client) Follow(ctx context.Context, fn func(Event)) error {
	url := "ws" + strings.TrimPrefix(c.base, "http") + "/api/v1/events"
	dialCtx, cancel := context.WithTimeout(ctx, Silence)
	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{HTTPClient: c.http})
	cancel()
	if err != nil {
		return fmt.Errorf("opening the event stream: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	for {
		readCtx, cancel := context.WithTimeout(ctx, Silence)
		_, b, err := conn.Read(readCtx)
		cancel()
		switch {
		case ctx.Err() != nil:
			conn.Close(websocket.StatusNormalClosure, "")
			return ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			return ErrSilent
		case err != nil:
			return fmt.Errorf("reading the event stream: %w", err)
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			return fmt.Errorf("reading an event: %w", err)
		}
		fn(e)
	}
}
