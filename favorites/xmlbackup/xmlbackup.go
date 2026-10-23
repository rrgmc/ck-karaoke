// Package xmlbackup carries the whole favorites database — every folder and the
// songs filed in it — as one XML file.
//
// # Why this is not qrshare
//
// qrshare exists to fit one folder through a QR code, and every decision in it
// follows from that: decimal digits because QR's numeric mode is two and a half
// times denser than byte mode, a width field so ids pack tightly, a mod-97 check
// because a camera can read three quarters of a code. None of that applies to a
// file, and a file has the one thing a code cannot have — room. So this is a
// separate format for a separate job, and the two meet only at favorites.AddSongs.
//
// Sync moves one folder between two devices standing next to each other. A
// backup moves everything, to somewhere it can sit for a year: the filesystem,
// Dropbox, Drive. A phone that is reset or replaced loses its favorites outright
// — the Android app sets allowBackup="false", so even an uninstall takes them —
// and no arrangement of QR codes is a backup of that.
//
// # What is in the file, and what is read back
//
// Ids are what an import reads. Titles and artists ride along so the file is
// worth opening: a backup that says nothing about what is in it cannot be
// checked, cannot be diffed, and cannot be salvaged by hand when something else
// has gone wrong. They are written from the catalogue at export time and ignored
// at import.
//
// # Reading is forgiving about shape and strict about identity
//
// A folder with no name, a song whose id is missing or mistyped, the same folder
// written twice: all of these are recoverable, and recovering them is better
// than refusing a file someone may have edited by hand. What is not recoverable
// is a file that is not ours, one that is damaged, or one from a version that
// knows more than this one — those refuse, for the same reason qrshare.Decode
// refuses a truncated code. Half an import is worse than none.
package xmlbackup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Version is the format this package writes. A file declaring more than this is
// refused rather than guessed at.
const Version = 1

// Errors callers are expected to tell the user about rather than merely log.
var (
	// ErrFormat is a file that is not one of ours, or is damaged.
	ErrFormat = errors.New("this does not look like a favorites file")
	// ErrVersion is a file from a newer version of the app. Worth its own error
	// because the remedy is specific.
	ErrVersion = errors.New("this file was made by a newer version of the app")
	// ErrEmpty is a file that parsed but holds nothing to import.
	ErrEmpty = errors.New("there are no songs in this file")
)

// Song is one catalogue song. Only ID is read back; the rest is for a person
// reading the file.
type Song struct {
	ID     int    `xml:"id,attr"`
	Title  string `xml:"title,attr,omitempty"`
	Artist string `xml:"artist,attr,omitempty"`
}

// Folder is one favorites folder and its songs.
type Folder struct {
	Name  string `xml:"name,attr"`
	Songs []Song `xml:"song"`
}

// Favorites is a whole database.
type Favorites struct {
	XMLName xml.Name `xml:"favorites"`
	// Version is the format, not the app's version.
	Version int `xml:"version,attr"`
	// Exported is when the file was written, RFC 3339.
	Exported string `xml:"exported,attr,omitempty"`
	// Catalogue names the song database the ids belong to. It is a label, never
	// an instruction: an import checks its own catalogue and does not compare
	// this, because a matching name is no guarantee two units number songs alike
	// and a differing one is no reason to refuse a file that may import cleanly.
	Catalogue string `xml:"catalogue,attr,omitempty"`

	Folders []Folder `xml:"folder"`
}

// Songs counts the memberships in the file, which is what an import will read.
func (f Favorites) Songs() int {
	n := 0
	for _, folder := range f.Folders {
		n += len(folder.Songs)
	}
	return n
}

// Marshal writes a file.
func Marshal(w io.Writer, f Favorites) error {
	f.Version = Version

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("writing favorites: %w", err)
	}
	enc := xml.NewEncoder(w)
	// Indented because someone will open this. It costs a few bytes per song
	// against a file that can be read, edited and diffed.
	enc.Indent("", "  ")
	if err := enc.Encode(f); err != nil {
		return fmt.Errorf("writing favorites: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("writing favorites: %w", err)
	}
	// encoding/xml writes no trailing newline, and a text file wants one.
	_, err := io.WriteString(w, "\n")
	return err
}

// Reading uses its own types, differing from the written ones in one place: an
// id arrives as text and is converted here.
//
// Decoding straight into an int would make a mistyped id fatal to the whole
// file, and fatal with strconv's own words showing through onto the page. It
// would also be arbitrary — a song with no id at all is skipped, so a song whose
// id is not a number has no business being treated worse. One mangled row must
// not cost the other thousand songs; being able to salvage a file by hand is
// most of why the titles are written in the first place.
type rawFavorites struct {
	XMLName   xml.Name    `xml:"favorites"`
	Version   int         `xml:"version,attr"`
	Exported  string      `xml:"exported,attr"`
	Catalogue string      `xml:"catalogue,attr"`
	Folders   []rawFolder `xml:"folder"`
}

type rawFolder struct {
	Name  string    `xml:"name,attr"`
	Songs []rawSong `xml:"song"`
}

type rawSong struct {
	ID     string `xml:"id,attr"`
	Title  string `xml:"title,attr"`
	Artist string `xml:"artist,attr"`
}

// id is the song's id, or zero for anything that is not one.
func (s rawSong) id() int {
	n, err := strconv.Atoi(strings.TrimSpace(s.ID))
	if err != nil {
		return 0
	}
	return n
}

// Parse reads a file back, cleaning up what it can and refusing what it cannot.
//
// Folders arrive in the order the file lists them, with duplicates merged into
// the first mention. Ids are de-duplicated within a folder but not across
// folders, because a song filed in two folders belongs in both.
func Parse(r io.Reader) (Favorites, error) {
	var raw rawFavorites
	// Strict is left on: a file with mismatched tags is damaged, and damaged is
	// exactly the case this must not import half of. The tag on XMLName is doing
	// work here too — it is what makes Decode refuse a well-formed XML file
	// whose root element is something other than <favorites>, which is how a
	// photograph or a spreadsheet handed to the file picker is turned away.
	if err := xml.NewDecoder(r).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return Favorites{}, ErrFormat
		}
		return Favorites{}, fmt.Errorf("%w: %s", ErrFormat, err)
	}
	// A file from the future may mean things by fields this version would drop,
	// so it is refused rather than partly understood. A file from the past is
	// fine by construction: every later version must still read version 1.
	if raw.Version > Version {
		return Favorites{}, fmt.Errorf("%w: it says version %d and this app knows %d",
			ErrVersion, raw.Version, Version)
	}

	out := Favorites{
		Version:   Version,
		Exported:  raw.Exported,
		Catalogue: raw.Catalogue,
	}
	// Folders are matched without regard to case, because the favorites store's unique
	// constraint is `collate nocase` — "Rock" and "rock" are one folder there,
	// and a file that named both would otherwise import into a folder that then
	// refused to exist.
	at := map[string]int{}
	seen := map[int]map[int]bool{}

	for _, folder := range raw.Folders {
		name := strings.TrimSpace(folder.Name)
		if name == "" {
			continue // nothing to file it under, and the favorites store would refuse the name
		}

		key := strings.ToLower(name)
		i, ok := at[key]
		if !ok {
			i = len(out.Folders)
			at[key] = i
			out.Folders = append(out.Folders, Folder{Name: name})
			seen[i] = map[int]bool{}
		}

		for _, song := range folder.Songs {
			// Ids start at 1001 in the catalogue this was written against and
			// are positive in any case, so zero is what an absent, blank or
			// mistyped attribute comes to — none of which is an id, and none of
			// which is worth failing a file over.
			id := song.id()
			if id <= 0 || seen[i][id] {
				continue
			}
			seen[i][id] = true
			out.Folders[i].Songs = append(out.Folders[i].Songs,
				Song{ID: id, Title: song.Title, Artist: song.Artist})
		}
	}

	if out.Songs() == 0 {
		return Favorites{}, ErrEmpty
	}
	return out, nil
}
