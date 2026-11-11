package webui

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/protocol"
)

// emptyServer is a server with no song list yet, that writes one to the
// returned path.
func emptyServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	favs, err := favorites.Open(filepath.Join(dir, "favorites.db"))
	require.NoError(t, err)
	t.Cleanup(func() { favs.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dev := UnitBackend(NewDevice("", protocol.Identity{}, logger, nil))
	path := filepath.Join(dir, "song.db")
	s, err := New(dev, nil, favs, logger, Options{SongListPath: path})
	require.NoError(t, err)
	t.Cleanup(func() {
		if db := s.Catalogue(); db != nil {
			db.Close()
		}
	})
	return s, path
}

// songListFile returns the bytes of a complete song list, one that passes
// catalog.Verify as well as catalog.Open.
func songListFile(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "song.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	_, err = db.Exec(`
		create table TableDBInfo (ProjectName text, ProductID int, DBVersion int);
		insert into TableDBInfo values ('MK8513-BR-B', 1, 7);
		create table TableLan (LanType int, LanName_EG text);
		insert into TableLan values (1, 'Portuguese');
		create table TableSinger (SingerIndex int, SingerName text, FirstWord text, AlphaIndex int, LanType int);
		insert into TableSinger values (1, 'Chico Buarque', 'CHICO BUARQUE', 3, 1);
		create table TableSong (SongIndex int, SongName text, SingerName text, LanType int, FirstWord text, AlphaIndex int);
		insert into TableSong values (101, 'A BANDA', 'Chico Buarque', 1, 'A BANDA', 1);
		create table TableSongSinger (SongIndex int, SingerIndex int);
		insert into TableSongSinger values (101, 1);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return content
}

// importSongList posts a file the way the browser's form does.
func importSongList(t *testing.T, s *Server, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "song.db")
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, form.Close())

	r := httptest.NewRequest("POST", "/songlist/import", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestSongsTabOffersTheImportWithoutASongList(t *testing.T) {
	s, _ := emptyServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `href="/songlist/import"`)
}

func TestImportInstallsTheSongList(t *testing.T) {
	s, path := emptyServer(t)
	w := importSongList(t, s, songListFile(t))
	assert.Equal(t, http.StatusSeeOther, w.Code)
	require.NotNil(t, s.Catalogue())
	assert.Equal(t, path, s.Catalogue().Path())
	assert.Equal(t, 1, s.Catalogue().Info().Songs)

	// The Songs tab now browses instead of offering the import.
	page := httptest.NewRecorder()
	s.Handler().ServeHTTP(page, httptest.NewRequest("GET", "/", nil))
	assert.NotContains(t, page.Body.String(), `href="/songlist/import"`)
}

func TestImportRefusesWhatIsNotASongList(t *testing.T) {
	s, path := emptyServer(t)

	w := importSongList(t, s, []byte("not really sqlite"))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "That file is not a song list from a karaoke device.")
	assert.Nil(t, s.Catalogue())
	assert.NoFileExists(t, path)

	// Nothing is left beside it either.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "song.db")
	}
}

// The import is for a server that has never had a song list; one that has
// keeps it.
func TestImportIsRefusedOnceThereIsASongList(t *testing.T) {
	s := backupServer(t)
	s.songListPath = filepath.Join(t.TempDir(), "song.db")
	before := s.Catalogue()

	w := importSongList(t, s, []byte("anything"))
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Same(t, before, s.Catalogue())
	assert.NoFileExists(t, s.songListPath)
}

func TestSongListExportIsAnAttachment(t *testing.T) {
	s := backupServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/songlist/song.db", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
	want, err := os.ReadFile(s.Catalogue().Path())
	require.NoError(t, err)
	assert.Equal(t, want, w.Body.Bytes())
}
