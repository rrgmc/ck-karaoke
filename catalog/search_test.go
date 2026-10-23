package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyDistinguishesGoodFromCorrupt(t *testing.T) {
	good := writeFixture(t)
	assert.NoError(t, Verify(good), "intact database rejected")

	// Overwrite the last pages, where the join table and the padding live. The
	// file still opens and its header is intact.
	b, err := os.ReadFile(good)
	require.NoError(t, err)
	for i := len(b) - 2048; i < len(b); i++ {
		b[i] = 0x5A
	}
	bad := filepath.Join(t.TempDir(), "corrupt.db")
	require.NoError(t, os.WriteFile(bad, b, 0o644))
	assert.Error(t, Verify(bad), "corrupt database accepted")
}

func TestSearch(t *testing.T) {
	db := openFixture(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		query Query
		want  int // a song id that must appear
	}{
		{"by title", Query{Text: "A DAY IN THE LIFE"}, songADayInTheLife},
		{"by title lowercase", Query{Text: "a day in the life"}, songADayInTheLife},
		{"by artist when asked", Query{Text: "Rush", MatchArtist: true}, songTomSawyer},
		{"by song number", Query{Text: "201"}, songTomSawyer},
		{"partial title", Query{Text: "SAWYER"}, songTomSawyer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			songs, total, err := db.Search(ctx, tc.query)
			require.NoError(t, err)
			require.NotZero(t, total, "no matches")
			assert.True(t, hasSong(songs, tc.want),
				"song %d not in the first %d of %d results", tc.want, len(songs), total)
		})
	}
}

// A song search must not quietly match the performer as well, or the Songs and
// Artists modes return the same rows.
func TestSearchArtistMatchingIsOptional(t *testing.T) {
	db := openFixture(t)
	ctx := context.Background()

	// "Rush" is a performer; none of their song titles contain the word.
	_, totalWith, err := db.Search(ctx, Query{Text: "Rush", MatchArtist: true})
	require.NoError(t, err)
	_, totalWithout, err := db.Search(ctx, Query{Text: "Rush"})
	require.NoError(t, err)
	assert.Equal(t, 2, totalWith)
	assert.Zero(t, totalWithout)
}

// Accented titles must be findable without typing the accents, which is the
// reason the schema carries a folded FirstWord column.
func TestSearchIgnoresAccents(t *testing.T) {
	db := openFixture(t)

	songs, _, err := db.Search(context.Background(), Query{Text: "coracao"})
	require.NoError(t, err)
	assert.True(t, hasSong(songs, songCoracao), `"coracao" did not find the accented title`)

	songs, _, err = db.Search(context.Background(), Query{Text: "coração"})
	require.NoError(t, err)
	assert.True(t, hasSong(songs, songCoracao), `"coração" did not find the accented title`)
}

func TestLanguagesAndInfo(t *testing.T) {
	db := openFixture(t)

	info := db.Info()
	assert.Equal(t, fixtureSongs, info.Songs)
	assert.Equal(t, fixtureSingers, info.Singers)

	langs, err := db.Languages(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, langs)
	var sum int
	for _, l := range langs {
		sum += l.Songs
	}
	assert.Equal(t, info.Songs, sum, "languages do not account for every song")
	assert.Equal(t, 1, db.LanTypeNamed(context.Background(), "english"))
	assert.Zero(t, db.LanTypeNamed(context.Background(), "Klingon"))
}

func TestBrowseAllSongs(t *testing.T) {
	db := openFixture(t)

	// A zero query lists the whole catalogue, which is what the browse page
	// shows before anything is typed.
	songs, total, err := db.Search(context.Background(), Query{})
	require.NoError(t, err)
	assert.Equal(t, fixtureSongs, total)
	assert.Len(t, songs, fixtureSongs)
}

func TestArtists(t *testing.T) {
	db := openFixture(t)
	ctx := context.Background()

	artists, total, err := db.SearchArtists(ctx, Query{})
	require.NoError(t, err)
	assert.Equal(t, fixtureSingers, total)
	for _, a := range artists {
		assert.NotZerof(t, a.Songs, "artist %q (%d) has no songs", a.Name, a.ID)
	}

	found, _, err := db.SearchArtists(ctx, Query{Text: "beatles"})
	require.NoError(t, err)
	require.Len(t, found, 1)

	// Drilling into an artist returns exactly that artist's songs, and the
	// count agrees with what the artist listing advertised.
	a := found[0]
	songs, total, err := db.Search(ctx, Query{ArtistID: a.ID, Limit: 200})
	require.NoError(t, err)
	assert.Equal(t, a.Songs, total)
	for _, s := range songs {
		assert.Equal(t, a.Name, s.Artist, "song %d", s.ID)
	}

	one, err := db.Artist(ctx, artistRush)
	require.NoError(t, err)
	assert.Equal(t, "Rush", one.Name)
	assert.Equal(t, 2, one.Songs)
}

// Browsing a favorites folder is a search restricted to a set of ids, so the
// restriction composes with the text and language filters rather than
// replacing them.
func TestSearchRestrictedToIDs(t *testing.T) {
	db := openFixture(t)
	ctx := context.Background()

	want := []int{songADayInTheLife, songTomSawyer, songAgua, song1999}
	songs, total, err := db.Search(ctx, Query{IDs: want, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, len(want), total)
	for _, id := range want {
		assert.Truef(t, hasSong(songs, id), "song %d missing from a search restricted to it", id)
	}

	// Combined with text: only the one whose title matches.
	one, total, err := db.Search(ctx, Query{IDs: want, Text: "A DAY IN THE LIFE", Limit: 50})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	assert.Equal(t, songADayInTheLife, one[0].ID)

	// An empty non-nil set means nothing matches. If this ever returns the
	// catalogue, opening an empty favorites folder shows every song.
	none, total, err := db.Search(ctx, Query{IDs: []int{}, Limit: 50})
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, none)

	// Nil means no restriction at all, which is every other caller.
	_, all, err := db.Search(ctx, Query{Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, fixtureSongs, all)
}

func TestSongsLookup(t *testing.T) {
	db := openFixture(t)
	got, err := db.Songs(context.Background(), []int{songHeyJude, songLimelight, 99999})
	require.NoError(t, err)
	assert.Len(t, got, 2, "a missing id is absent, not an error")
	assert.Equal(t, "HEY JUDE", got[songHeyJude].Title)
	assert.Equal(t, "English", got[songHeyJude].Language)
}

// AlphaIndexFor is the only place the 1..27 convention is written down, so it
// is worth pinning: the whole letter filter is wrong if this is.
func TestAlphaIndexFor(t *testing.T) {
	cases := map[string]int{
		"A": 1, "S": 19, "Z": 26,
		"a": 1, "z": 26,
		"0": alphaDigits,
		// Everything else is "no restriction" rather than an error, because it
		// arrives from a URL and a bad one must not filter the list to nothing.
		"": 0, "9": 0, "AB": 0, "#": 0, "Á": 0,
	}
	for in, want := range cases {
		assert.Equal(t, want, AlphaIndexFor(in), "AlphaIndexFor(%q)", in)
	}
}

// The letter filter narrows songs to one initial, and composes with the text
// and language filters rather than replacing them.
func TestSearchByLetter(t *testing.T) {
	db := openFixture(t)
	ctx := context.Background()

	songs, total, err := db.Search(ctx, Query{Alpha: AlphaIndexFor("A"), Limit: 200})
	require.NoError(t, err)
	assert.Equal(t, 5, total, "songs beginning with A")
	for _, s := range songs {
		assert.Truef(t, strings.HasPrefix(strings.ToUpper(foldAccents(s.Title)), "A"),
			"song %d %q is not in the A bucket", s.ID, s.Title)
	}

	// The digit bucket is one bucket for all ten, not ten buckets.
	digits, _, err := db.Search(ctx, Query{Alpha: AlphaIndexFor("0"), Limit: 200})
	require.NoError(t, err)
	require.Len(t, digits, 1)
	assert.Equal(t, song1999, digits[0].ID)

	// Composing with text: the letter bounds the search.
	within, withinTotal, err := db.Search(ctx, Query{Alpha: AlphaIndexFor("A"), Text: "azul", Limit: 200})
	require.NoError(t, err)
	assert.Equal(t, 2, withinTotal)
	assert.True(t, hasSong(within, songAzul))
	assert.True(t, hasSong(within, songAzulDaCor))

	// And with a language, the third filter in the same row.
	byLang, _, err := db.Search(ctx, Query{
		Alpha: AlphaIndexFor("A"), LanType: db.LanTypeNamed(ctx, "English"), Limit: 200})
	require.NoError(t, err)
	require.Len(t, byLang, 1)
	assert.Equal(t, songADayInTheLife, byLang[0].ID)
}

// Artists get the same filter, against TableSinger's own AlphaIndex.
func TestSearchArtistsByLetter(t *testing.T) {
	db := openFixture(t)

	artists, total, err := db.SearchArtists(context.Background(), Query{Alpha: AlphaIndexFor("Q"), Limit: 200})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, artists, 1)
	assert.Equal(t, artistQueen, artists[0].ID)
}

// A title that starts with punctuation is filed by its first letter, because
// the device strips punctuation when it builds FirstWord and AlphaIndex.
func TestLetterBucketsIgnorePunctuation(t *testing.T) {
	db := openFixture(t)
	ctx := context.Background()

	songs, _, err := db.Search(ctx, Query{Alpha: AlphaIndexFor("I"), Limit: 200})
	require.NoError(t, err)
	assert.True(t, hasSong(songs, songSatisfaction), "(I CAN'T GET NO) SATISFACTION is not under I")

	artists, _, err := db.SearchArtists(ctx, Query{Alpha: AlphaIndexFor("E"), Limit: 200})
	require.NoError(t, err)
	require.Len(t, artists, 1)
	assert.Equal(t, artistEarth, artists[0].ID, `"Earth, Wind And Fire" is not under E`)
}

// Ordering runs on FirstWord, which is accent-stripped, rather than on the
// display name. Under SQLite's default BINARY collation every accented
// character sorts after Z, so ordering by the display name would put "ÁGUA" at
// the end of its letter rather than among the AGs where a reader looks.
func TestOrderingIgnoresAccents(t *testing.T) {
	db := openFixture(t)

	ids := []int{songAzul, songAgua, songAzulDaCor, songAFlorDaPele}
	want := []string{"À FLOR DA PELE", "ÁGUA", "AZUL", "AZUL DA COR DO MAR"}

	songs, _, err := db.Search(context.Background(), Query{IDs: ids})
	require.NoError(t, err)
	require.Len(t, songs, len(ids))

	got := make([]string, len(songs))
	for i, s := range songs {
		got[i] = s.Title
	}
	assert.Equal(t, want, got)
}

func hasSong(songs []Song, id int) bool {
	for _, s := range songs {
		if s.ID == id {
			return true
		}
	}
	return false
}
