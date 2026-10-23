package webui

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/rrgmc/ck-karaoke/i18n"
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

// renderFunc produces an update's markup in one language. An empty result means
// there is nothing to send in that language.
type renderFunc func(i18n.Lang) string

// subscriber is one open event stream and the language its page is in.
type subscriber struct {
	lang i18n.Lang
	ch   chan update
}

// hub fans device updates out to connected browsers.
//
// Each subscriber gets a small buffered channel and updates are dropped for a
// subscriber that falls behind, never queued. A phone that locks its screen
// mid-song must not be able to stall the goroutine reading from the device.
//
// Pages in different languages can be open at once, so an update is published
// as a function rather than as markup, and rendered once for each language that
// has a subscriber.
type hub struct {
	mu   sync.Mutex
	next int
	subs map[int]subscriber
	// latest holds the most recent state-bearing event per name, replayed to a new
	// subscriber so a freshly opened page shows current state without waiting for
	// the device's next push. Toasts are excluded: a notification that already
	// happened is noise on arrival.
	latest map[string]renderFunc
}

func newHub() *hub { return &hub{subs: map[int]subscriber{}, latest: map[string]renderFunc{}} }

func (h *hub) publish(event string, render renderFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if event != eventToast {
		h.latest[event] = render
	}
	rendered := map[i18n.Lang]string{}
	for _, sub := range h.subs {
		html, ok := rendered[sub.lang]
		if !ok {
			html = render(sub.lang)
			rendered[sub.lang] = html
		}
		if html == "" {
			continue
		}
		select {
		case sub.ch <- update{event, html}:
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

// subscribe returns a channel of updates in a language and a function to
// release it.
func (h *hub) subscribe(lang i18n.Lang) (<-chan update, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.next
	h.next++
	ch := make(chan update, 8)
	for _, event := range []string{eventPlayer, eventQueue, eventQueueCount, eventBanner, eventConn} {
		if render := h.latest[event]; render != nil {
			if html := render(lang); html != "" {
				ch <- update{event, html}
			}
		}
	}
	h.subs[id] = subscriber{lang: lang, ch: ch}

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if sub, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(sub.ch)
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

	updates, release := s.hub.subscribe(langOf(r))
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
