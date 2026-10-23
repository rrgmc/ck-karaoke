package qrshare

import (
	"errors"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	code, err := Encode(Folder{Name: "Rock", IDs: []int{2511, 10233, 7, 99999}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := Decode(code)
	if err != nil {
		t.Fatalf("Decode(%q): %v", code, err)
	}
	if got.Name != "Rock" {
		t.Errorf("name = %q, want Rock", got.Name)
	}
	want := []int{7, 2511, 10233, 99999} // sorted
	if len(got.IDs) != len(want) {
		t.Fatalf("got %v, want %v", got.IDs, want)
	}
	for i := range want {
		if got.IDs[i] != want[i] {
			t.Fatalf("got %v, want %v", got.IDs, want)
		}
	}
}

// The names in this catalogue are Portuguese, so accents are the common case
// rather than an edge one. Three digits a byte carries UTF-8 unchanged.
func TestNameSurvivesAccentsAndSpaces(t *testing.T) {
	for _, name := range []string{
		"Sertanejo Raiz",
		"Forró & Pagode",
		"Músicas da Vovó",
		"日本語",
		"Favorites",
	} {
		code, err := Encode(Folder{Name: name, IDs: []int{1, 2, 3}})
		if err != nil {
			t.Fatalf("Encode(%q): %v", name, err)
		}
		got, err := Decode(code)
		if err != nil {
			t.Fatalf("Decode for %q: %v", name, err)
		}
		if got.Name != name {
			t.Errorf("name round-tripped as %q, want %q", got.Name, name)
		}
	}
}

// The saving that pays for the name: everything stays in QR's numeric mode, so
// a letter in a folder name cannot push the whole payload into byte mode.
func TestCodeIsDigitsOnly(t *testing.T) {
	code, err := Encode(Folder{Name: "Forró & Pagode", IDs: []int{4, 8, 15, 16, 23, 42}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimLeft(code, "0123456789") != "" {
		t.Errorf("code has non-digits: %q", code)
	}
}

// Two devices holding the same songs must produce the same string, or a person
// comparing two codes by eye would conclude they had diverged when they had not.
func TestOrderAndDuplicatesDoNotChangeTheCode(t *testing.T) {
	a, err := Encode(Folder{Name: "Rock", IDs: []int{300, 100, 200}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(Folder{Name: "Rock", IDs: []int{200, 300, 100, 200, 100}})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same songs gave different codes:\n %s\n %s", a, b)
	}
}

// The whole point of the length being derivable from the header: a camera that
// reads three quarters of a code must fail, not import three quarters of a
// folder.
func TestTruncatedCodeIsRejected(t *testing.T) {
	code, err := Encode(Folder{Name: "Rock", IDs: []int{11111, 22222, 33333, 44444, 55555}})
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{1, 5, 12} {
		short := code[:len(code)-cut]
		if _, err := Decode(short); !errors.Is(err, ErrFormat) {
			t.Errorf("Decode(%q) = %v, want ErrFormat", short, err)
		}
	}
}

// A single wrong digit is always caught, because 97 is prime and no
// single-digit change can be a multiple of it. Covers the name bytes too,
// since the checksum is taken before the name is read back.
func TestSingleWrongDigitIsCaught(t *testing.T) {
	code, err := Encode(Folder{Name: "Rock", IDs: []int{11111, 22222, 33333}})
	if err != nil {
		t.Fatal(err)
	}
	for i := headerLen; i < len(code)-checkLen; i++ {
		bad := []byte(code)
		if bad[i] == '9' {
			bad[i] = '0'
		} else {
			bad[i]++
		}
		if _, err := Decode(string(bad)); !errors.Is(err, ErrChecksum) {
			t.Fatalf("a wrong digit at %d gave %v, want ErrChecksum", i, err)
		}
	}
}

// Codes arrive through messaging apps and text fields, which wrap them.
func TestWhitespaceIsIgnored(t *testing.T) {
	code, err := Encode(Folder{Name: "Rock", IDs: []int{101, 202, 303}})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := "  " + code[:5] + "\n" + code[5:10] + " \t" + code[10:] + "\r\n"
	got, err := Decode(wrapped)
	if err != nil {
		t.Fatalf("Decode(wrapped): %v", err)
	}
	if len(got.IDs) != 3 || got.Name != "Rock" {
		t.Errorf("got %+v, want Rock with 3 songs", got)
	}
}

func TestRejectsJunk(t *testing.T) {
	for name, in := range map[string]string{
		"empty":        "",
		"short":        "1400",
		"letters":      "24000000ABCDE12",
		"a URL":        "https://example.com/",
		"wrong format": "940000000101099",
		"zero width":   "2000000000101",
	} {
		if _, err := Decode(in); err == nil {
			t.Errorf("%s: Decode(%q) succeeded, want an error", name, in)
		}
	}
}

func TestEmptyFolderHasNoCode(t *testing.T) {
	if _, err := Encode(Folder{Name: "Rock"}); !errors.Is(err, ErrEmpty) {
		t.Errorf("Encode with no songs = %v, want ErrEmpty", err)
	}
}

// A folder must still travel if its name will not, since the songs are the
// point and the name is only a label.
func TestNamelessFolderStillWorks(t *testing.T) {
	code, err := Encode(Folder{IDs: []int{1, 2}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(code)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Name != "" || len(got.IDs) != 2 {
		t.Errorf("got %+v, want two songs and no name", got)
	}
}

func TestTooManySongs(t *testing.T) {
	ids := make([]int, MaxSongs+1)
	for i := range ids {
		ids[i] = i + 1
	}
	if _, err := Encode(Folder{Name: "Rock", IDs: ids}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Encode(%d songs) = %v, want ErrTooLarge", len(ids), err)
	}
}

// An over-long name is cut rather than refused, and cut on a rune boundary so
// the remainder is still valid UTF-8.
func TestOverlongNameIsTrimmedCleanly(t *testing.T) {
	code, err := Encode(Folder{Name: strings.Repeat("é", 800), IDs: []int{1}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(code)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got.Name) > MaxName {
		t.Errorf("name is %d bytes, over the %d limit", len(got.Name), MaxName)
	}
	if strings.ContainsRune(got.Name, '�') {
		t.Error("the name was cut through the middle of a character")
	}
}

// The id width comes from the data, so a catalogue numbering its songs
// differently costs nothing.
func TestWidthFollowsTheLargestID(t *testing.T) {
	narrow, err := Encode(Folder{Name: "R", IDs: []int{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	wide, err := Encode(Folder{Name: "R", IDs: []int{1, 2, 300000}})
	if err != nil {
		t.Fatal(err)
	}
	if narrow[1] != '1' {
		t.Errorf("width digit = %c, want 1 for single-digit ids", narrow[1])
	}
	if wide[1] != '6' {
		t.Errorf("width digit = %c, want 6 for 300000", wide[1])
	}
	if len(narrow) >= len(wide) {
		t.Errorf("narrow code (%d) should be shorter than wide (%d)", len(narrow), len(wide))
	}
}
