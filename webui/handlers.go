package webui

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/i18n"
	"github.com/rrgmc/ck-karaoke/protocol"
)

// pageData is what every full page template receives.
type pageData struct {
	// Lang is the language the page is rendered in. page sets it from the
	// request.
	Lang       i18n.Lang
	Title      string
	Nav        string
	Online     bool
	DeviceAddr string
	// QueueCount is what the tab badge shows on a full page render; after that
	// it is kept current over the event stream.
	QueueCount int
	// OfflineReason explains why the device is unavailable, so the banner can
	// say "busy" rather than a generic "offline".
	OfflineReason string
	Catalogue     catalog.Info

	Player playerData
	Browse browseData
	Queue  queueData
	Sync   syncData
	Backup backupData
}

// playerData drives the Now tab and the SSE fragment.
type playerData struct {
	Online   bool
	HasSong  bool
	Title    string
	Artist   string
	SongID   string
	Position string
	Next     string
	Key      int
	Tempo    int
	Volume   int
	Mic      int
	Echo     int
	Lyrics   []string
}

// Browse modes.
const (
	modeSongs   = "songs"
	modeArtists = "artists"
	// modeFavorites lists folders, and drilling into one lists its songs —
	// structurally the same shape as artists, which is why it reuses the same
	// machinery rather than being a page of its own.
	modeFavorites = "favorites"
)

// browseData drives the catalogue page in both its modes.
type browseData struct {
	// Lang is the language of the sentences built here, such as EmptyText.
	Lang    i18n.Lang
	Mode    string
	Query   string
	LanType int
	// Letter narrows the list to one initial: "A".."Z", or "0" for the titles
	// that start with a digit. Empty is the whole alphabet.
	//
	// Navigation state rather than a preference, so it travels in the URL and
	// the browse cookie beside the search text, not in a cookie of its own like
	// the ⋯ toggle.
	Letter   string
	ArtistID int
	// Artist is set when browsing one performer's songs.
	Artist    catalog.Artist
	Languages []catalog.Language

	Songs   []catalog.Song
	Artists []catalog.Artist

	// FolderID and Folder are set when browsing inside one favorites folder,
	// mirroring ArtistID and Artist.
	FolderID int
	Folder   favorites.Folder
	Folders  []favorites.Folder

	// Favorited holds the ids on this page that are in at least one folder, so
	// the star renders filled. Populated in one query per page rather than one
	// per row.
	Favorited map[int]bool

	Total      int
	Offset     int
	Limit      int
	NextOffset int
	HasMore    bool

	// SongQuery and FavQuery are the two remembered search texts. The favorites
	// tab searches folder names, so a song title typed on another tab means
	// nothing there and vice versa — each side of that divide keeps its own, and
	// switching between them swaps the box rather than carrying text across.
	// Query is whichever of the two is currently in the box.
	SongQuery string
	FavQuery  string

	// ShowExtraActions reveals "play next" and "play now" on each row. They are
	// hidden by default because both cut ahead of the reserved list, and a row
	// of a search result is an easy thing to mistap — one guest can reorder
	// everyone else's evening without meaning to. Queueing stays available.
	ShowExtraActions bool

	// LangChoice is the language picked on this device for the app itself, or
	// "" to follow the system. Not to be confused with Languages, the song
	// languages of the catalogue.
	LangChoice i18n.Lang
}

// ShowingArtists reports whether the paging list holds artists. Drilling into an
// artist shows songs, so the mode alone is not enough to decide.
func (b browseData) ShowingArtists() bool { return b.Mode == modeArtists && b.ArtistID == 0 }

// QueryFor returns the search text belonging to a mode.
func (b browseData) QueryFor(mode string) string {
	if mode == modeFavorites {
		return b.FavQuery
	}
	return b.SongQuery
}

// ShowingFolders reports whether the list holds folders rather than songs.
func (b browseData) ShowingFolders() bool { return b.Mode == modeFavorites && b.FolderID == 0 }

// InFolder reports whether one favorites folder is open. Sync belongs here too,
// and knowing which folder means it can go straight to that folder's code
// instead of asking again for the one already on screen.
func (b browseData) InFolder() bool { return b.Mode == modeFavorites && b.FolderID != 0 }

// UsesLanguage reports whether the language filter applies to this list.
//
// It does not to favorites. A song is in a folder because someone put it there,
// so hiding it because a language was chosen on another tab is surprising — and
// the folder list could not honour the filter anyway, since the favorites
// database holds song ids and nothing else. The counts then disagreed: a row
// saying twelve songs opened showing three.
func (b browseData) UsesLanguage() bool { return b.Mode != modeFavorites }

// UsesLetter reports whether the initial-letter filter belongs here: the two
// long lists, and only at their top level.
//
// Those are the ones an alphabet helps with — eleven thousand songs and three
// and a half thousand artists. One performer's songs and one folder's contents
// are already short enough to read, and the folder list never reaches the
// catalogue at all, so it has no AlphaIndex to filter on.
func (b browseData) UsesLetter() bool {
	if b.ArtistID != 0 || b.FolderID != 0 {
		return false
	}
	return b.Mode == modeSongs || b.Mode == modeArtists
}

// activeLetter is the letter actually narrowing this list, which is not the
// same as the one being remembered: the choice survives a visit into an artist
// or a folder, where it does not apply. Anything explaining the list to the
// reader has to ask this rather than reading Letter, or an empty artist page
// blames a letter that was never in force.
func (b browseData) activeLetter() string {
	if !b.UsesLetter() {
		return ""
	}
	return b.Letter
}

// letterPhrase names the active bucket the way a sentence needs it. Only the
// digit bucket differs from the value itself, and "0" is not what it means.
func (b browseData) letterPhrase() string {
	if b.activeLetter() == "0" {
		return b.Lang.T("a digit")
	}
	return b.activeLetter()
}

// IsFavorite reports whether a song is filed in any folder.
func (b browseData) IsFavorite(id int) bool { return b.Favorited[id] }

// Is reports whether this is the named mode, for the toggle's active state.
func (b browseData) Is(mode string) bool { return b.Mode == mode }

// Placeholder adapts the search box to what is being listed.
func (b browseData) Placeholder() string {
	l := b.Lang
	if b.ArtistID != 0 {
		return l.T("Search within %s", b.Artist.Name)
	}
	if b.FolderID != 0 {
		return l.T("Search within %s", b.Folder.Name)
	}
	switch b.Mode {
	case modeArtists:
		return l.T("Search artists")
	case modeFavorites:
		return l.T("Search folders")
	default:
		return l.T("Song title or number")
	}
}

// CountText is the match count, shown beside the language filter rather than on
// a line of its own — it is one short phrase and was costing a whole row.
func (b browseData) CountText() string {
	switch {
	case b.ShowingArtists():
		return b.Lang.N(b.Total, "%d artist", "%d artists")
	case b.ShowingFolders():
		return b.Lang.N(b.Total, "%d folder", "%d folders")
	}
	return b.Lang.N(b.Total, "%d song", "%d songs")
}

// IsEmpty reports whether the list has no rows, whichever of the three kinds
// of row it is showing.
func (b browseData) IsEmpty() bool {
	switch {
	case b.ShowingFolders():
		return len(b.Folders) == 0
	case b.ShowingArtists():
		return len(b.Artists) == 0
	}
	return len(b.Songs) == 0
}

// EmptyText is what a list with no rows says instead.
//
// Assembled here rather than branched in the template, for the same reason
// CountText and Placeholder are: it is a sentence, and once the letter filter
// joined the search box there were three kinds of list and four reasons each
// could come up empty. Nested {{if}}s that deep stop being readable, and a
// sentence is the one thing worth testing directly.
func (b browseData) EmptyText() string {
	l, q := b.Lang, b.Query
	letter := b.activeLetter()
	switch {
	case b.ShowingFolders():
		if q != "" {
			return l.T("No folders match “%s”.", q)
		}
		return l.T("No folders yet.")

	case b.ShowingArtists():
		switch {
		case q != "" && letter != "":
			return l.T("No artists starting with %s match “%s”.", b.letterPhrase(), q)
		case q != "":
			return l.T("No artists match “%s”.", q)
		case letter != "":
			return l.T("No artists start with %s.", b.letterPhrase())
		}
		// Only a language filter can empty the whole artist list, and saying so
		// would be guessing at the cause.
		return l.T("No artists here.")

	// An empty folder is worth its own words: it is the one case with an
	// obvious next step rather than a filter to loosen.
	case b.FolderID != 0 && q == "":
		return l.T("This folder is empty. Star a song to file it here.")

	case q != "" && letter != "":
		return l.T("No songs starting with %s match “%s”.", b.letterPhrase(), q)
	case q != "":
		return l.T("Nothing matches “%s”.", q)
	case letter != "":
		return l.T("No songs start with %s.", b.letterPhrase())
	}
	return l.T("No songs here.")
}

// QueryString rebuilds the browse URL for paging and mode links.
func (b browseData) QueryString(offset int) string {
	v := url.Values{}
	if b.Mode != "" {
		v.Set("mode", b.Mode)
	}
	if b.Query != "" {
		v.Set("q", b.Query)
	}
	if b.LanType != 0 {
		v.Set("lan", strconv.Itoa(b.LanType))
	}
	if b.Letter != "" {
		v.Set("letter", b.Letter)
	}
	if b.ArtistID != 0 {
		v.Set("artist", strconv.Itoa(b.ArtistID))
	}
	if b.FolderID != 0 {
		v.Set("folder", strconv.Itoa(b.FolderID))
	}
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	return v.Encode()
}

type queueData struct {
	Entries []queueEntry
	Total   int
	// Offline is true when there is no session to ask for the list.
	Offline bool
	// Pending is true while no list has arrived yet, so the page can say it is
	// loading rather than claim the queue is empty.
	Pending bool
}

type queueEntry struct {
	Position int
	SongID   string
	// Title is empty for a song the catalogue does not know; the template
	// names it by number instead.
	Title  string
	Artist string
	// By names the client that reserved it, which the device records per entry.
	By string
}

// bannerData drives the connection strip at the top of every page.
type bannerData struct {
	Online     bool
	DeviceAddr string
	Reason     string
}

func (s *Server) playerData(st *protocol.Status) playerData {
	d := playerData{Online: s.dev.Online()}
	if st == nil {
		return d
	}
	d.Title, d.Artist, d.SongID = st.CurrentSong, "", st.CurrentSongID
	d.Next = st.NextSong
	d.Position = formatDuration(st.SongPosition)
	d.Key, d.Tempo, d.Volume, d.Mic, d.Echo = st.Key, st.Tempo, st.MusicVolume, st.MicVolume, st.Echo
	d.Lyrics = st.Lyrics
	d.HasSong = st.CurrentSong != ""

	// The status frame carries the song name but not the artist, so fill it in
	// from the catalogue.
	if s.songs != nil && st.CurrentSongID != "" {
		if id, err := strconv.Atoi(st.CurrentSongID); err == nil {
			if song, err := s.songs.Song(context.Background(), id); err == nil {
				d.Artist = song.Artist
				if d.Title == "" {
					d.Title = song.Title
				}
			}
		}
	}
	return d
}

func (s *Server) now(w http.ResponseWriter, r *http.Request) {
	s.page(w, r, "now.html", pageData{
		Title:  langOf(r).T("Now Playing"),
		Player: s.playerData(s.dev.Status()),
	})
}

func (s *Server) playerFragment(w http.ResponseWriter, r *http.Request) {
	// Same reasoning as the browse fragments: reached from the address bar,
	// these are a dead end rather than a page.
	if !isHTMX(r) {
		http.Redirect(w, r, "/now", http.StatusSeeOther)
		return
	}
	s.fragment(w, r, "_player.html", s.playerData(s.dev.Status()))
}

// isHTMX reports whether the request came from htmx rather than the address bar.
func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// reset returns to a clean home page.
//
// Navigation state goes — the search text, the mode, whichever artist or folder
// was open — while preferences stay: the language, the sort, and whether the
// queue-skipping actions are shown. Those are choices about how the app works,
// not about where you happen to be standing in it.
//
// It redirects rather than swapping, so the browser reloads in full and opens a
// new event stream. That is the other half of "refresh everything" after a page
// has been open all evening.
func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	prev := s.recallBrowse(r)
	s.rememberBrowse(w, browseData{Mode: modeSongs, LanType: prev.LanType})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// browseURL is the address the catalogue should show for this state — the same
// parameters minus the fragment plumbing.
func browseURL(b browseData) string {
	if qs := b.QueryString(0); qs != "" {
		return "/?" + qs
	}
	return "/"
}

// browseCookie remembers where you were in the catalogue, so leaving the tab and
// coming back does not throw away the search or the mode.
const browseCookie = "ck_browse"

// actionsCookie remembers whether the queue-skipping actions are revealed.
//
// It is deliberately separate from browseCookie, which clears itself whenever
// the browse state returns to its default — that would take this preference
// with it. Being a cookie also makes it per-device, which is the point: the
// phone that sets it keeps the extra actions, and every other phone that opens
// the page gets the safe default.
const actionsCookie = "ck_actions"

func rememberShowActions(w http.ResponseWriter, on bool) {
	c := &http.Cookie{
		Name:     actionsCookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if on {
		c.Value = "1"
		c.MaxAge = int((365 * 24 * time.Hour).Seconds())
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func recallShowActions(r *http.Request) bool {
	c, err := r.Cookie(actionsCookie)
	return err == nil && c.Value == "1"
}

// langCookie holds the language chosen on this device, overriding the one the
// browser asks for. Without it the app follows the system language.
const langCookie = "ck_lang"

// recallLang returns the language chosen on this device, or "" when none is,
// or the cookie names a language the app no longer has.
func recallLang(r *http.Request) i18n.Lang {
	c, err := r.Cookie(langCookie)
	if err != nil {
		return ""
	}
	if lang := i18n.Lang(c.Value); slices.Contains(i18n.Supported, lang) {
		return lang
	}
	return ""
}

// setLanguage stores the language picked on the folder list, or clears the
// choice for an empty value, then reloads the page. The reload is what makes
// the switch complete: the page and its event stream both reopen in the new
// language.
func (s *Server) setLanguage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.Lang(r.FormValue("lang"))
	if lang != "" && !slices.Contains(i18n.Supported, lang) {
		http.Error(w, "unknown language", http.StatusBadRequest)
		return
	}
	c := &http.Cookie{
		Name:     langCookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if lang != "" {
		c.Value = string(lang)
		c.MaxAge = int((365 * 24 * time.Hour).Seconds())
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// rememberBrowse stores the browse state, or clears it when there is nothing
// worth restoring.
func (s *Server) rememberBrowse(w http.ResponseWriter, b browseData) {
	c := &http.Cookie{
		Name:     browseCookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if b.SongQuery == "" && b.FavQuery == "" && b.LanType == s.defaultLan && b.Letter == "" &&
		b.ArtistID == 0 && b.FolderID == 0 && b.Mode == modeSongs {
		c.MaxAge = -1
	} else {
		// Not QueryString: that builds addresses, and the parked query has no
		// business appearing in one.
		v := url.Values{}
		v.Set("mode", b.Mode)
		if b.SongQuery != "" {
			v.Set("q", b.SongQuery)
		}
		if b.FavQuery != "" {
			v.Set("fq", b.FavQuery)
		}
		// Always written, even as 0: zero means "all languages", which is a
		// choice, and leaving the key out would make it indistinguishable from
		// never having chosen — so picking All would silently revert.
		v.Set("lan", strconv.Itoa(b.LanType))
		// The letter needs none of that care: there is no default to revert to,
		// so empty is simply the whole alphabet.
		if b.Letter != "" {
			v.Set("letter", b.Letter)
		}
		if b.ArtistID != 0 {
			v.Set("artist", strconv.Itoa(b.ArtistID))
		}
		if b.FolderID != 0 {
			v.Set("folder", strconv.Itoa(b.FolderID))
		}
		c.Value = v.Encode()
		c.MaxAge = int((30 * 24 * time.Hour).Seconds())
	}
	http.SetCookie(w, c)
}

// recallBrowse restores the remembered browse state.
func (s *Server) recallBrowse(r *http.Request) browseData {
	out := browseData{Mode: modeSongs, LanType: s.defaultLan}
	c, err := r.Cookie(browseCookie)
	if err != nil {
		return out
	}
	v, err := url.ParseQuery(c.Value)
	if err != nil {
		return out
	}
	switch v.Get("mode") {
	case modeArtists:
		out.Mode = modeArtists
	case modeFavorites:
		out.Mode = modeFavorites
	}
	out.SongQuery, out.FavQuery = v.Get("q"), v.Get("fq")
	out.Query = out.QueryFor(out.Mode)
	// Present but zero means All was chosen; absent means never chosen.
	if v.Has("lan") {
		out.LanType = atoiDefault(v.Get("lan"), 0)
	}
	out.Letter = normalizeLetter(v.Get("letter"))
	out.ArtistID = atoiDefault(v.Get("artist"), 0)
	out.FolderID = atoiDefault(v.Get("folder"), 0)
	return out
}

// normalizeLetter accepts only what the letter select can produce, so a
// hand-edited URL or a stale cookie cannot put anything else into the state
// that gets echoed back into the page. Validation is catalog's, since it owns
// the bucket numbering.
func normalizeLetter(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if catalog.AlphaIndexFor(s) == 0 {
		return ""
	}
	return s
}

// letterParam reads the letter select.
//
// Present but empty means A–Z was chosen; absent means the control was not on
// screen to be sent, so the remembered choice stands. That is the same
// distinction the language filter draws, and it is needed here for a sharper
// version of the same reason: the select is missing from the favorites list, so hx-include has nothing to pick up there, and switching away and
// back would drop a letter the reader never unset.
func letterParam(q url.Values, remembered string) string {
	if !q.Has("letter") {
		return remembered
	}
	return normalizeLetter(q.Get("letter"))
}

// browse is the catalogue: every song by default, with a search box, and a
// toggle to list artists instead. Choosing an artist lists their songs.
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Recalled first so it can supply the fallbacks below.
	prev := s.recallBrowse(r)

	data := browseData{
		Mode:  modeSongs,
		Query: q.Get("q"),
		// Absent means "unchanged", not "back to the default". A URL that names
		// a mode but no language — a bookmark, or a control that does not carry
		// one — would otherwise reset a Portuguese session to English.
		LanType:  atoiDefault(q.Get("lan"), prev.LanType),
		Letter:   letterParam(q, prev.Letter),
		ArtistID: atoiDefault(q.Get("artist"), 0),
		FolderID: atoiDefault(q.Get("folder"), 0),
		Offset:   atoiDefault(q.Get("offset"), 0),
		Limit:    50,
	}
	switch q.Get("mode") {
	case modeArtists:
		data.Mode = modeArtists
	case modeFavorites:
		data.Mode = modeFavorites
	}
	// A folder id only means anything in favorites mode, and arriving with one
	// is how the folder rows link in.
	if data.FolderID != 0 {
		data.Mode = modeFavorites
	}

	// The two search boxes are kept apart. Switching between the favorites tab
	// and the other three swaps the text for that side's own, because the mode
	// toggle sends whatever is currently typed and folder names and song titles
	// are not interchangeable — arriving at favorites with a song title
	// searched folders for it and found none.
	if from := q.Get("from"); from != "" && (from == modeFavorites) != (data.Mode == modeFavorites) {
		data.Query = prev.QueryFor(data.Mode)
	}
	data.SongQuery, data.FavQuery = prev.SongQuery, prev.FavQuery
	if data.Mode == modeFavorites {
		data.FavQuery = data.Query
	} else {
		data.SongQuery = data.Query
	}

	// No parameters at all means the tab bar was tapped rather than a search
	// submitted, so restore where you left off.
	if len(q) == 0 {
		data = prev
		data.Limit = 50
	} else {
		s.rememberBrowse(w, data)
	}

	// Read after the recall above, which builds a fresh browseData and would
	// otherwise drop the preference. The list fragments read it from the cookie
	// too, so "load more" keeps whatever the page was showing without having to
	// carry it in every URL.
	data.ShowExtraActions = recallShowActions(r)
	if v := q.Get("actions"); v != "" {
		data.ShowExtraActions = v == "1"
		rememberShowActions(w, data.ShowExtraActions)
	}
	data.Lang = langOf(r)
	data.LangChoice = recallLang(r)

	if s.songs == nil {
		s.page(w, r, "browse.html", pageData{Title: data.title(), Browse: data})
		return
	}

	langs, err := s.songs.Languages(r.Context())
	if err != nil {
		s.logger.Error("listing languages", "error", err)
	}
	data.Languages = langs

	if data.ArtistID != 0 {
		if a, err := s.songs.Artist(r.Context(), data.ArtistID); err == nil {
			data.Artist = a
		} else {
			// The artist went away; fall back to the full list rather than an error.
			s.logger.Warn("unknown artist", "id", data.ArtistID, "error", err)
			data.ArtistID = 0
		}
	}

	if data.FolderID != 0 {
		if f, err := s.favs.Folder(r.Context(), data.FolderID); err == nil {
			data.Folder = f
		} else {
			// Another phone may have deleted it while this one was looking at
			// it. Fall back to the folder list rather than an error.
			s.logger.Warn("unknown folder", "id", data.FolderID, "error", err)
			data.FolderID = 0
		}
	}

	if err := s.loadBrowse(r, &data); err != nil {
		s.fail(w, r, err)
		return
	}

	// htmx asks for a fragment when typing (the list), paging (just the next
	// rows, which the Load more button swaps in place of itself), or switching
	// mode (the whole browse block, so the toggle and placeholder update too).
	// Honour ?fragment= only for htmx. The search box pushes its URL into
	// history, and that URL carries fragment=list, so a refresh or a back
	// button lands here as an ordinary navigation asking for a bare fragment —
	// a page with no layout and no way out of it. Treating those as a normal
	// page request makes any such URL, including ones already sitting in
	// somebody's history, render properly again.
	fragment := q.Get("fragment")
	if !isHTMX(r) {
		fragment = ""
	}

	switch fragment {
	case "browse":
		s.fragment(w, r, "_browsepage.html", pageData{Lang: data.Lang, Browse: data, Catalogue: s.catalogue()})
		return
	case "list":
		// Push the clean browse URL rather than letting htmx push the request
		// URL it just built, which is the one carrying fragment=list. The
		// header wins over hx-push-url, and it must be set before the body.
		w.Header().Set("HX-Push-Url", browseURL(data))
		s.fragment(w, r, "_browse.html", data)
		// The count lives up in the filter row, outside the swapped list, so it
		// is updated out of band.
		fmt.Fprintf(w, `<span id="count" class="result-count" hx-swap-oob="true">%s</span>`,
			template.HTMLEscapeString(data.CountText()))
		return
	case "rows":
		s.fragment(w, r, "_rows.html", data)
		return
	}
	s.page(w, r, "browse.html", pageData{Title: data.title(), Browse: data})
}

func (b browseData) title() string {
	if b.ArtistID != 0 {
		return b.Artist.Name
	}
	if b.FolderID != 0 {
		return b.Folder.Name
	}
	switch b.Mode {
	case modeArtists:
		return b.Lang.T("Artists")
	case modeFavorites:
		return b.Lang.T("Favorites")
	default:
		return b.Lang.T("Songs")
	}
}

// matchFolders filters the folder list by the search box. Folders are few
// enough that this is cheaper than a query, and it keeps folder search
// case-insensitive like everything else.
func matchFolders(folders []favorites.Folder, query string) []favorites.Folder {
	query = strings.TrimSpace(query)
	if query == "" {
		return folders
	}
	var out []favorites.Folder
	for _, f := range folders {
		if strings.Contains(strings.ToUpper(f.Name), strings.ToUpper(query)) {
			out = append(out, f)
		}
	}
	return out
}

// loadBrowse fills in whichever list the current mode calls for.
func (s *Server) loadBrowse(r *http.Request, data *browseData) error {
	q := catalog.Query{
		Text: data.Query, LanType: data.LanType, ArtistID: data.ArtistID,
		Offset: data.Offset, Limit: data.Limit,
		// A song search never widens to the performer's name: the Artists mode
		// does that, and inside one artist it would match every one of their
		// songs regardless of the query.
		MatchArtist: false,
	}

	// Inside a folder the catalogue does the searching, filtering and paging —
	// it just works over the set of ids the favorites database supplies. Note
	// the empty slice is preserved rather than normalised to nil: an empty
	// folder must find nothing, not everything.
	if data.FolderID != 0 {
		ids, err := s.favs.SongIDs(r.Context(), data.FolderID)
		if err != nil {
			return err
		}
		if ids == nil {
			ids = []int{}
		}
		q.IDs = ids
	}

	// Favorites ignore the language filter. data.LanType is deliberately left
	// alone so the choice survives the visit and is still there on the way back
	// to Songs; only the query drops it.
	if !data.UsesLanguage() {
		q.LanType = 0
	}

	// The letter, on the same terms and for the same reason: activeLetter is
	// empty wherever the control is not on screen, so a list can never be
	// narrowed by something with no visible switch. data.Letter itself is left
	// alone like data.LanType, so the choice is still there when you come back
	// out of an artist.
	q.Alpha = catalog.AlphaIndexFor(data.activeLetter())

	var n int
	switch {
	case data.ShowingFolders():
		folders, err := s.favs.Folders(r.Context())
		if err != nil {
			return err
		}
		folders = matchFolders(folders, data.Query)
		data.Folders, data.Total, n = folders, len(folders), len(folders)

	case data.ShowingArtists():
		artists, total, err := s.songs.SearchArtists(r.Context(), q)
		if err != nil {
			return err
		}
		data.Artists, data.Total, n = artists, total, len(artists)

	default:
		songs, total, err := s.songs.Search(r.Context(), q)
		if err != nil {
			return err
		}
		data.Songs, data.Total, n = songs, total, len(songs)

		ids := make([]int, len(songs))
		for i, sg := range songs {
			ids[i] = sg.ID
		}
		if data.Favorited, err = s.favs.FavoritedIDs(r.Context(), ids); err != nil {
			return err
		}
	}

	data.NextOffset = data.Offset + n
	data.HasMore = !data.ShowingFolders() && data.NextOffset < data.Total
	return nil
}

func (s *Server) queuePage(w http.ResponseWriter, r *http.Request) {
	s.page(w, r, "queue.html", pageData{Title: langOf(r).T("Queue"), Queue: s.cachedQueue(r.Context())})
	s.refreshQueue()
}

func (s *Server) queueFragment(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/queue", http.StatusSeeOther)
		return
	}
	s.fragment(w, r, "_queue.html", s.cachedQueue(r.Context()))
	s.refreshQueue()
}

// cachedQueue renders whatever list the device last sent, without asking for a
// new one.
//
// Querying takes about two seconds — the device seems to emit the reserved list
// on its own cadence rather than on demand — so blocking a page load on it made
// the Queue tab feel broken. It pushes the list unprompted as well, so the cache
// is usually current anyway.
func (s *Server) cachedQueue(ctx context.Context) queueData {
	if !s.dev.Online() {
		return queueData{Offline: true}
	}
	q := s.dev.LastQueue()
	if q == nil {
		return queueData{Pending: true}
	}
	return s.describeQueue(ctx, q)
}

// refreshQueue asks the device for the list in the background and publishes it
// when it arrives, so the page updates itself rather than making the reader wait.
func (s *Server) refreshQueue() {
	if !s.refreshing.CompareAndSwap(false, true) {
		return // one in flight is enough
	}
	go func() {
		defer s.refreshing.Store(false)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := s.dev.Do(func(c *client.Client) error {
			_, err := c.Queue(ctx, 0, 50)
			return err
		})
		if err != nil {
			s.logger.Debug("queue refresh failed", "error", err)
			return
		}
		// The reply arrives through OnQueue, which publishes it.
	}()
}

// PublishQueue pushes a new reserved list to every connected browser.
func (s *Server) PublishQueue(q *protocol.Queue) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.publish(eventQueue, "_queue.html", s.describeQueue(ctx, q))
	s.publish(eventQueueCount, "_queuecount.html", q.Total)
}

// describeQueue joins the device's song ids against the catalogue, since the
// reserved list carries nothing but numbers.
func (s *Server) describeQueue(ctx context.Context, q *protocol.Queue) queueData {
	out := queueData{Total: q.Total}

	ids := make([]int, 0, len(q.Entries))
	for _, e := range q.Entries {
		if id, err := strconv.Atoi(e.SongID); err == nil {
			ids = append(ids, id)
		}
	}
	var titles map[int]catalog.Song
	if s.songs != nil {
		titles, _ = s.songs.Songs(ctx, ids)
	}

	for i, e := range q.Entries {
		entry := queueEntry{Position: i + 1, SongID: e.SongID, By: e.QueuedBy()}
		if id, err := strconv.Atoi(e.SongID); err == nil {
			if song, ok := titles[id]; ok {
				entry.Title, entry.Artist = song.Title, song.Artist
			}
		}
		out.Entries = append(out.Entries, entry)
	}
	return out
}

// interactiveTimeout bounds a single button press.
//
// A command normally round-trips in about 250ms, so anything past this is the
// device not answering. The cost of waiting longer is not just a slow button: a
// browser opens only about six connections per host and the event stream holds
// one of them permanently, so a handful of clicks at an unresponsive device ties
// up the rest. Once they are gone even a plain tab-bar navigation has to wait
// for one to free, which is why the whole UI appeared to lock up rather than
// just the button that was pressed.
const interactiveTimeout = 3 * time.Second

// deviceCtx bounds a device command and detaches it from the browser request.
//
// Detaching matters: tying the command to r.Context() means navigating away, or
// htmx replacing an in-flight request, cancels a command the user asked for. A
// press of Play should reach the device whether or not its reply is still
// wanted.
func deviceCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), interactiveTimeout)
}

// control handles the transport and audio buttons. Every action returns the
// refreshed player fragment so the UI reflects the device, not a guess.
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	amount := atoiDefault(r.FormValue("v"), 0)

	ctx, cancel := deviceCtx()
	defer cancel()

	err := s.dev.Do(func(c *client.Client) error {
		switch action {
		case "pause":
			return c.Pause(ctx)
		case "stop":
			return c.Stop(ctx)
		case "next":
			return c.NextSong(ctx)
		case "skip":
			return c.Skip(ctx)
		case "repeat":
			return c.Repeat(ctx)
		case "key":
			return c.SetKey(ctx, amount)
		case "tempo":
			return c.SetTempo(ctx, amount)
		case "reset-key":
			return c.ResetKey(ctx)
		case "reset-tempo":
			return c.ResetTempo(ctx)
		case "volume":
			return c.SetVolume(ctx, amount)
		case "mic":
			return c.SetMicVolume(ctx, atoiDefault(r.FormValue("mic"), 1), amount)
		case "reverb":
			return c.SetReverb(ctx, amount)
		case "melody":
			return c.SetMelody(ctx)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	})
	// A missing echo is not a failure the user should see. The command was sent;
	// the device acknowledges most of them but not always, and it ignores key
	// and tempo outright when nothing is playing. Returning an error here left
	// the button looking dead, because htmx does not swap on a non-2xx response.
	// Only a genuinely broken connection is worth reporting.
	if err != nil && !isEchoTimeout(err) {
		s.fail(w, r, err)
		return
	}
	if err != nil {
		s.logger.Debug("no echo for control command", "action", action, "error", err)
	}

	// Give the device a moment to report the new state before re-rendering.
	time.Sleep(150 * time.Millisecond)
	s.fragment(w, r, "_player.html", s.playerData(s.dev.Status()))
}

// isEchoTimeout reports whether the device simply never acknowledged a command,
// as opposed to the connection being down.
func isEchoTimeout(err error) bool {
	if err == nil {
		return false
	}
	// interactiveTimeout is shorter than the client's own echo timer, so in
	// practice the deadline is what fires; match it directly rather than relying
	// on the wording that carries it.
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "timed out waiting for echo")
}

// songAction queues or plays a song.
func (s *Server) songAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	if _, err := strconv.Atoi(id); err != nil {
		s.fail(w, r, fmt.Errorf("bad song id %q", id))
		return
	}
	lang := langOf(r)

	ctx, cancel := deviceCtx()
	defer cancel()

	err := s.dev.Do(func(c *client.Client) error {
		switch action {
		case "reserve":
			return c.Reserve(ctx, id)
		case "reserve-top":
			return c.ReserveFirst(ctx, id)
		case "play":
			return c.PlayNow(ctx, id)
		case "remove":
			return c.RemoveFromQueue(ctx, id)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	})

	// Same reasoning as the transport controls: a late echo must not leave the
	// button stuck. It is reported honestly though — the row says "sent" rather
	// than "queued", because without the acknowledgement we do not know.
	if err != nil && !isEchoTimeout(err) {
		s.fail(w, r, err)
		withToast(w, toastBad, errorText(lang, err))
		return
	}

	label, level := actionLabel(lang, action), toastGood
	acked := err == nil
	if !acked {
		s.logger.Debug("no echo for song command", "action", action, "song", id, "error", err)
		label, level = lang.T("sent"), toastInfo
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span class="badge text-bg-secondary">%s</span>`,
		template.HTMLEscapeString(label))

	// Queueing something changes the list, so pull a fresh one for the Queue tab.
	s.refreshQueue()

	title := lang.T("Song %s", id)
	if s.songs != nil {
		if n, err := strconv.Atoi(id); err == nil {
			if song, err := s.songs.Song(r.Context(), n); err == nil {
				title = song.Title
			}
		}
	}
	msg := actionMessage(lang, action, title)
	if !acked {
		msg = lang.T("%s (not acknowledged)", msg)
	}
	withToast(w, level, msg)
}

func actionLabel(lang i18n.Lang, action string) string {
	switch action {
	case "reserve":
		return lang.T("queued")
	case "reserve-top":
		return lang.T("next up")
	case "play":
		return lang.T("playing")
	case "remove":
		return lang.T("removed")
	}
	return lang.T("done")
}

func actionMessage(lang i18n.Lang, action, title string) string {
	switch action {
	case "reserve":
		return lang.T("Queued: %s", title)
	case "reserve-top":
		return lang.T("Playing next: %s", title)
	case "play":
		return lang.T("Now playing: %s", title)
	case "remove":
		return lang.T("Removed: %s", title)
	}
	return lang.T("Done: %s", title)
}

// stepperData and sliderData describe one control row.
//
// The two families behave differently: SetKey and SetTempo take a *delta*
// (sending -1 twice goes from 0 to -2), while the volume controls take an *absolute* level that the device snaps
// to a thirteen-position grid. Treating either like the other made the buttons
// jump erratically or do nothing.
type stepperData struct {
	Label, Action, Reset string
	Value, Up, Down      int
}

type sliderData struct {
	Label, Action, Mic string
	Value, Up, Down    int
}

// parseTemplates parses every template with the functions for one language.
func parseTemplates(lang i18n.Lang) (*template.Template, error) {
	return template.New("").Funcs(templateFuncs(lang)).ParseFS(templateFS, "templates/*.html")
}

func templateFuncs(lang i18n.Lang) template.FuncMap {
	return template.FuncMap{
		// t translates a message, {{t "Remove from %s" .Name}}; tn picks the
		// singular or plural by a count, {{tn .Songs "%d song" "%d songs"}}.
		// Both return plain text, which the template escapes as usual.
		"t":  lang.T,
		"tn": lang.N,
		// th is t for a message that carries markup of its own, such as
		// <strong>. The catalogue is trusted; the arguments are not, so they
		// are escaped before going in.
		"th": func(msg string, args ...any) template.HTML {
			for i, a := range args {
				args[i] = template.HTMLEscapeString(fmt.Sprint(a))
			}
			return template.HTML(lang.T(msg, args...))
		},
		"lang":  lang.Tag,
		"asset": assetURL,
		// The one URL that points outside the app. A helper rather than a
		// string built in the template, because the query has to be
		// percent-encoded and a template has no way to do that.
		"ytSearch": youtubeSearch,
		"star": func(id int, on, oob bool) starData {
			return starData{ID: id, On: on, OOB: oob}
		},
		"banner": func(online bool, addr, reason string) bannerData {
			return bannerData{Online: online, DeviceAddr: addr, Reason: reason}
		},
		// modes drives the browse toggle. An ordered slice of pairs keeps the
		// buttons in a fixed order, which a map would not.
		"modes": func() []struct{ Mode, Label string } {
			return []struct{ Mode, Label string }{
				{modeSongs, lang.T("Songs")},
				{modeArtists, lang.T("Artists")},
				{modeFavorites, "★"},
			}
		},
		// letters drives the initial-letter select, and is static for the same
		// reason modes is: a fixed, ordered set of options. Deliberately not
		// derived from the catalogue — the counts behind each letter shift with
		// the search box and the language, and the select is only re-rendered
		// when the whole browse block is, so anything computed here would
		// spend most of its life wrong.
		"letters": func() []struct{ Value, Label string } {
			out := make([]struct{ Value, Label string }, 0, 27)
			for c := 'A'; c <= 'Z'; c++ {
				out = append(out, struct{ Value, Label string }{string(c), string(c)})
			}
			// The device files every title beginning with a digit together, so
			// there is one bucket for all ten and "0" is its name.
			return append(out, struct{ Value, Label string }{"0", "0–9"})
		},
		"stepper": func(label, action string, value, lo, hi int, reset string) stepperData {
			// Key and tempo take a delta, so the buttons always carry ±1
			// regardless of where the control currently sits. They are still
			// disabled at the ends so a press cannot run past the range.
			up, down := 1, -1
			if value >= hi {
				up = 0
			}
			if value <= lo {
				down = 0
			}
			return stepperData{
				Label: label, Action: action, Reset: reset, Value: value,
				Up: up, Down: down,
			}
		},
		"slider": func(label, action string, value int, mic string) sliderData {
			// Volumes are absolute and quantised, so each button carries the
			// level one grid position away from what the device reports.
			return sliderData{
				Label: label, Action: action, Mic: mic, Value: value,
				Up:   protocol.VolumeNudge(value, +1),
				Down: protocol.VolumeNudge(value, -1),
			}
		},
		"add": func(a, b int) int { return a + b },
		"signed": func(n int) string {
			if n > 0 {
				return "+" + strconv.Itoa(n)
			}
			return strconv.Itoa(n)
		},
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
