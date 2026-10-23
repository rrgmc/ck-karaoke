package webui

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/favorites"
)

// post runs one htmx request against a handler and returns the body.
func post(t *testing.T, s *Server, h func(http.ResponseWriter, *http.Request),
	target string, folder, song int, form string) string {
	t.Helper()
	r := httptest.NewRequest("POST", target, strings.NewReader(form))
	r.Header.Set("HX-Request", "true")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("folder", strconv.Itoa(folder))
	r.SetPathValue("song", strconv.Itoa(song))
	w := httptest.NewRecorder()
	h(w, r)
	return w.Body.String()
}

func renderSheetWith(t *testing.T, s *Server, data sheetData) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "_sheet.html", data); err != nil {
		t.Fatalf("rendering the sheet: %v", err)
	}
	return buf.String()
}

func mustFolders(t *testing.T, s *Server) []favorites.Folder {
	t.Helper()
	folders, err := s.favs.Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return folders
}

// The common case is one folder, so a tap files the song and the sheet goes
// away. htmx clears #sheet because the body carries nothing but out-of-band
// swaps — a stray element here would leave the sheet up instead.
func TestTogglingOnceClosesTheSheet(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)

	body := post(t, s, s.toggleFavorite, "/favorites/toggle/1/2511", f.ID, 2511, "multi=0")

	if strings.Contains(body, "sheet-list") || strings.Contains(body, "sheet-backdrop") {
		t.Error("the sheet was re-rendered instead of closing")
	}
	// The row behind it still has to catch up, and the toast is the only
	// confirmation left once the sheet has gone.
	if !strings.Contains(body, "hx-swap-oob") {
		t.Error("nothing was sent to refresh the row's star")
	}
	if !strings.Contains(body, "Added to "+f.Name) {
		t.Error("no toast said where the song went")
	}

	in, err := s.favs.FoldersForSong(context.Background(), 2511)
	if err != nil {
		t.Fatal(err)
	}
	if !in[f.ID] {
		t.Error("the song was not actually filed")
	}
}

// The secondary way: the sheet stays up so several folders can be ticked.
func TestTogglingInMultiKeepsTheSheetOpen(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)

	body := post(t, s, s.toggleFavorite, "/favorites/toggle/1/2511", f.ID, 2511, "multi=1")

	if !strings.Contains(body, "sheet-list") {
		t.Fatal("the sheet closed; there would be no way to pick a second folder")
	}
	// And it must still be in multi mode, or the next tap would close it.
	if !strings.Contains(body, `"multi":"1"`) {
		t.Error("the sheet dropped back to single mode after one tap")
	}
	if !strings.Contains(body, ">Done<") {
		t.Error("no way to finish once several are ticked")
	}
}

// Primary and secondary have to be distinguishable on the sheet itself.
func TestSheetOffersMultiAsTheSecondWay(t *testing.T) {
	s := syncServer(t)
	folders := mustFolders(t, s)

	single := renderSheetWith(t, s, sheetData{
		SongID: 2511, Title: "A Banda", Folders: folders, In: map[int]bool{},
	})
	if !strings.Contains(single, "Add to more than one") {
		t.Error("no way into multi-select")
	}
	if !strings.Contains(single, `"multi":"0"`) {
		t.Error("the rows do not carry single mode, so a tap would keep the sheet up")
	}
	if strings.Contains(single, ">Done<") {
		t.Error("single mode shows Done, which implies the sheet stays open")
	}

	multi := renderSheetWith(t, s, sheetData{
		SongID: 2511, Title: "A Banda", Folders: folders, In: map[int]bool{}, Multi: true,
	})
	if !strings.Contains(multi, ">Done<") {
		t.Error("multi mode has no way to finish")
	}
	if strings.Contains(multi, "Add to more than one") {
		t.Error("multi mode still offers the way into itself")
	}
}

// Making a folder is not filing a song into it, so the sheet stays either way —
// and it must not lose the mode on the way through.
func TestCreatingAFolderFromTheSheetKeepsTheMode(t *testing.T) {
	s := syncServer(t)

	body := post(t, s, s.createFolder, "/favorites/folders", 0, 0, "song=2511&multi=1&name=Rock")
	if !strings.Contains(body, "sheet-list") {
		t.Fatal("the sheet closed after making a folder")
	}
	if !strings.Contains(body, `"multi":"1"`) {
		t.Error("making a folder dropped the sheet back to single mode")
	}
	if !strings.Contains(body, "Rock") {
		t.Error("the new folder is not in the list")
	}
}

// Removing a song is the one action here that takes something away, so it is
// behind the ⋯ toggle like the queue-skipping ones, red, and confirmed.
func TestRemoveFromFolderIsGuarded(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	songs := []catalog.Song{{ID: 2511, Title: "A Banda", Artist: "Nara Leão"}}

	rows := func(b browseData) string {
		t.Helper()
		var buf bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&buf, "_rows.html", b); err != nil {
			t.Fatalf("rendering rows: %v", err)
		}
		return buf.String()
	}

	inFolder := browseData{Mode: modeFavorites, FolderID: f.ID, Folder: f, Songs: songs}
	if out := rows(inFolder); strings.Contains(out, "/remove/") {
		t.Error("the remove button is on every row before the ⋯ toggle is on")
	}

	inFolder.ShowExtraActions = true
	shown := rows(inFolder)
	if !strings.Contains(shown, "/favorites/folders/"+strconv.Itoa(f.ID)+"/remove/2511") {
		t.Fatal("no way to remove a song from the folder")
	}
	if !strings.Contains(shown, "btn-outline-danger") {
		t.Error("the remove button is not red")
	}
	if !strings.Contains(shown, "hx-confirm") {
		t.Error("removing a song is not confirmed")
	}

	// Outside a folder "remove" has nothing to mean.
	loose := browseData{Mode: modeSongs, Songs: songs, ShowExtraActions: true}
	if out := rows(loose); strings.Contains(out, "/remove/") {
		t.Error("a plain song list offers to remove from a folder")
	}
}
