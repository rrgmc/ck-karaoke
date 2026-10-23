package webui

import (
	"bytes"
	"context"
	"image/png"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/favorites/qrshare"
)

func syncServer(t *testing.T) *Server {
	t.Helper()
	favs, err := favorites.Open(filepath.Join(t.TempDir(), "favorites.db"))
	if err != nil {
		t.Fatalf("opening favorites: %v", err)
	}
	t.Cleanup(func() { favs.Close() })
	return &Server{
		favs:   favs,
		tmpl:   templates(t),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func defaultFolder(t *testing.T, s *Server) favorites.Folder {
	t.Helper()
	folders, err := s.favs.Folders(context.Background())
	if err != nil || len(folders) == 0 {
		t.Fatalf("listing folders: %v", err)
	}
	return folders[0]
}

func render(t *testing.T, s *Server, name string, data pageData) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("rendering %s: %v", name, err)
	}
	return buf.String()
}

// The image has to be a real PNG the browser will draw, and square, or the
// camera has nothing to read.
func TestFavoritesQRServesAnImage(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	if _, err := s.favs.AddSongs(context.Background(), f.ID, []int{101, 2511, 10233}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/favorites/qr/"+strconv.Itoa(f.ID), nil)
	r.SetPathValue("folder", strconv.Itoa(f.ID))
	w := httptest.NewRecorder()
	s.favoritesQR(w, r)

	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	// The folder changes as songs are filed and the URL does not, so a cached
	// image would show a code for songs that have moved on.
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("the body is not a PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() {
		t.Errorf("image is %dx%d, want square", b.Dx(), b.Dy())
	}
	// Scaled to a band, not a fixed number: the module count varies with how
	// many songs there are, and the scale is a whole number of pixels per module.
	if b.Dx() < 700 || b.Dx() > 1400 {
		t.Errorf("image is %d pixels across, want roughly 900", b.Dx())
	}
}

func TestFavoritesQRRefusesAnEmptyFolder(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)

	r := httptest.NewRequest("GET", "/favorites/qr/"+strconv.Itoa(f.ID), nil)
	r.SetPathValue("folder", strconv.Itoa(f.ID))
	w := httptest.NewRecorder()
	s.favoritesQR(w, r)

	if w.Code != 400 {
		t.Errorf("status %d for an empty folder, want 400", w.Code)
	}
}

// The name travelling is what makes a mis-scan visible before anything is
// written, so it has to survive encoding.
func TestFolderCodeCarriesTheName(t *testing.T) {
	s := syncServer(t)
	ctx := context.Background()
	rock, err := s.favs.CreateFolder(ctx, "Forró & Pagode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.favs.AddSongs(ctx, rock.ID, []int{11, 22, 33}); err != nil {
		t.Fatal(err)
	}

	data, err := s.folderCode(ctx, rock)
	if err != nil {
		t.Fatal(err)
	}
	got, err := qrshare.Decode(data.Code)
	if err != nil {
		t.Fatalf("the code this device shows does not decode: %v", err)
	}
	if got.Name != "Forró & Pagode" {
		t.Errorf("name came back as %q", got.Name)
	}
	if len(got.IDs) != 3 {
		t.Errorf("got %d songs, want 3", len(got.IDs))
	}
}

// Past roughly a thousand songs a code outgrows what any QR can hold. The page
// must say so and fall back to the text, rather than linking an image the
// handler will refuse and leaving a broken icon with no explanation.
func TestHugeFolderFallsBackToText(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)

	ids := make([]int, 0, 2000)
	for i := range 2000 {
		ids = append(ids, 10000+i)
	}
	if _, err := s.favs.AddSongs(context.Background(), f.ID, ids); err != nil {
		t.Fatal(err)
	}
	f = defaultFolder(t, s)

	data, err := s.folderCode(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !data.TooDense {
		t.Fatal("a 2000-song folder was not marked too dense for a QR")
	}
	if data.Code == "" {
		t.Error("the text code went missing, leaving no way to share at all")
	}

	out := render(t, s, "sync_code.html", pageData{Sync: data})
	if strings.Contains(out, "/favorites/qr/") {
		t.Error("the page still links a QR image that cannot be generated")
	}
	if !strings.Contains(out, data.Code) {
		t.Error("the text fallback is not on the page")
	}
}

// With no catalogue there is nothing to check ids against, and rejecting them
// all would silently drop every song.
func TestKnownSongsWithoutACatalogueAcceptsEverything(t *testing.T) {
	s := syncServer(t)
	got, err := s.knownSongs(context.Background(), []int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("kept %d of 3 songs", len(got))
	}
}

// The chooser settles which device you are, and nothing else — the folder was
// settled by being inside it.
func TestChooserOffersBothDirectionsForOneFolder(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	out := render(t, s, "sync.html", pageData{Title: "Sync", Sync: syncData{Folder: f}})

	root := "/favorites/sync/" + strconv.Itoa(f.ID)
	for _, want := range []string{root + "/send", root + "/receive", "Send", "Receive"} {
		if !strings.Contains(out, want) {
			t.Errorf("the chooser does not mention %q", want)
		}
	}
	// Nothing here should ask which folder: that question is what was removed.
	if strings.Contains(out, "Which folder") {
		t.Error("the chooser is still asking which folder")
	}
	if strings.Contains(out, "scan-video") || strings.Contains(out, "/favorites/qr/") {
		t.Error("the chooser is doing more than choosing")
	}
}

var inputTag = regexp.MustCompile(`<input[^>]*>`)

// The code is machinery. Showing it was most of what made this look like a
// tool rather than a task, so it may appear only as a hidden field's value.
func TestReceivedCodeIsNeverReadable(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)

	code, err := qrshare.Encode(qrshare.Folder{Name: f.Name, IDs: []int{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	scanned, err := qrshare.Decode(code)
	if err != nil {
		t.Fatal(err)
	}

	out := render(t, s, "sync_confirm.html", pageData{Sync: syncData{
		Folder: f, Scanned: scanned, Raw: code,
	}})

	if visible := inputTag.ReplaceAllString(out, ""); strings.Contains(visible, code) {
		t.Error("the code is on the page as readable text, not only in a hidden field")
	}
	if !strings.Contains(out, `value="`+code+`"`) {
		t.Error("the code is not carried forward, so Merge would have nothing to work on")
	}

	// What should be readable instead: where it is going, and how much.
	if !strings.Contains(out, "Add to "+f.Name) {
		t.Error("the destination is not offered as one button")
	}
	if !strings.Contains(out, "3 songs") {
		t.Error("the page does not say how much arrived")
	}
}

// The destination is the folder you were in, so there is nothing to choose —
// and nothing that could create a folder either.
func TestConfirmChoosesNothing(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	scanned := qrshare.Folder{Name: f.Name, IDs: []int{1, 2}}

	out := render(t, s, "sync_confirm.html", pageData{Sync: syncData{
		Folder: f, Scanned: scanned, Raw: "x",
	}})

	if strings.Contains(out, `name="name"`) {
		t.Error("the confirmation has a folder-name box")
	}
	if strings.Contains(out, `name="folder"`) {
		t.Error("the confirmation still asks which folder")
	}
}

// Scanning the wrong folder's code looks exactly like deliberately merging one
// folder into another, so the difference has to be on screen before the write.
func TestConfirmNamesAMismatchedSource(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)

	same := render(t, s, "sync_confirm.html", pageData{Sync: syncData{
		Folder: f, Scanned: qrshare.Folder{Name: f.Name, IDs: []int{1}}, Raw: "x",
	}})
	if strings.Contains(same, "This code came from") {
		t.Error("a matching name was reported as a mismatch")
	}

	other := render(t, s, "sync_confirm.html", pageData{Sync: syncData{
		Folder: f, Scanned: qrshare.Folder{Name: "Sertanejo", IDs: []int{1}}, Raw: "x",
	}})
	if !strings.Contains(other, "This code came from") {
		t.Error("a code from another folder merged with no warning")
	}
}

// The scanner is markup plus two scripts, and the ids are the contract between
// them: scan.js gives up silently if any is missing, which would look like a
// camera that simply does not work.
func TestReceivePageCarriesTheScanner(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	out := render(t, s, "sync_receive.html", pageData{Sync: syncData{Folder: f}})

	for _, want := range []string{
		`id="scan"`, `id="scan-start"`, `id="scan-stop"`,
		`id="scan-view"`, `id="scan-video"`, `id="scan-status"`,
		`id="scan-form"`, `id="scan-code"`, // where the result goes, and what submits it
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the scanner markup is missing %s", want)
		}
	}
	if !strings.Contains(out, "playsinline") {
		t.Error("the video is not marked playsinline; iOS would go fullscreen")
	}
	if !strings.Contains(out, `id="scan" class="scan" hidden`) {
		t.Error("the scanner is not hidden by default")
	}
	// It has to post back to this folder, or the songs land somewhere else.
	if !strings.Contains(out, `action="/favorites/sync/`+strconv.Itoa(f.ID)+`/receive"`) {
		t.Error("the scan form does not post back to this folder")
	}

	// The decoder has to load before the script that calls it.
	jsqr, scan := strings.Index(out, "jsqr.js"), strings.Index(out, "scan.js")
	if jsqr < 0 || scan < 0 {
		t.Fatalf("scripts missing: jsqr at %d, scan at %d", jsqr, scan)
	}
	if jsqr > scan {
		t.Error("scan.js is loaded before jsqr.js, so window.jsQR would be undefined")
	}
}

// Both scripts must actually be embedded and hashed, or the page links URLs
// that 404 and the scanner never appears.
func TestScannerAssetsAreEmbedded(t *testing.T) {
	for _, name := range []string{"jsqr.js", "scan.js"} {
		if _, ok := assetVersions[name]; !ok {
			t.Errorf("%s is not embedded", name)
		}
	}
}

// A merge goes one way, so the page that ends it has to point at the return
// leg rather than leave both devices looking finished.
func TestDoneOffersTheReturnLeg(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	out := render(t, s, "sync_done.html", pageData{Sync: syncData{
		Folder: f,
		Result: &mergeResult{Folder: f.Name, FolderID: f.ID, Scanned: 9, Added: 4, Already: 3, Unknown: 2},
	}})

	if !strings.Contains(out, "/favorites/sync/"+strconv.Itoa(f.ID)+"/send") {
		t.Error("no way back to showing this device's code")
	}
	if !strings.Contains(out, "not in this device's song list") {
		t.Error("the skipped songs are not explained")
	}
	if !strings.Contains(out, "folder="+strconv.Itoa(f.ID)) {
		t.Error("no link to the folder that just changed")
	}
}

// Sync runs several screens deep, and every one of them needs the way out —
// otherwise finishing means backing out a step at a time.
func TestEverySyncScreenLeavesToFavorites(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	if _, err := s.favs.AddSongs(context.Background(), f.ID, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	f = defaultFolder(t, s)
	sent, err := s.folderCode(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	scanned := qrshare.Folder{Name: f.Name, IDs: []int{1, 2}}

	for name, data := range map[string]pageData{
		"sync.html":         {Title: "Sync", Sync: syncData{Folder: f}},
		"sync_code.html":    {Title: f.Name, Sync: sent},
		"sync_receive.html": {Title: "Receive", Sync: syncData{Folder: f}},
		"sync_confirm.html": {Title: "Received", Sync: syncData{Folder: f, Scanned: scanned, Raw: "x"}},
		"sync_done.html": {Title: "Merged", Sync: syncData{
			Folder: f, Result: &mergeResult{Folder: f.Name, FolderID: f.ID, Added: 2},
		}},
	} {
		out := render(t, s, name, data)
		if !strings.Contains(out, `href="/?mode=favorites"`) {
			t.Errorf("%s has no way out to the favorites list", name)
		}
	}
}

// Sync belongs to a folder, so it is offered inside one and nowhere else.
func TestSyncIsOfferedOnlyInsideAFolder(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)

	inside := render(t, s, "_browsepage.html", pageData{Browse: browseData{
		Mode: modeFavorites, FolderID: f.ID, Folder: f,
	}})
	if !strings.Contains(inside, "/favorites/sync/"+strconv.Itoa(f.ID)) {
		t.Error("no way to sync the folder you are inside")
	}

	list := render(t, s, "_browsepage.html", pageData{Browse: browseData{Mode: modeFavorites}})
	if strings.Contains(list, "/favorites/sync") {
		t.Error("the folder list still offers sync, which asks which folder twice")
	}

	songs := render(t, s, "_browsepage.html", pageData{Browse: browseData{Mode: modeSongs}})
	if strings.Contains(songs, "/favorites/sync") {
		t.Error("the song list offers sync")
	}
}

// The folder list is where folders are made, and until recently nothing there
// could.
func TestFolderListCanCreateAFolder(t *testing.T) {
	s := syncServer(t)
	out := render(t, s, "_browsepage.html", pageData{Browse: browseData{Mode: modeFavorites}})

	if !strings.Contains(out, `for="newfolder"`) {
		t.Error("the folder list offers no button to create one")
	}
	// The button sits in the control row; the panel it opens has to be outside
	// the search form, since a form inside a form is not a thing. If they ever
	// end up in the same place, the markup is invalid and htmx swallows one.
	row := strings.Index(out, `class="filter-row"`)
	closeForm := strings.Index(out[row:], "</form>") + row
	panel := strings.Index(out, "newfolder-form")
	if !(row < strings.Index(out, `for="newfolder"`) && strings.Index(out, `for="newfolder"`) < closeForm) {
		t.Error("the + Folder button is not in the control row")
	}
	if panel < closeForm {
		t.Error("the new-folder panel is nested inside the search form")
	}
}

// ...and only there: a song list has no folders to add to.
func TestSongListHasNoFolderCreation(t *testing.T) {
	s := syncServer(t)
	out := render(t, s, "_browsepage.html", pageData{Browse: browseData{Mode: modeSongs}})

	if strings.Contains(out, "New folder") {
		t.Error("the song list offers folder creation")
	}
}

// Renaming was possible in favorites from the start and reachable from nowhere.
func TestFolderViewCanRename(t *testing.T) {
	s := syncServer(t)
	f := defaultFolder(t, s)
	out := render(t, s, "_browsepage.html", pageData{Browse: browseData{
		Mode: modeFavorites, FolderID: f.ID, Folder: f,
	}})

	if !strings.Contains(out, "Rename") {
		t.Error("no way to rename the folder you are looking at")
	}
	if !strings.Contains(out, "/favorites/folders/"+strconv.Itoa(f.ID)+"/rename") {
		t.Error("the rename form does not post to the rename route")
	}
	// Pre-filled, so correcting a typo does not mean retyping the name.
	if !strings.Contains(out, `value="`+f.Name+`"`) {
		t.Error("the rename field does not start from the current name")
	}
}

// ...and not from the folder list, where it would sit on every row for
// something done rarely.
func TestFolderListHasNoRename(t *testing.T) {
	s := syncServer(t)
	out := render(t, s, "_browsepage.html", pageData{Browse: browseData{Mode: modeFavorites}})
	if strings.Contains(out, "/rename") {
		t.Error("the folder list carries a rename control")
	}
}
