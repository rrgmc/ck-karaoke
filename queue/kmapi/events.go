package kmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// event is one WebSocket frame: an object tagged by its "event" field, with
// the event's fields beside the tag.
type event map[string]any

// subscriberBuffer is how many frames a slow subscriber may fall behind before
// frames are dropped and it is sent a desync event instead.
const subscriberBuffer = 256

type subscriber struct {
	frames  chan []byte
	mu      sync.Mutex
	skipped uint64
}

// hub fans events out to every open stream.
type hub struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed chan struct{}
	once   sync.Once
}

func newHub() *hub {
	return &hub{subs: map[*subscriber]struct{}{}, closed: make(chan struct{})}
}

func (h *hub) publish(e event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.frames <- b:
		default:
			s.mu.Lock()
			s.skipped++
			s.mu.Unlock()
		}
	}
}

func (h *hub) subscribe() (*subscriber, func()) {
	s := &subscriber{frames: make(chan []byte, subscriberBuffer)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s, func() {
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
	}
}

func (h *hub) close() { h.once.Do(func() { close(h.closed) }) }

// handleEvents serves GET /api/v1/events. The stream carries no credentials,
// and the client sends nothing on it; the state event goes out at once and
// then every StateInterval.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	// Reads only to notice a close; anything the client sends is ignored.
	ctx := conn.CloseRead(r.Context())

	sub, unsubscribe := s.hub.subscribe()
	defer unsubscribe()

	write := func(b []byte) bool {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, b) == nil
	}
	first, _ := json.Marshal(event{"event": "state", "state": s.stateDTO(ctx)})
	if !write(first) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.hub.closed:
			conn.Close(websocket.StatusGoingAway, "the server is stopping")
			return
		case b := <-sub.frames:
			if !write(b) {
				return
			}
			sub.mu.Lock()
			skipped := sub.skipped
			sub.skipped = 0
			sub.mu.Unlock()
			if skipped > 0 {
				d, _ := json.Marshal(event{"event": "desync", "skipped": skipped})
				if !write(d) {
					return
				}
			}
		}
	}
}
