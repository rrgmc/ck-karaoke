package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheUnitHasNoSetupPage(t *testing.T) {
	s := backupServer(t)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/setup/code", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/queue", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `href="/setup"`, "no Setup tab")
	assert.NotContains(t, rec.Body.String(), `href="/mine"`, "no My songs tab")
	assert.Contains(t, rec.Body.String(), "· CK Karaoke</title>")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mine", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestQueueEntriesActOnTheirIDs(t *testing.T) {
	tmpl := templates(t)
	render := func(caps Capabilities) string {
		var b strings.Builder
		err := tmpl.ExecuteTemplate(&b, "_queue.html", queueData{Total: 2, Caps: caps, Entries: []queueEntry{
			{Position: 1, EntryID: "7", SongID: "1001", Title: "PRESENTE DE DEUS"},
			{Position: 2, EntryID: "8", SongID: "1001", Title: "PRESENTE DE DEUS"},
		}})
		require.NoError(t, err)
		return b.String()
	}

	byEntry := render(Capabilities{EntryIDs: true})
	assert.Contains(t, byEntry, `/queue/7/remove`)
	assert.Contains(t, byEntry, `/queue/8/up`)
	assert.NotContains(t, byEntry, `/queue/7/up`, "the first entry cannot go up")
	assert.NotContains(t, byEntry, `/song/1001/remove`)

	bySong := render(Capabilities{})
	assert.Contains(t, bySong, `/song/1001/remove`)
	assert.NotContains(t, bySong, `/queue/7/`)
}
