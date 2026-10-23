package webui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/i18n"
)

// fixed is an update that reads the same in every language.
func fixed(html string) renderFunc {
	return func(i18n.Lang) string { return html }
}

func TestHubDeliversToSubscribers(t *testing.T) {
	h := newHub()
	updates, release := h.subscribe(i18n.English)
	defer release()

	h.publish(eventToast, fixed(toastHTML(toastGood, "Device connected")))

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
	h.publish(eventPlayer, fixed("<div>player</div>"))
	h.publish(eventToast, fixed(toastHTML(toastWarn, "Device disconnected")))

	updates, release := h.subscribe(i18n.English)
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

// Pages in two languages can be open at once, and each must hear an update in
// its own, including the replay a page gets when it first connects.
func TestHubSpeaksEachSubscribersLanguage(t *testing.T) {
	h := newHub()
	renders := 0
	h.publish(eventPlayer, func(l i18n.Lang) string {
		renders++
		return "<p>" + l.Tag() + "</p>"
	})

	en, releaseEN := h.subscribe(i18n.English)
	defer releaseEN()
	pt, releasePT := h.subscribe(i18n.Portuguese)
	defer releasePT()
	en2, releaseEN2 := h.subscribe(i18n.English)
	defer releaseEN2()

	assert.Equal(t, "<p>en</p>", (<-en).html)
	assert.Equal(t, "<p>pt-BR</p>", (<-pt).html)
	assert.Equal(t, "<p>en</p>", (<-en2).html)

	renders = 0
	h.publish(eventToast, func(l i18n.Lang) string {
		renders++
		return l.T("Device connected")
	})
	assert.Equal(t, "Device connected", (<-en).html)
	assert.Equal(t, "Device connected", (<-en2).html)
	require.NotEqual(t, "Device connected", (<-pt).html, "the Portuguese page got English")
	assert.Equal(t, 2, renders, "an update is rendered once per language, not once per page")
}

// A language that fails to render is skipped rather than sent an empty event.
func TestHubSkipsAnEmptyRender(t *testing.T) {
	h := newHub()
	updates, release := h.subscribe(i18n.English)
	defer release()

	h.publish(eventToast, fixed(""))

	select {
	case u := <-updates:
		t.Errorf("sent an empty %q", u.event)
	default:
	}
}

// A slow subscriber must be dropped from, never block, the device reader.
func TestHubDropsForSlowSubscribers(t *testing.T) {
	h := newHub()
	_, release := h.subscribe(i18n.English)
	defer release()

	// Far more than the channel buffer; publish must not block.
	done := make(chan struct{})
	go func() {
		for range 100 {
			h.publish(eventToast, fixed("x"))
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
