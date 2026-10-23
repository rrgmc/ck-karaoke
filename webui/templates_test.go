package webui

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/favorites"
)

func templates(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		t.Fatalf("parsing templates: %v", err)
	}
	return tmpl
}

// The queue badge is delivered as a server-sent event, and an event carrying no
// data is liable to be dropped rather than dispatched — which would strand a
// stale count on the tab bar exactly when the queue empties. So the zero case
// has to render something, and it has to survive html/template, which silently
// removes comments.
func TestQueueCountNeverRendersEmpty(t *testing.T) {
	tmpl := templates(t)

	for _, n := range []int{0, 1, 2, 50} {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "_queuecount.html", n); err != nil {
			t.Fatalf("rendering count %d: %v", n, err)
		}
		got := buf.String()
		if strings.TrimSpace(got) == "" {
			t.Errorf("count %d rendered nothing; the event that clears the badge would carry no data", n)
		}
		switch {
		case n == 0 && strings.Contains(got, "tab-badge\">"):
			t.Errorf("count 0 drew a badge: %q", got)
		case n > 0 && !strings.Contains(got, ">"+itoa(n)+"<"):
			t.Errorf("count %d did not appear in %q", n, got)
		}
	}
}

// Play-next and play-now cut ahead of the reserved list, so they must not be
// reachable from a search result until the toggle is on. Queueing always is.
func TestSongRowsHideQueueSkippingActions(t *testing.T) {
	tmpl := templates(t)

	data := browseData{
		Mode:  modeSongs,
		Songs: []catalog.Song{{ID: 2511, Title: "A DAY IN THE LIFE", Artist: "Beatles"}},
	}

	render := func(show bool) string {
		data.ShowExtraActions = show
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "_rows.html", data); err != nil {
			t.Fatalf("rendering rows (show=%v): %v", show, err)
		}
		return buf.String()
	}

	hidden := render(false)
	for _, path := range []string{"/song/2511/reserve-top", "/song/2511/play"} {
		if strings.Contains(hidden, path) {
			t.Errorf("%s is reachable by default; a mistap would jump the queue", path)
		}
	}
	if !strings.Contains(hidden, "/song/2511/reserve") {
		t.Error("queueing should always be available")
	}

	shown := render(true)
	for _, path := range []string{"/song/2511/reserve", "/song/2511/reserve-top", "/song/2511/play"} {
		if !strings.Contains(shown, path) {
			t.Errorf("%s missing once the toggle is on", path)
		}
	}
}

// The whole player is replaced every time the device reports, so any button
// without hx-preserve is destroyed and rebuilt about thirty times a minute —
// which is what made a tapped button flicker. Preserving needs a unique id on
// each one, so a duplicate would silently break it.
func TestPlayerButtonsSurviveSwaps(t *testing.T) {
	tmpl := templates(t)

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "_player.html", playerData{Online: true, HasSong: true}); err != nil {
		t.Fatalf("rendering player: %v", err)
	}
	html := buf.String()

	buttons := strings.Count(html, "<button")
	preserved := strings.Count(html, `hx-preserve="true"`)
	if buttons != preserved {
		t.Errorf("%d buttons but %d preserved; the unpreserved ones will flicker", buttons, preserved)
	}

	ids := regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(ids) < buttons {
		t.Errorf("only %d ids for %d buttons; hx-preserve needs one each", len(ids), buttons)
	}
	seen := map[string]bool{}
	for _, m := range ids {
		if seen[m[1]] {
			t.Errorf("duplicate id %q; hx-preserve cannot match it", m[1])
		}
		seen[m[1]] = true
	}
}

// The URL pushed into history must never carry the fragment plumbing. When it
// did, refreshing landed on a bare fragment with no layout and no way back.
func TestBrowseURLOmitsFragmentPlumbing(t *testing.T) {
	cases := []struct {
		name string
		data browseData
		want string
	}{
		{"default", browseData{}, "/"},
		{"search", browseData{Mode: modeSongs, Query: "estranho"}, "/?mode=songs&q=estranho"},
		{"artist", browseData{Mode: modeArtists, ArtistID: 42}, "/?artist=42&mode=artists"},
		// The letter has to be in here or "Load more" fetches page two of the
		// whole catalogue and appends it under the letter you chose.
		{"letter", browseData{Mode: modeSongs, Letter: "S"}, "/?letter=S&mode=songs"},
		{"letter and search", browseData{Mode: modeSongs, Letter: "0", Query: "amor"},
			"/?letter=0&mode=songs&q=amor"},
	}
	for _, c := range cases {
		got := browseURL(c.data)
		if strings.Contains(got, "fragment") {
			t.Errorf("%s: pushed URL carries fragment plumbing: %q", c.name, got)
		}
		if got != c.want {
			t.Errorf("%s: browseURL = %q, want %q", c.name, got, c.want)
		}
	}
}

// Every static asset needs a content-hashed URL. Without one the long cache
// lifetime the handler sets would pin a stale copy after an upgrade.
func TestAssetURLsAreVersioned(t *testing.T) {
	for _, name := range []string{"bootstrap.min.css", "app.css", "htmx.min.js", "htmx-sse.js"} {
		got := assetURL(name)
		if !strings.Contains(got, "?v=") {
			t.Errorf("asset %q has no version in %q", name, got)
		}
	}
	if assetVersions["app.css"] == assetVersions["htmx.min.js"] {
		t.Error("different files share a version; the hash is not content-derived")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// The star must be filled only for songs actually in a folder, and its id has to
// match what a sheet response targets out of band — otherwise tapping a folder
// updates the sheet but leaves a stale star on the row behind it.
func TestStarReflectsFavoriteState(t *testing.T) {
	tmpl := templates(t)

	data := browseData{
		Mode:      modeSongs,
		Songs:     []catalog.Song{{ID: 2511, Title: "A DAY IN THE LIFE"}, {ID: 5853, Title: "ALL SHOOK UP"}},
		Favorited: map[int]bool{2511: true},
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "_rows.html", data); err != nil {
		t.Fatalf("rendering rows: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, `id="fav-2511"`) || !strings.Contains(html, `id="fav-5853"`) {
		t.Fatal("song rows are missing their star buttons")
	}
	filled := regexp.MustCompile(`id="fav-(\d+)" class="[^"]*btn-warning`).FindAllStringSubmatch(html, -1)
	if len(filled) != 1 || filled[0][1] != "2511" {
		t.Errorf("filled stars = %v, want exactly 2511", filled)
	}
}

func TestSheetTicksTheFoldersHoldingTheSong(t *testing.T) {
	tmpl := templates(t)

	render := func(allowDelete bool) string {
		var buf bytes.Buffer
		err := tmpl.ExecuteTemplate(&buf, "_sheet.html", sheetData{
			SongID: 2511,
			Title:  "A DAY IN THE LIFE",
			Folders: []favorites.Folder{
				{ID: 1, Name: "Favorites", Songs: 3},
				{ID: 2, Name: "Rock", Songs: 9},
			},
			In:          map[int]bool{2: true},
			AllowDelete: allowDelete,
		})
		if err != nil {
			t.Fatalf("rendering sheet: %v", err)
		}
		return buf.String()
	}

	html := render(false)
	on := regexp.MustCompile(`aria-pressed="(true|false)"`).FindAllStringSubmatch(html, -1)
	if len(on) != 2 || on[0][1] != "false" || on[1][1] != "true" {
		t.Errorf("ticked folders = %v, want only the second", on)
	}
	if strings.Contains(html, "sheet-delete") {
		t.Error("the delete control is offered although deletion is disabled")
	}

	if !strings.Contains(render(true), "sheet-delete") {
		t.Error("the delete control is missing although deletion is enabled")
	}
}

// The favorites tab searches folder names while the others search the
// catalogue, so the two must not share a search box: arriving at favorites with
// a song title searched folders for it and found none.
func TestFavoritesKeepsItsOwnSearchText(t *testing.T) {
	b := browseData{Mode: modeSongs, SongQuery: "estranho", FavQuery: "pao"}

	if got := b.QueryFor(modeFavorites); got != "pao" {
		t.Errorf("favorites query = %q, want the folder search", got)
	}
	for _, mode := range []string{modeSongs, modeArtists} {
		if got := b.QueryFor(mode); got != "estranho" {
			t.Errorf("%s query = %q, want the song search", mode, got)
		}
	}
}

// The language filter starts on one language, so "all languages" is a choice
// somebody makes rather than where they begin. That makes zero ambiguous in the
// cookie — it is both "all" and the zero value — so the key has to be written
// even when it is zero, or picking All would silently revert to the default.
func TestLanguageCookieDistinguishesAllFromUnset(t *testing.T) {
	srv := &Server{defaultLan: 4}

	rec := httptest.NewRecorder()
	srv.rememberBrowse(rec, browseData{Mode: modeSongs, LanType: 0})
	cookie := rec.Result().Cookies()[0]
	if !strings.Contains(cookie.Value, "lan=0") {
		t.Fatalf("choosing All was not recorded: %q", cookie.Value)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if got := srv.recallBrowse(req).LanType; got != 0 {
		t.Errorf("recalled language %d, want 0 — All reverted to the default", got)
	}

	// No cookie at all is the first visit, which starts on the default.
	if got := srv.recallBrowse(httptest.NewRequest("GET", "/", nil)).LanType; got != 4 {
		t.Errorf("a first visit got language %d, want the default 4", got)
	}
}

// The alphabet only belongs on the two lists long enough to need one, and only
// at their top level. Everywhere else it would either duplicate a shorter list
// or, on the folder list, filter on a column that list never reads.
func TestLetterFilterOnlyWhereItApplies(t *testing.T) {
	yes := map[string]browseData{
		"songs":   {Mode: modeSongs},
		"artists": {Mode: modeArtists},
	}
	for name, b := range yes {
		assert.Truef(t, b.UsesLetter(), "%s should offer the letter filter", name)
	}

	no := map[string]browseData{
		"inside an artist": {Mode: modeSongs, ArtistID: 42},
		"artist's songs":   {Mode: modeArtists, ArtistID: 42},
		"inside a folder":  {Mode: modeFavorites, FolderID: 7},
		"folder list":      {Mode: modeFavorites},
	}
	for name, b := range no {
		assert.Falsef(t, b.UsesLetter(), "%s should not offer the letter filter", name)
	}
}

// A URL and a cookie are both places a letter can arrive from, so neither is
// trusted: anything the select cannot produce filters nothing rather than
// being echoed back into the page.
func TestLetterIsValidated(t *testing.T) {
	for _, in := range []string{"A", "a", "z", "0"} {
		assert.NotEmptyf(t, normalizeLetter(in), "%q is a letter the select offers", in)
	}
	for _, in := range []string{"", "9", "AB", "#", "Á", "  "} {
		assert.Emptyf(t, normalizeLetter(in), "%q is not something the select can produce", in)
	}
	assert.Equal(t, "S", normalizeLetter(" s "), "the value should be normalised, not just accepted")
}

// The letter select is missing from the favorites list, so hx-include
// finds nothing there. Absent must therefore mean "unchanged" rather than
// "cleared", or switching to that tab and back would drop a letter nobody
// unset — the same distinction the language filter draws.
func TestLetterSurvivesATabWithNoLetterSelect(t *testing.T) {
	q := url.Values{"mode": {"songs"}}
	assert.Equal(t, "S", letterParam(q, "S"), "an absent letter must keep the remembered one")

	// Present but empty is the reader choosing A–Z, which must clear it.
	q.Set("letter", "")
	assert.Equal(t, "", letterParam(q, "S"), "choosing A–Z did not clear the letter")

	q.Set("letter", "m")
	assert.Equal(t, "M", letterParam(q, "S"))
}

// The letter rides in the browse cookie beside the search text, because it is
// where you are rather than how you like things — and unlike the language it
// needs no "written even when zero" dance, since empty has no default to
// revert to.
func TestLetterRoundTripsThroughTheCookie(t *testing.T) {
	srv := &Server{defaultLan: 4}

	rec := httptest.NewRecorder()
	srv.rememberBrowse(rec, browseData{Mode: modeSongs, LanType: 4, Letter: "S"})
	cookie := rec.Result().Cookies()[0]
	require.Positive(t, cookie.MaxAge, "a chosen letter is worth remembering, so the cookie must not clear itself")

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	assert.Equal(t, "S", srv.recallBrowse(req).Letter)

	// Back to the whole alphabet, with everything else at its default, is
	// nothing worth storing.
	rec = httptest.NewRecorder()
	srv.rememberBrowse(rec, browseData{Mode: modeSongs, LanType: 4})
	assert.Negative(t, rec.Result().Cookies()[0].MaxAge, "the default state should clear the cookie")
}

// An empty list has to say why it is empty, and the letter is now one of the
// reasons. "Nothing matches “”" was what the old template produced once a
// letter could narrow a list with nothing typed.
func TestEmptyTextNamesTheLetter(t *testing.T) {
	cases := []struct {
		name string
		data browseData
		want string
	}{
		{"songs, letter only", browseData{Mode: modeSongs, Letter: "X"},
			"No songs start with X."},
		{"songs, digits", browseData{Mode: modeSongs, Letter: "0"},
			"No songs start with a digit."},
		{"songs, letter and text", browseData{Mode: modeSongs, Letter: "X", Query: "amor"},
			"No songs starting with X match “amor”."},
		{"artists, letter only", browseData{Mode: modeArtists, Letter: "Q"},
			"No artists start with Q."},
		{"artists, letter and text", browseData{Mode: modeArtists, Letter: "Q", Query: "beat"},
			"No artists starting with Q match “beat”."},

		// Unchanged from before the letter filter existed.
		{"songs, text only", browseData{Mode: modeSongs, Query: "zzz"},
			"Nothing matches “zzz”."},
		{"artists, text only", browseData{Mode: modeArtists, Query: "zzz"},
			"No artists match “zzz”."},
		{"empty folder", browseData{Mode: modeFavorites, FolderID: 7},
			"This folder is empty. Star a song to file it here."},
		{"no folders", browseData{Mode: modeFavorites}, "No folders yet."},
		{"folder search", browseData{Mode: modeFavorites, Query: "rock"},
			"No folders match “rock”."},

		// The letter is remembered across a visit into an artist or a folder
		// but is not applied there, so it must not be blamed for the emptiness
		// either. Saying "no songs starting with S" on a page that is not
		// filtered by S sends the reader to clear a filter that is not on.
		{"letter remembered but not in force, inside an artist",
			browseData{Mode: modeSongs, ArtistID: 42, Letter: "S", Query: "zzz"},
			"Nothing matches “zzz”."},
		{"letter remembered but not in force, inside a folder",
			browseData{Mode: modeFavorites, FolderID: 7, Letter: "S", Query: "zzz"},
			"Nothing matches “zzz”."},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, c.data.EmptyText(), "%s", c.name)
	}
}
