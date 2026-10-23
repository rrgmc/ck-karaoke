package webui

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Server-sent event names. The player fragment replaces the Now page's controls;
// toasts are appended wherever the notification area lives.
const (
	eventPlayer = "player"
	eventQueue  = "queue"
	// eventQueueCount carries just the tab badge. It rides separately from
	// eventQueue because the badge is on every page while the list itself is
	// only on one.
	eventQueueCount = "queuecount"
	eventBanner     = "banner"
	// eventConn carries the session indicator in the tab bar. Separate from
	// eventBanner for the same reason: one event cannot be swapped into two places
	// in the document, and the banner is a block at the top while this is a dot at
	// the bottom.
	eventConn  = "conn"
	eventToast = "toast"
)

// update is one thing to send to the browsers.
type update struct {
	event string
	html  string
}

// hub fans device updates out to connected browsers.
//
// Each subscriber gets a small buffered channel and updates are dropped for a
// subscriber that falls behind, never queued. A phone that locks its screen
// mid-song must not be able to stall the goroutine reading from the device.
type hub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan update
	// latest holds the most recent state-bearing event per name, replayed to a new
	// subscriber so a freshly opened page shows current state without waiting for
	// the device's next push. Toasts are excluded: a notification that already
	// happened is noise on arrival.
	latest map[string]string
}

func newHub() *hub { return &hub{subs: map[int]chan update{}, latest: map[string]string{}} }

func (h *hub) publish(event, html string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if event != eventToast {
		h.latest[event] = html
	}
	for _, ch := range h.subs {
		select {
		case ch <- update{event, html}:
		default: // subscriber is behind; it will get the next one
		}
	}
}

// count is how many streams are currently subscribed.
func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// subscribe returns a channel of updates and a function to release it.
func (h *hub) subscribe() (<-chan update, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.next
	h.next++
	ch := make(chan update, 8)
	for _, event := range []string{eventPlayer, eventQueue, eventQueueCount, eventBanner, eventConn} {
		if html := h.latest[event]; html != "" {
			ch <- update{event, html}
		}
	}
	h.subs[id] = ch

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
}

// serveSSE streams updates as Server-Sent Events. htmx's SSE extension swaps
// each event into whichever element declares sse-swap for that name.
func (s *Server) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this, a reverse proxy may buffer the stream into uselessness.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	updates, release := s.hub.subscribe()
	defer release()

	// Logged because logRequests deliberately skips this path, and a stream that
	// has quietly died is indistinguishable from a device with nothing to say —
	// the page simply stops updating and goes on showing whatever it last heard.
	s.logger.Debug("event stream opened", "client", r.RemoteAddr, "streams", s.hub.count())
	defer func() {
		s.logger.Debug("event stream closed", "client", r.RemoteAddr, "streams", s.hub.count()-1)
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.stopping:
			// The server is going away; let the connection go rather than holding
			// shutdown open until its deadline.
			return
		case u, ok := <-updates:
			if !ok {
				return
			}
			// SSE data cannot contain bare newlines, so each line is prefixed.
			var b strings.Builder
			fmt.Fprintf(&b, "event: %s\n", u.event)
			for _, line := range strings.Split(u.html, "\n") {
				fmt.Fprintf(&b, "data: %s\n", strings.TrimSuffix(line, "\r"))
			}
			b.WriteString("\n")

			// Checked, so a stream that can no longer be written to is given up
			// rather than published into forever. It was going unnoticed: a phone
			// that suspends the app leaves the connection in a state where nothing
			// on either side reports a problem.
			if _, err := io.WriteString(w, b.String()); err != nil {
				s.logger.Debug("event stream write failed", "client", r.RemoteAddr, "error", err)
				return
			}
			flusher.Flush()
		}
	}
}
