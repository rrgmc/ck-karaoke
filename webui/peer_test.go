package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rrgmc/ck-karaoke/favorites/qrshare"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendBackup posts a backup body the way a phone port does, and returns the
// confirm page that the port opens next.
func sendBackup(t *testing.T, s *Server, body string) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.backupIncomingPost(w, httptest.NewRequest("POST", "/favorites/backup/incoming", strings.NewReader(body)))
	require.Equal(t, http.StatusNoContent, w.Code)

	w = httptest.NewRecorder()
	s.backupIncoming(w, httptest.NewRequest("GET", "/favorites/backup/incoming", nil))
	require.Equal(t, http.StatusOK, w.Code)
	return w.Body.String()
}

func postForm(t *testing.T, call func(http.ResponseWriter, *http.Request), path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	call(w, r)
	return w
}

func songIDs(t *testing.T, s *Server, name string) []int {
	t.Helper()
	folders, err := s.favs.Folders(context.Background())
	require.NoError(t, err)
	for _, f := range folders {
		if f.Name == name {
			ids, err := s.favs.SongIDs(context.Background(), f.ID)
			require.NoError(t, err)
			return ids
		}
	}
	return nil
}

func TestSentBackupWaitsForConfirmation(t *testing.T) {
	s := backupServer(t)
	page := sendBackup(t, s, `<favorites version="1">
		<folder name="Rock"><song id="101"/><song id="10233"/></folder>
	</favorites>`)

	assert.Contains(t, page, "2 songs in 1 folder")
	assert.Contains(t, page, `action="/favorites/backup/incoming/import"`)
	assert.Nil(t, songIDs(t, s, "Rock"), "nothing is added before the confirmation")

	w := postForm(t, s.backupIncomingImport, "/favorites/backup/incoming/import", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "2 songs added")
	assert.ElementsMatch(t, []int{101, 10233}, songIDs(t, s, "Rock"))

	// Importing forgets it, so a second post adds nothing again.
	w = postForm(t, s.backupIncomingImport, "/favorites/backup/incoming/import", nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)
}

func TestSentBackupKeepsWhatIsHere(t *testing.T) {
	s := backupServer(t)
	fill(t, s, "Rock", 2511)
	sendBackup(t, s, `<favorites version="1"><folder name="Rock"><song id="101"/></folder></favorites>`)

	postForm(t, s.backupIncomingImport, "/favorites/backup/incoming/import", nil)
	assert.ElementsMatch(t, []int{101, 2511}, songIDs(t, s, "Rock"))
}

func TestSentBackupThatCannotBeReadSaysSo(t *testing.T) {
	s := backupServer(t)
	page := sendBackup(t, s, "not a backup")
	assert.Contains(t, page, "This does not look like a favorites file.")
	assert.NotContains(t, page, `action="/favorites/backup/incoming/import"`)
}

func TestCancelledBackupIsForgotten(t *testing.T) {
	s := backupServer(t)
	sendBackup(t, s, `<favorites version="1"><folder name="Rock"><song id="101"/></folder></favorites>`)

	w := postForm(t, s.backupIncomingCancel, "/favorites/backup/incoming/cancel", nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	w = httptest.NewRecorder()
	s.backupIncoming(w, httptest.NewRequest("GET", "/favorites/backup/incoming", nil))
	assert.Contains(t, w.Body.String(), "Nothing was received")
	assert.Nil(t, songIDs(t, s, "Rock"))
}

func folderCodeFor(t *testing.T, name string, ids ...int) string {
	t.Helper()
	code, err := qrshare.Encode(qrshare.Folder{Name: name, IDs: ids})
	require.NoError(t, err)
	return code
}

func openIncomingFolder(t *testing.T, s *Server, code string) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.folderIncoming(w, httptest.NewRequest("GET", "/favorites/sync/incoming?code="+url.QueryEscape(code), nil))
	require.Equal(t, http.StatusOK, w.Code)
	return w.Body.String()
}

func TestSentFolderChoosesTheFolderOfTheSameName(t *testing.T) {
	s := backupServer(t)
	rock := fill(t, s, "Rock", 2511)
	page := openIncomingFolder(t, s, folderCodeFor(t, "Rock", 101))

	assert.Contains(t, page, `value="`+strconv.Itoa(rock.ID)+`" selected`)
	assert.NotContains(t, page, `value="new"`, "a folder of that name exists")
	assert.Equal(t, []int{2511}, songIDs(t, s, "Rock"), "nothing is added before the choice")
}

func TestSentFolderCanGoIntoANewFolder(t *testing.T) {
	s := backupServer(t)
	code := folderCodeFor(t, "Rock", 101, 10233, missingID)
	page := openIncomingFolder(t, s, code)
	assert.Contains(t, page, `value="new" selected`)

	w := postForm(t, s.folderIncomingMerge, "/favorites/sync/incoming",
		url.Values{"code": {code}, "folder": {"new"}})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "2 songs added")
	assert.ElementsMatch(t, []int{101, 10233}, songIDs(t, s, "Rock"))
}

func TestSentFolderCanGoIntoAnotherFolder(t *testing.T) {
	s := backupServer(t)
	pop := fill(t, s, "Pop", 2511)
	code := folderCodeFor(t, "Rock", 101)

	w := postForm(t, s.folderIncomingMerge, "/favorites/sync/incoming",
		url.Values{"code": {code}, "folder": {strconv.Itoa(pop.ID)}})
	require.Equal(t, http.StatusOK, w.Code)
	assert.ElementsMatch(t, []int{101, 2511}, songIDs(t, s, "Pop"))
	assert.Nil(t, songIDs(t, s, "Rock"))
}

func TestSentFolderThatCannotBeReadSaysSo(t *testing.T) {
	s := backupServer(t)
	page := openIncomingFolder(t, s, "garbage")
	assert.Contains(t, page, "This does not look like a favorites code.")
	assert.NotContains(t, page, `action="/favorites/sync/incoming"`)
}

func TestSendLinksNeedTheOtherApp(t *testing.T) {
	s := backupServer(t)
	rock := fill(t, s, "Rock", 101)

	syncPage := func() string {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/favorites/sync/"+strconv.Itoa(rock.ID), nil)
		r.SetPathValue("folder", strconv.Itoa(rock.ID))
		s.syncChoose(w, r)
		return w.Body.String()
	}
	backupPage := func() string {
		w := httptest.NewRecorder()
		s.backupChoose(w, httptest.NewRequest("GET", "/favorites/backup", nil))
		return w.Body.String()
	}

	assert.NotContains(t, syncPage(), "ckpeer:")
	assert.NotContains(t, backupPage(), "ckpeer:")

	s.peerApp = "CK Queue Remote"
	page := syncPage()
	assert.Contains(t, page, "Send to CK Queue Remote")
	assert.Contains(t, page, `href="ckpeer:folder?code=`+url.QueryEscape(folderCodeFor(t, "Rock", 101))+`"`)
	assert.Contains(t, backupPage(), `href="ckpeer:backup"`)
}
