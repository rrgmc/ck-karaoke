package webui

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ⋯ toggle holds for the run that turned it on and starts off in the next
// one, so the queue-skipping actions are hidden every time the app starts.
func TestExtraActionsToggleStartsOffEachRun(t *testing.T) {
	first, _ := settingsServer(t)
	second, _ := settingsServer(t)
	require.NotEqual(t, first.runID, second.runID)

	w := httptest.NewRecorder()
	first.rememberShowActions(w, true)
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Zero(t, cookies[0].MaxAge, "the cookie must not outlive the app")

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookies[0])
	assert.True(t, first.recallShowActions(r), "the toggle is lost within the run")
	assert.False(t, second.recallShowActions(r), "the toggle survives a restart")

	off := httptest.NewRecorder()
	first.rememberShowActions(off, false)
	assert.Negative(t, off.Result().Cookies()[0].MaxAge)
}
