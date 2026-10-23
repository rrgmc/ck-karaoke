package webui

import (
	"strings"
	"testing"
)

func TestHubDeliversToSubscribers(t *testing.T) {
	h := newHub()
	updates, release := h.subscribe()
	defer release()

	h.publish(eventToast, toastHTML(toastGood, "Device connected"))

	select {
	case u := <-updates:
		if u.event != eventToast {
			t.Errorf("event = %q, want %q", u.event, eventToast)
		}
		if !strings.Contains(u.html, "Device connected") {
			t.Errorf("html = %q, missing the message", u.html)
		}
	default:
		t.Fatal("subscriber received nothing")
	}
}

// A page opened mid-song must show the current player straight away, but must
// not be greeted by notifications about things that already happened.
func TestHubReplaysPlayerButNotToasts(t *testing.T) {
	h := newHub()
	h.publish(eventPlayer, "<div>player</div>")
	h.publish(eventToast, toastHTML(toastWarn, "Device disconnected"))

	updates, release := h.subscribe()
	defer release()

	select {
	case u := <-updates:
		if u.event != eventPlayer {
			t.Errorf("replayed %q, want only %q", u.event, eventPlayer)
		}
	default:
		t.Fatal("player state was not replayed to a new subscriber")
	}

	select {
	case u := <-updates:
		t.Errorf("replayed a stale %q: %q", u.event, u.html)
	default: // correct: nothing else waiting
	}
}

// A slow subscriber must be dropped from, never block, the device reader.
func TestHubDropsForSlowSubscribers(t *testing.T) {
	h := newHub()
	_, release := h.subscribe()
	defer release()

	// Far more than the channel buffer; publish must not block.
	done := make(chan struct{})
	go func() {
		for range 100 {
			h.publish(eventToast, "x")
		}
		close(done)
	}()

	select {
	case <-done:
	default:
		<-done // let it finish; the point is that it does
	}
}

func TestToastEscapesContent(t *testing.T) {
	got := toastHTML(toastBad, `<script>alert("x")</script>`)
	if strings.Contains(got, "<script>") {
		t.Errorf("toast did not escape its message: %q", got)
	}
	if !strings.Contains(got, "toast-bad") {
		t.Errorf("toast lost its level: %q", got)
	}
}
