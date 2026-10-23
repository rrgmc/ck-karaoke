package webui

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/favorites/xmlbackup"
	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// This file uses testify, per the repo's standard for new tests. The other webui tests
// beside it predate that and are left as they are.

// catalogueSongs is the whole of the catalogue these tests run against. Small on
// purpose: what is being checked is which ids survive a round trip, and three
// songs plus one id that is deliberately absent says that as well as thousands.
var catalogueSongs = map[int]struct{ Title, Artist string }{
	101:   {"A Banda", "Chico Buarque"},
	2511:  {"Evidências", "Chitãozinho & Xororó"},
	10233: {"Anna Júlia", "Los Hermanos"},
}

// missingID is in no catalogue, and stands for a song another device has and
// this one does not.
const missingID = 99999

// catalogue writes a minimal song database of the shape catalog.Open expects.
//
// Built here rather than copied from a real device database so the web tests
// stay hermetic. Only the columns catalog actually reads are created, so this is a fixture for the
// queries rather than a reproduction of the device's schema.
func catalogue(t *testing.T) *catalog.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "song.db")

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)

	_, err = db.Exec(`
		create table TableDBInfo (ProjectName text, ProductID int, DBVersion int);
		insert into TableDBInfo values ('MK8513-BR-B', 1, 1);
		create table TableLan (LanType int, LanName_EG text);
		insert into TableLan values (1, 'Portuguese');
		create table TableSinger (SingerIndex int, SingerName text);
		insert into TableSinger values (1, 'Chico Buarque');
		create table TableSong (
			SongIndex int, SongName text, SingerName text, LanType int, FirstWord text);`)
	require.NoError(t, err)

	for id, song := range catalogueSongs {
		_, err = db.Exec(`insert into TableSong values (?, ?, ?, 1, '')`,
			id, song.Title, song.Artist)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	songs, err := catalog.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { songs.Close() })
	return songs
}

// backupServer is a whole server: unlike syncServer it carries a device and a
// catalogue, because the backup pages are full renders rather than fragments and
// s.page reads both.
func backupServer(t *testing.T) *Server {
	t.Helper()
	favs, err := favorites.Open(filepath.Join(t.TempDir(), "favorites.db"))
	require.NoError(t, err)
	t.Cleanup(func() { favs.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Server{
		favs:   favs,
		songs:  catalogue(t),
		dev:    NewDevice("127.0.0.1:0", protocol.Identity{}, logger, nil),
		tmpl:   templates(t),
		logger: logger,
	}
}

// fill files songs into a named folder, creating it.
func fill(t *testing.T, s *Server, name string, ids ...int) favorites.Folder {
	t.Helper()
	ctx := context.Background()
	f, _, err := s.favs.EnsureFolder(ctx, name)
	require.NoError(t, err)
	_, err = s.favs.AddSongs(ctx, f.ID, ids)
	require.NoError(t, err)
	return f
}

func export(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.backupExport(w, httptest.NewRequest("GET", "/favorites/backup.xml", nil))
	return w
}

// restore posts a file the way the browser's form does.
func restore(t *testing.T, s *Server, filename string, content []byte) string {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, form.Close())

	r := httptest.NewRequest("POST", "/favorites/backup/import", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	s.backupRestore(w, r)
	return w.Body.String()
}

// The attachment header is the whole mechanism: it is what makes a browser save
// the response, and it is what both app wrappers watch for to hand the file to
// the phone. Losing it would leave the page rendering XML at the user.
func TestExportIsAnAttachment(t *testing.T) {
	s := backupServer(t)
	fill(t, s, "Rock", 101)

	w := export(t, s)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/xml; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Regexp(t, `^attachment; filename="ck-karaoke-favorites-\d{4}-\d{2}-\d{2}\.xml"$`,
		w.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"),
		"the favorites change and the URL does not")
}

func TestExportCarriesEveryFolderAndItsTitles(t *testing.T) {
	s := backupServer(t)
	fill(t, s, "Rock", 101, 10233)
	fill(t, s, "Sertanejo", 2511)

	read, err := xmlbackup.Parse(export(t, s).Body)
	require.NoError(t, err)

	assert.Equal(t, "MK8513-BR-B", read.Catalogue)
	assert.NotEmpty(t, read.Exported)

	byName := map[string][]xmlbackup.Song{}
	for _, f := range read.Folders {
		byName[f.Name] = f.Songs
	}
	require.Contains(t, byName, "Rock")
	require.Contains(t, byName, "Sertanejo")
	assert.Len(t, byName["Rock"], 2)
	assert.Len(t, byName["Sertanejo"], 1)

	// Titles are joined from the catalogue, so a file is worth opening.
	assert.Equal(t, "Evidências", byName["Sertanejo"][0].Title)
	assert.Equal(t, "Chitãozinho & Xororó", byName["Sertanejo"][0].Artist)
}

// The catalogue is refreshed independently of the favorites, so an id it does
// not carry today may be carried tomorrow. Dropping it from the file would lose
// it for good, which is the one thing a backup must not do.
func TestExportKeepsSongsTheCatalogueCannotName(t *testing.T) {
	s := backupServer(t)
	fill(t, s, "Rock", 101, missingID)

	read, err := xmlbackup.Parse(export(t, s).Body)
	require.NoError(t, err)

	require.Len(t, read.Folders, 1)
	ids := []int{}
	for _, song := range read.Folders[0].Songs {
		ids = append(ids, song.ID)
		if song.ID == missingID {
			assert.Empty(t, song.Title, "there is no title to write for it")
		}
	}
	assert.ElementsMatch(t, []int{101, missingID}, ids)
}

// An empty folder carries nothing and restores to nothing, so it is not written
// — and the page that offers the download must agree with the file about how
// many folders there are.
func TestExportSkipsEmptyFolders(t *testing.T) {
	s := backupServer(t)
	fill(t, s, "Rock", 101)
	_, _, err := s.favs.EnsureFolder(context.Background(), "Empty")
	require.NoError(t, err)

	read, err := xmlbackup.Parse(export(t, s).Body)
	require.NoError(t, err)
	require.Len(t, read.Folders, 1)
	assert.Equal(t, "Rock", read.Folders[0].Name)

	w := httptest.NewRecorder()
	s.backupChoose(w, httptest.NewRequest("GET", "/favorites/backup", nil))
	assert.Contains(t, w.Body.String(), "1 folder", "the page counted a folder it will not write")
}

func TestRestoreCreatesMissingFolders(t *testing.T) {
	s := backupServer(t)
	file := []byte(`<favorites version="1">
		<folder name="Rock"><song id="101"/><song id="10233"/></folder>
	</favorites>`)

	body := restore(t, s, "backup.xml", file)
	assert.Contains(t, body, "2 songs added")
	assert.Contains(t, body, "1 folder created")

	f, created, err := s.favs.EnsureFolder(context.Background(), "Rock")
	require.NoError(t, err)
	require.False(t, created, "the restore should have made it")
	assert.Equal(t, 2, f.Songs)
}

// Restoring is add-only and idempotent by construction, which is why there is no
// confirmation step in front of it. A second read must be a no-op.
func TestRestoringTwiceAddsNothingTheSecondTime(t *testing.T) {
	s := backupServer(t)
	fill(t, s, "Rock", 101)
	file := export(t, s).Body.Bytes()

	first := restore(t, s, "backup.xml", file)
	assert.Contains(t, first, "0 songs added")
	assert.Contains(t, first, "1 already here")

	second := restore(t, s, "backup.xml", file)
	assert.Contains(t, second, "0 songs added")

	folders, err := s.favs.Folders(context.Background())
	require.NoError(t, err)
	assert.Len(t, folders, 2, "only the default folder and Rock, however often it is read")
}

// The guarantee the whole feature rests on: a restore adds and never takes away.
func TestRestoreTakesNothingAway(t *testing.T) {
	s := backupServer(t)
	rock := fill(t, s, "Rock", 101, 2511)
	file := []byte(`<favorites version="1">
		<folder name="rock"><song id="10233"/></folder>
	</favorites>`)

	restore(t, s, "backup.xml", file)

	ids, err := s.favs.SongIDs(context.Background(), rock.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{101, 2511, 10233}, ids,
		"the songs already filed must survive, and the new one join them")

	f, _, err := s.favs.EnsureFolder(context.Background(), "Rock")
	require.NoError(t, err)
	assert.Equal(t, "Rock", f.Name, "a differently-cased name in the file must not rename it")
	assert.Equal(t, rock.ID, f.ID, "and must not make a second folder")
}

// Folder counts come from the favorites database while the listing is filtered
// through the catalogue, so an id nothing can display would make a folder claim
// more songs than it lists. It is dropped, and said out loud.
func TestRestoreDropsSongsThisCatalogueLacks(t *testing.T) {
	s := backupServer(t)
	file := []byte(`<favorites version="1">
		<folder name="Rock"><song id="101"/><song id="99999"/></folder>
	</favorites>`)

	body := restore(t, s, "backup.xml", file)
	assert.Contains(t, body, "1 song added")
	assert.Contains(t, body, "not in this device's song list")

	f, _, err := s.favs.EnsureFolder(context.Background(), "Rock")
	require.NoError(t, err)
	assert.Equal(t, 1, f.Songs, "the count must match what opening the folder would list")
}

// Everything a person can hand a file picker ends up here, and none of it may
// reach a 500 — the page has to say what went wrong and offer the picker again.
func TestRestoreRefusesWhatItCannotRead(t *testing.T) {
	for name, tc := range map[string]struct {
		file []byte
		want string
	}{
		"a photograph":     {[]byte("\xff\xd8\xff\xe0 JFIF"), "does not look like a favorites file"},
		"someone's export": {[]byte(`<playlist><song id="1"/></playlist>`), "does not look like a favorites file"},
		"a newer version": {[]byte(`<favorites version="99"><folder name="R"><song id="101"/></folder></favorites>`),
			"newer version"},
		"an empty backup": {[]byte(`<favorites version="1"></favorites>`), "no songs in this file"},
	} {
		t.Run(name, func(t *testing.T) {
			s := backupServer(t)
			body := restore(t, s, "picked.xml", tc.file)

			assert.Contains(t, body, tc.want)
			assert.Contains(t, body, `type="file"`, "the picker has to still be there to try again")

			folders, err := s.favs.Folders(context.Background())
			require.NoError(t, err)
			assert.Len(t, folders, 1, "nothing may be created by a file that was refused")
		})
	}
}

// A restore reads no file at all if the form arrives without one, which is what
// a picker cancelled at the wrong moment produces.
func TestRestoreWithNoFileSaysSo(t *testing.T) {
	s := backupServer(t)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	require.NoError(t, form.Close())

	r := httptest.NewRequest("POST", "/favorites/backup/import", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	s.backupRestore(w, r)

	assert.Contains(t, w.Body.String(), "no file was chosen")
}

// A file larger than any favorites database could be is refused before it is
// read, rather than parsed at whatever length it happens to be.
func TestRestoreRefusesAnOversizedFile(t *testing.T) {
	s := backupServer(t)

	body := restore(t, s, "huge.xml", bytes.Repeat([]byte("<!-- padding -->"), (maxUpload/16)+64))

	assert.Contains(t, body, "larger than 8 MB")
}

// Backup is reachable from the folder list and nowhere else — the mirror of sync
// being reachable only from inside a folder.
func TestBackupIsOfferedOnTheFolderListOnly(t *testing.T) {
	s := backupServer(t)
	folders, err := s.favs.Folders(context.Background())
	require.NoError(t, err)

	list := render(t, s, "_browsepage.html", pageData{
		Browse: browseData{Mode: modeFavorites, Folders: folders},
	})
	assert.Contains(t, list, `href="/favorites/backup"`)

	inside := render(t, s, "_browsepage.html", pageData{
		Browse: browseData{Mode: modeFavorites, FolderID: folders[0].ID, Folder: folders[0]},
	})
	assert.NotContains(t, inside, `href="/favorites/backup"`)
	assert.Contains(t, inside, "/favorites/sync/", "sync is what belongs inside a folder")
}

// Nothing on these pages is htmx: the export has to be a real navigation the
// browser can turn into a file, and a swap would eat it.
func TestBackupPagesAreOrdinaryNavigations(t *testing.T) {
	s := backupServer(t)
	fill(t, s, "Rock", 101)

	for _, page := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"backup", s.backupChoose, "/favorites/backup"},
		{"import", s.backupImport, "/favorites/backup/import"},
	} {
		t.Run(page.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			page.call(w, httptest.NewRequest("GET", page.path, nil))

			body := w.Body.String()
			require.Equal(t, http.StatusOK, w.Code)
			assert.True(t, strings.Contains(body, "<!doctype html>"),
				"a fragment would leave the browser with no page to save from")
			assert.NotContains(t, body, "hx-get=\"/favorites/backup")
			assert.NotContains(t, body, "hx-post=\"/favorites/backup")
		})
	}
}
