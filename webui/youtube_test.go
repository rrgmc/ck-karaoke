package webui

import (
	"bytes"
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestYouTubeSearchBuildsTheQuery(t *testing.T) {
	const prefix = "https://www.youtube.com/results?search_query="

	cases := []struct {
		name          string
		artist, title string
		want          string
	}{
		{"artist and title", "Beatles", "A DAY IN THE LIFE", prefix + "Beatles+-+A+DAY+IN+THE+LIFE"},
		// A catalogue row with no performer must not leave a dangling separator:
		// " - TITLE" is a worse query than the title on its own.
		{"no artist", "", "A DAY IN THE LIFE", prefix + "A+DAY+IN+THE+LIFE"},
		{"blank artist", "   ", "ESTRANHO", prefix + "ESTRANHO"},
		{"punctuation", "AC/DC", "T.N.T. (100%)", prefix + "AC%2FDC+-+T.N.T.+%28100%25%29"},
		{"accents", "Zezé Di Camargo", "É O AMOR", prefix + "Zez%C3%A9+Di+Camargo+-+%C3%89+O+AMOR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, youtubeSearch(c.artist, c.title))
		})
	}
}

// The href has to reach the browser as the builder wrote it. Where the action
// is the whole attribute, html/template runs urlNormalizer, which leaves valid
// %XX alone; spliced into a query string instead it would run urlEscaper, which
// re-encodes % and would turn the %28 of a bracket into %2528 — a search for
// literal percent signs. This is what catches that being introduced.
//
// The comparison is made after unescaping, because the last stage of the
// pipeline is htmlEscaper and it writes the + that QueryEscape uses for a space
// as &#43;. That is correct attribute serialisation and the browser undoes it
// before the URL is ever used; only the decoded value is worth asserting on.
func TestYouTubeHrefIsNotEscapedTwice(t *testing.T) {
	tmpl := templates(t)

	// A title carrying a literal % is the case that matters: it encodes to %25,
	// which is exactly what a second pass would go on to mangle.
	song := catalog.Song{ID: 2511, Title: "T.N.T. (100%)", Artist: "AC/DC"}
	var buf bytes.Buffer
	require.NoError(t, tmpl.ExecuteTemplate(&buf, "_rows.html", browseData{
		Mode: modeSongs, Songs: []catalog.Song{song},
	}))

	match := regexp.MustCompile(`href="(https://www\.youtube\.com[^"]*)"`).FindStringSubmatch(buf.String())
	require.Len(t, match, 2, "no YouTube link on the song row")
	href := html.UnescapeString(match[1])

	assert.Equal(t, youtubeSearch(song.Artist, song.Title), href)
	assert.NotContains(t, href, "%2528", "the query was escaped a second time")
}

// The reply to /song/{id}/{action} replaces the whole contents of #song-{id}
// with a badge, so a control placed in that div is gone the moment the song is
// queued. The star has always been in a div of its own for that reason; this
// asserts the YouTube link is on the same side of the line.
func TestYouTubeButtonSurvivesQueueing(t *testing.T) {
	tmpl := templates(t)

	var buf bytes.Buffer
	require.NoError(t, tmpl.ExecuteTemplate(&buf, "_rows.html", browseData{
		Mode:             modeSongs,
		ShowExtraActions: true,
		Songs:            []catalog.Song{{ID: 2511, Title: "A DAY IN THE LIFE", Artist: "Beatles"}},
	}))
	html := buf.String()

	swapped := strings.Index(html, `id="song-2511"`)
	require.GreaterOrEqual(t, swapped, 0, "the queueing div is missing")
	link := strings.Index(html, "https://www.youtube.com")
	require.GreaterOrEqual(t, link, 0, "the song row has no YouTube link")
	assert.Less(t, link, swapped,
		"the YouTube link is inside #song-2511, so queueing the song would wipe it")
}

// It takes nothing from anyone, so unlike play-next and play-now it is not
// behind the ⋯ toggle. Its URL must also carry no /song/ path, or it would
// satisfy the substring checks in TestSongRowsHideQueueSkippingActions and hide
// a real regression there.
func TestYouTubeButtonIsAlwaysOffered(t *testing.T) {
	tmpl := templates(t)

	for _, show := range []bool{false, true} {
		var buf bytes.Buffer
		require.NoError(t, tmpl.ExecuteTemplate(&buf, "_rows.html", browseData{
			Mode:             modeSongs,
			ShowExtraActions: show,
			Songs:            []catalog.Song{{ID: 2511, Title: "A DAY IN THE LIFE", Artist: "Beatles"}},
		}))
		assert.Contains(t, buf.String(), "https://www.youtube.com/results?search_query=",
			"no YouTube link with the ⋯ toggle %v", show)
	}
	assert.NotContains(t, youtubeSearch("Beatles", "PLAY"), "/song/")
}
