package webui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// settingsServer is emptyServer with a file to keep the session name in.
func settingsServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, path := emptyServer(t)
	s.nameFile = filepath.Join(filepath.Dir(path), "name.txt")
	return s, s.nameFile
}

func postSettings(s *Server, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestSettingsPageOffersLanguageNameAndSongList(t *testing.T) {
	s, _ := settingsServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/setup", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	page := w.Body.String()
	assert.Contains(t, page, `hx-post="/language"`)
	assert.Contains(t, page, `hx-post="/setup/name"`)
	assert.Contains(t, page, `href="/songlist/import"`, "no song list yet, so the import")
	assert.NotContains(t, page, `id="setup"`, "the unit has no queue manager part")
}

func TestSettingsPageOffersTheSongListExport(t *testing.T) {
	s := backupServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/setup", nil))

	assert.Contains(t, w.Body.String(), `href="/songlist/song.db"`)
	assert.NotContains(t, w.Body.String(), `hx-post="/setup/name"`, "no name file, so no name setting")
}

func TestSettingsSaveTheSessionName(t *testing.T) {
	s, nameFile := settingsServer(t)
	w := postSettings(s, "/setup/name", "name=Sala+de+estar")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Sala de estar", ReadName(nameFile))
	assert.Equal(t, "Sala de estar", s.dev.(unitBackend).dev.Identity().Name)
}

func TestSettingsRefuseAnEmptyName(t *testing.T) {
	s, nameFile := settingsServer(t)
	w := postSettings(s, "/setup/name", "name=+++")

	assert.Contains(t, w.Body.String(), "Type a name first.")
	assert.Empty(t, ReadName(nameFile))
}
