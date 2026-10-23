package xmlbackup

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoundTrip(t *testing.T) {
	in := Favorites{
		Exported:  "2026-08-16T20:14:05Z",
		Catalogue: "MK8513-BR-B",
		Folders: []Folder{
			{Name: "Rock", Songs: []Song{
				{ID: 102300, Title: "Evidências", Artist: "Chitãozinho"},
				{ID: 100477, Title: "Anna Júlia", Artist: "Los Hermanos"},
			}},
			{Name: "Sertanejo", Songs: []Song{{ID: 102300, Title: "Evidências"}}},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, Marshal(&buf, in))

	out, err := Parse(&buf)
	require.NoError(t, err)

	assert.Equal(t, Version, out.Version)
	assert.Equal(t, in.Exported, out.Exported)
	assert.Equal(t, in.Catalogue, out.Catalogue)
	assert.Equal(t, in.Folders, out.Folders)
	assert.Equal(t, 3, out.Songs(), "a song filed in two folders counts in both")
}

// The file is meant to be opened, so the shape of it is part of the contract
// rather than an implementation detail of encoding/xml.
func TestMarshalIsReadable(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Marshal(&buf, Favorites{
		Folders: []Folder{{Name: "Rock", Songs: []Song{{ID: 12, Title: "Anna Júlia"}}}},
	}))
	got := buf.String()

	assert.True(t, strings.HasPrefix(got, `<?xml version="1.0" encoding="UTF-8"?>`),
		"a text editor and a browser both want the declaration: %s", got)
	assert.Contains(t, got, `<favorites version="1"`, "the version is always written out")
	assert.Contains(t, got, "\n  <folder", "indented, because someone will read it")
	assert.Contains(t, got, `title="Anna Júlia"`, "UTF-8 survives, unescaped")
	assert.True(t, strings.HasSuffix(got, "\n"), "a text file ends in a newline")
}

// Titles are decoration. A file hand-written with nothing but ids has to import.
func TestParseIDsOnly(t *testing.T) {
	out, err := Parse(strings.NewReader(`
		<favorites version="1">
		  <folder name="Rock"><song id="12"/><song id="34"/></folder>
		</favorites>`))
	require.NoError(t, err)
	require.Len(t, out.Folders, 1)
	assert.Equal(t, []Song{{ID: 12}, {ID: 34}}, out.Folders[0].Songs)
}

// the favorites store's unique constraint is `collate nocase`, so two spellings of one name
// are one folder there. Merging here is what keeps the file and the database
// agreeing about how many folders it holds.
func TestParseMergesFoldersIgnoringCase(t *testing.T) {
	out, err := Parse(strings.NewReader(`
		<favorites version="1">
		  <folder name="Rock"><song id="12"/></folder>
		  <folder name="  rock  "><song id="34"/><song id="12"/></folder>
		</favorites>`))
	require.NoError(t, err)

	require.Len(t, out.Folders, 1)
	assert.Equal(t, "Rock", out.Folders[0].Name, "the first spelling wins")
	assert.Equal(t, []Song{{ID: 12}, {ID: 34}}, out.Folders[0].Songs,
		"and the repeated id is filed once")
}

// One mangled row must not cost the rest of the file. Being able to salvage a
// backup by hand is most of why the titles are in it.
func TestParseSkipsWhatItCannotUse(t *testing.T) {
	out, err := Parse(strings.NewReader(`
		<favorites version="1">
		  <folder name="   "><song id="12"/></folder>
		  <folder name="Rock">
		    <song id="0"/>
		    <song id="-5"/>
		    <song/>
		    <song id=""/>
		    <song id="not a number" title="Anna Júlia"/>
		    <song id=" 34 "/>
		  </folder>
		</favorites>`))
	require.NoError(t, err)

	require.Len(t, out.Folders, 1, "a folder with no name has nothing to be filed under")
	assert.Equal(t, "Rock", out.Folders[0].Name)
	assert.Equal(t, []Song{{ID: 34}}, out.Folders[0].Songs,
		"nothing that is not an id survives, and whitespace around one does not stop it being one")
}

// Half an import is worse than none, so anything that cannot be fully accounted
// for has to refuse rather than yield what it managed to read.
func TestParseRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want error
	}{
		"empty file":     {``, ErrFormat},
		"not xml at all": {"\xff\xd8\xff\xe0 JFIF", ErrFormat},
		"another schema": {`<playlist><song id="1"/></playlist>`, ErrFormat},
		"truncated": {`<favorites version="1"><folder name="Rock"><song id="12"/>`,
			ErrFormat},
		"from the future": {`<favorites version="99"><folder name="R"><song id="1"/></folder></favorites>`,
			ErrVersion},
		"no folders": {`<favorites version="1"></favorites>`, ErrEmpty},
		"no songs": {`<favorites version="1"><folder name="Rock"></folder></favorites>`,
			ErrEmpty},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.in))
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A version field older than this one must keep working, or a backup stops being
// a backup the moment the format moves.
func TestParseAcceptsAnOlderVersion(t *testing.T) {
	out, err := Parse(strings.NewReader(
		`<favorites version="0"><folder name="Rock"><song id="12"/></folder></favorites>`))
	require.NoError(t, err)
	assert.Equal(t, Version, out.Version, "what came back is in this version's terms")
}

// Marshal fixes the version rather than trusting its caller, so a file cannot be
// written claiming to be something it is not.
func TestMarshalStampsTheVersion(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Marshal(&buf, Favorites{
		Version: 99,
		Folders: []Folder{{Name: "Rock", Songs: []Song{{ID: 12}}}},
	}))
	assert.Contains(t, buf.String(), `version="1"`)

	out, err := Parse(&buf)
	require.NoError(t, err)
	assert.Equal(t, Version, out.Version)
}
