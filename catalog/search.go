package catalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Song is one catalogue entry. ID is what the device's commands take.
type Song struct {
	ID       int
	Title    string
	Artist   string
	Language string
	LanType  int
}

// Language is a catalogue language and how many songs it holds.
type Language struct {
	LanType int
	Name    string
	Songs   int
}

// Artist is a performer in the catalogue.
//
// Names are stored truncated to 24 characters, so long ones arrive clipped
// ("Zezé Di Camargo e Lucian"). That is the device's data, not a bug here.
type Artist struct {
	ID    int
	Name  string
	Songs int
}

// Query selects songs. A zero Query lists the whole catalogue.
type Query struct {
	// Text matches the song title. A bare number also matches the song id
	// exactly, which is how the physical remote is used.
	Text string
	// MatchArtist widens Text to the performer's name as well. Off by default so
	// that a song search is a song search — with it always on, searching songs
	// and searching everything returned the same thing.
	MatchArtist bool
	// LanType filters by language when non-zero.
	LanType int
	// Alpha restricts results to one initial-letter bucket, using the
	// catalogue's own AlphaIndex column: 1 is A, 26 is Z, and 27 is everything
	// beginning with a digit. Zero means no restriction. Use AlphaIndexFor to
	// turn a letter into one.
	Alpha int
	// ArtistID restricts results to one performer when non-zero.
	ArtistID int
	// IDs restricts results to a set of song ids, which is how a favorites
	// folder is browsed: the ids come from the favorites database, and the
	// catalogue supplies the titles, the text and language filtering, and the
	// paging.
	//
	// Nil means no restriction. An empty *non-nil* slice means the set is empty
	// and nothing can match — the distinction matters, because an empty folder
	// must show no songs rather than the whole catalogue.
	IDs    []int
	Offset int
	Limit  int
}

// Search returns matching songs and the total number of matches.
func (d *DB) Search(ctx context.Context, q Query) ([]Song, int, error) {
	// The 200 cap protects against an unbounded page of the whole catalogue.
	// A query already restricted to a set of ids is bounded by that set, so it
	// may ask for all of them.
	max := 200
	if q.IDs != nil {
		max = len(q.IDs) + 1
	}
	if q.Limit <= 0 || q.Limit > max {
		if q.Limit <= 0 {
			q.Limit = 50
		} else {
			q.Limit = max
		}
	}

	where, args := q.conditions()

	// Restricting to one artist needs the join table, which is authoritative:
	// it agrees with the denormalised TableSong.SingerName on every row.
	join := ""
	if q.ArtistID != 0 {
		join = ` join TableSongSinger g on g.SongIndex = s.SongIndex `
	}

	countSQL := `select count(*) from TableSong s ` + join + where

	var total int
	if err := d.sql.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting matches: %w", err)
	}

	listSQL := `
		select s.SongIndex, s.SongName, s.SingerName, s.LanType,
		       coalesce(l.LanName_EG, '')
		from TableSong s ` + join + `
		left join TableLan l on l.LanType = s.LanType ` +
		where + songOrder + `
		limit ? offset ?`

	// Ordering placeholders sit between the WHERE args and the paging args, so
	// they are appended in that order.
	listArgs := append(append([]any{}, args...), q.Limit, q.Offset)
	rows, err := d.sql.QueryContext(ctx, listSQL, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("searching: %w", err)
	}
	defer rows.Close()

	var out []Song
	for rows.Next() {
		var s Song
		if err := rows.Scan(&s.ID, &s.Title, &s.Artist, &s.LanType, &s.Language); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// songOrder sorts songs alphabetically.
//
// Alphabetical means FirstWord, not SongName. SQLite compares text with its
// default BINARY collation, under which every accented character sorts after Z,
// so ordering by the display title would put "ÁGUA" and every other
// accent-initial title at the very end of the catalogue rather than among the
// AGs. FirstWord is the accent-stripped uppercase copy, so it sorts the way a
// reader expects; it is truncated to 19 characters, hence the SongName
// tiebreak.
const songOrder = " order by s.FirstWord, s.SongName "

// conditions builds the WHERE clause and its arguments.
//
// Matching is done three ways because the schema stores the title twice: SongName
// is the display form with accents, and FirstWord is an accent-stripped uppercase
// copy — but truncated to 19 characters, so it alone misses long titles. Querying
// both, plus the id, is what makes "coracao" find "CORAÇÃO COM BURAQUINHOS".
func (q Query) conditions() (string, []any) {
	var clauses []string
	var args []any

	if text := strings.TrimSpace(q.Text); text != "" {
		like := "%" + strings.ToUpper(text) + "%"
		folded := "%" + foldAccents(strings.ToUpper(text)) + "%"

		// SongName is the display title and FirstWord an accent-stripped copy,
		// truncated to 19 characters — matching both is what lets "coracao" find
		// "CORAÇÃO COM BURAQUINHOS" without missing long titles.
		parts := []string{"upper(s.SongName) like ?", "s.FirstWord like ?"}
		args = append(args, like, folded)

		if q.MatchArtist {
			parts = append(parts, "upper(s.SingerName) like ?")
			args = append(args, like)
		}

		// A bare number is almost always someone typing a song id.
		if id, err := strconv.Atoi(text); err == nil {
			parts = append(parts, "s.SongIndex = ?")
			args = append(args, id)
		}
		clauses = append(clauses, "("+strings.Join(parts, " or ")+")")
	}

	if q.LanType != 0 {
		clauses = append(clauses, "s.LanType = ?")
		args = append(args, q.LanType)
	}

	if q.Alpha != 0 {
		clauses = append(clauses, "s.AlphaIndex = ?")
		args = append(args, q.Alpha)
	}

	if q.ArtistID != 0 {
		clauses = append(clauses, "g.SingerIndex = ?")
		args = append(args, q.ArtistID)
	}

	if q.IDs != nil {
		if len(q.IDs) == 0 {
			// An empty set, not an absent filter. Falling through to no clause
			// here would turn an empty favorites folder into the whole
			// catalogue.
			clauses = append(clauses, "1 = 0")
		} else {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(q.IDs)), ",")
			clauses = append(clauses, "s.SongIndex in ("+placeholders+")")
			for _, id := range q.IDs {
				args = append(args, id)
			}
		}
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return "where " + strings.Join(clauses, " and "), args
}

// SearchArtists lists performers, with how many songs each has, and the total
// number matching.
func (d *DB) SearchArtists(ctx context.Context, q Query) ([]Artist, int, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}

	var clauses []string
	var args []any
	if text := strings.TrimSpace(q.Text); text != "" {
		like := "%" + strings.ToUpper(text) + "%"
		folded := "%" + foldAccents(strings.ToUpper(text)) + "%"
		clauses = append(clauses, "(upper(a.SingerName) like ? or a.FirstWord like ?)")
		args = append(args, like, folded)
	}
	if q.LanType != 0 {
		clauses = append(clauses, "a.LanType = ?")
		args = append(args, q.LanType)
	}
	if q.Alpha != 0 {
		clauses = append(clauses, "a.AlphaIndex = ?")
		args = append(args, q.Alpha)
	}
	where := ""
	if len(clauses) > 0 {
		where = "where " + strings.Join(clauses, " and ")
	}

	var total int
	if err := d.sql.QueryRowContext(ctx,
		`select count(*) from TableSinger a `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting artists: %w", err)
	}

	rows, err := d.sql.QueryContext(ctx, `
		select a.SingerIndex, a.SingerName, count(g.SongIndex) c
		from TableSinger a
		left join TableSongSinger g on g.SingerIndex = a.SingerIndex `+
		where+`
		group by a.SingerIndex, a.SingerName
		order by a.FirstWord, a.SingerName
		limit ? offset ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing artists: %w", err)
	}
	defer rows.Close()

	var out []Artist
	for rows.Next() {
		var a Artist
		if err := rows.Scan(&a.ID, &a.Name, &a.Songs); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// Artist returns one performer by id.
func (d *DB) Artist(ctx context.Context, id int) (Artist, error) {
	var a Artist
	err := d.sql.QueryRowContext(ctx, `
		select a.SingerIndex, a.SingerName, count(g.SongIndex)
		from TableSinger a
		left join TableSongSinger g on g.SingerIndex = a.SingerIndex
		where a.SingerIndex = ?
		group by a.SingerIndex, a.SingerName`, id).Scan(&a.ID, &a.Name, &a.Songs)
	if err != nil {
		return Artist{}, fmt.Errorf("artist %d: %w", id, err)
	}
	return a, nil
}

// Song returns one entry by id.
func (d *DB) Song(ctx context.Context, id int) (Song, error) {
	var s Song
	err := d.sql.QueryRowContext(ctx, `
		select s.SongIndex, s.SongName, s.SingerName, s.LanType,
		       coalesce(l.LanName_EG, '')
		from TableSong s
		left join TableLan l on l.LanType = s.LanType
		where s.SongIndex = ?`, id).
		Scan(&s.ID, &s.Title, &s.Artist, &s.LanType, &s.Language)
	if err != nil {
		return Song{}, fmt.Errorf("song %d: %w", id, err)
	}
	return s, nil
}

// Songs looks up several entries at once, so a queue of ids can be shown with
// titles. Missing ids are simply absent from the result.
func (d *DB) Songs(ctx context.Context, ids []int) (map[int]Song, error) {
	out := make(map[int]Song, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := d.sql.QueryContext(ctx, `
		select s.SongIndex, s.SongName, s.SingerName, s.LanType,
		       coalesce(l.LanName_EG, '')
		from TableSong s
		left join TableLan l on l.LanType = s.LanType
		where s.SongIndex in (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("looking up songs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var s Song
		if err := rows.Scan(&s.ID, &s.Title, &s.Artist, &s.LanType, &s.Language); err != nil {
			return nil, err
		}
		out[s.ID] = s
	}
	return out, rows.Err()
}

// Languages lists the languages that actually have songs, most populous first.
func (d *DB) Languages(ctx context.Context) ([]Language, error) {
	rows, err := d.sql.QueryContext(ctx, `
		select s.LanType, coalesce(l.LanName_EG, ''), count(*) c
		from TableSong s
		left join TableLan l on l.LanType = s.LanType
		group by s.LanType
		having c > 0
		order by c desc`)
	if err != nil {
		return nil, fmt.Errorf("listing languages: %w", err)
	}
	defer rows.Close()

	var out []Language
	for rows.Next() {
		var l Language
		if err := rows.Scan(&l.LanType, &l.Name, &l.Songs); err != nil {
			return nil, err
		}
		if l.Name == "" {
			l.Name = fmt.Sprintf("Language %d", l.LanType)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AlphaIndexFor turns a letter into the bucket number the catalogue files it
// under: "A".."Z" are 1..26, and "0" is 27, where everything beginning with a
// digit lives. Anything else, "" included, is zero — no restriction.
//
// The numbering is the device's, not ours. TableSong.AlphaIndex and
// TableSinger.AlphaIndex already hold it, and both agree with the first
// character of FirstWord, so filtering on it needs no string work and gets
// accents right for free ("ÁGUA" is filed under A).
func AlphaIndexFor(letter string) int {
	if len(letter) != 1 {
		return 0
	}
	switch c := letter[0]; {
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 1
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 1
	case c == '0':
		return alphaDigits
	}
	return 0
}

// alphaDigits is the bucket the device gives titles that start with a digit.
const alphaDigits = 27

// foldAccents strips diacritics so a search for "coracao" matches the stored
// accent-free FirstWord column.
func foldAccents(s string) string {
	t := transform.Chain(
		norm.NFD,
		runes.Remove(runes.In(unicode.Mn)),
		norm.NFC,
	)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return out
}

// LanTypeNamed returns the id of the language with this English name, or zero
// if the catalogue does not have it.
//
// Looked up rather than hardcoded: the ids come from the device's own TableLan,
// and there is no guarantee another unit numbers them the same way.
func (d *DB) LanTypeNamed(ctx context.Context, name string) int {
	var lan int
	err := d.sql.QueryRowContext(ctx,
		`select LanType from TableLan where LanName_EG = ? collate nocase limit 1`, name).Scan(&lan)
	if err != nil {
		return 0
	}
	return lan
}
