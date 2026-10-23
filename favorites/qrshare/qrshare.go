// Package qrshare encodes one favorites folder as a string of decimal digits,
// so that it can be carried to another device as a QR code.
//
// Two phones each keep their own favorites database and, by design, cannot
// reach each other: both app wrappers bind the server to loopback so that
// nothing else on the WiFi can drive the karaoke unit. A QR code held up
// between two screens needs no network, no account and no pairing, which also
// suits the pair of devices this was written for — an Android phone and an
// iPad, between which neither AirDrop nor Nearby Share exists.
//
// # Why digits
//
// QR has a numeric mode that packs 3⅓ bits per character where byte mode
// spends 8, and rsc.io/qr picks one mode for the whole string — "we could split
// the string and use different encodings but that seems like overkill" — so a
// single letter anywhere would push the entire payload into the wider mode and
// cost roughly two and a half times the size.
//
// The folder's name travels anyway, written as three digits per UTF-8 byte.
// That is 10 bits where byte mode would spend 8, but it is paid only on the
// name: a folder called "Rock" costs twelve digits against a body of hundreds,
// and the ids keep the dense encoding. Leaving the name out would force the
// receiving device to ask which folder it had just been handed.
//
// # The format
//
//	format(1) width(1) count(4) namelen(3) name(3 digits/byte) id×count(width each) check(2)
//	└─ "2"    └─ 1-9   └─ 0142  └─ 004     └─ 082111099107     └─ 0102300477…      └─ mod-97
//
// The width of an id field is measured from the largest id and written into the
// header rather than fixed, so nothing here assumes how a given device numbers
// its catalogue.
//
// # Why it is checked
//
// A camera that reads three quarters of a code, or a paste that clips the last
// line, must fail rather than quietly import a truncated folder. The length is
// therefore fully determined by the header and verified against it, and a
// mod-97 check closes the case where a plausible-looking string has the right
// shape and the wrong contents.
package qrshare

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Folder is what one code carries.
type Folder struct {
	// Name is the folder's name on the sending device. It is a label, not an
	// instruction: the receiving device uses it to offer the obvious
	// destination, and never to create one.
	Name string
	// IDs are the catalogue song ids filed in it.
	IDs []int
}

// Errors callers are expected to tell the user about rather than merely log.
var (
	// ErrFormat is a code that is not one of ours, or has been damaged.
	ErrFormat = errors.New("this does not look like a favorites code")
	// ErrChecksum is a code of the right shape whose check digits disagree,
	// which means it was mis-scanned or mis-copied.
	ErrChecksum = errors.New("this code is damaged; scan or copy it again")
	// ErrEmpty is a folder with nothing in it, which is not worth a code.
	ErrEmpty = errors.New("there are no songs to share")
	// ErrTooLarge is more songs, or larger ids, than the format can carry.
	ErrTooLarge = errors.New("this folder is too large to share as one code")
)

const (
	// Format is the current format digit.
	Format = '2'

	// MaxSongs is what the four-digit count field can express.
	MaxSongs = 9999
	// MaxName is what the three-digit name length can express, in bytes. Far
	// beyond the sixty characters a folder name is allowed.
	MaxName = 999

	// maxWidth is what the one-digit width field can express, and 999999999
	// song ids is beyond any karaoke catalogue.
	maxWidth = 9

	headerLen = 9 // format, width, a four-digit count, a three-digit name length
	checkLen  = 2
)

// Encode renders a folder as a code. The ids are sorted and de-duplicated
// first, so that two devices holding the same songs produce the same string
// whatever order they happen to store them in.
func Encode(f Folder) (string, error) {
	clean, err := normalise(f.IDs)
	if err != nil {
		return "", err
	}

	// The largest id decides how wide every field is. Sorting has already put
	// it last.
	width := len(strconv.Itoa(clean[len(clean)-1]))
	if width > maxWidth {
		return "", fmt.Errorf("%w: song id %d is too large", ErrTooLarge, clean[len(clean)-1])
	}

	name := trimName(f.Name)

	var b strings.Builder
	b.Grow(headerLen + 3*len(name) + len(clean)*width + checkLen)
	b.WriteByte(Format)
	b.WriteString(strconv.Itoa(width))
	fmt.Fprintf(&b, "%04d", len(clean))
	fmt.Fprintf(&b, "%03d", len(name))
	for i := range len(name) {
		fmt.Fprintf(&b, "%03d", name[i])
	}
	for _, id := range clean {
		fmt.Fprintf(&b, "%0*d", width, id)
	}

	body := b.String()
	return body + fmt.Sprintf("%02d", checksum(body)), nil
}

// Decode reads a code back, rejecting anything it cannot fully account for.
//
// Whitespace is ignored, because a code that has been through a messaging app
// or a text field arrives wrapped across lines.
func Decode(code string) (Folder, error) {
	s := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, code)

	if len(s) < headerLen+checkLen {
		return Folder{}, ErrFormat
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return Folder{}, ErrFormat
		}
	}
	switch s[0] {
	case Format:
	default:
		return Folder{}, fmt.Errorf("%w: it is in a format this version does not know", ErrFormat)
	}

	width := int(s[1] - '0')
	if width < 1 || width > maxWidth {
		return Folder{}, ErrFormat
	}
	count, err := strconv.Atoi(s[2:6])
	if err != nil || count == 0 {
		return Folder{}, ErrFormat
	}
	nameLen, err := strconv.Atoi(s[6:headerLen])
	if err != nil {
		return Folder{}, ErrFormat
	}

	// The header fully determines the length, which is what turns a truncated
	// scan into an error instead of a short folder.
	if len(s) != headerLen+3*nameLen+count*width+checkLen {
		return Folder{}, fmt.Errorf("%w: it looks incomplete", ErrFormat)
	}

	body, check := s[:len(s)-checkLen], s[len(s)-checkLen:]
	if want := fmt.Sprintf("%02d", checksum(body)); check != want {
		return Folder{}, ErrChecksum
	}

	name := make([]byte, 0, nameLen)
	for i := range nameLen {
		at := headerLen + i*3
		b, err := strconv.Atoi(s[at : at+3])
		if err != nil || b > 255 {
			return Folder{}, ErrFormat
		}
		name = append(name, byte(b))
	}

	ids := make([]int, 0, count)
	for i := range count {
		at := headerLen + 3*nameLen + i*width
		id, err := strconv.Atoi(s[at : at+width])
		if err != nil {
			return Folder{}, ErrFormat
		}
		ids = append(ids, id)
	}

	// A name that does not survive the round trip is a label, not data, so it
	// is dropped rather than failing the whole code — the songs are the point.
	out := Folder{IDs: ids}
	if utf8.Valid(name) {
		out.Name = string(name)
	}
	return out, nil
}

// trimName bounds the name to what the length field can express, cutting on a
// rune boundary so a multi-byte character is never split in half.
func trimName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) <= MaxName {
		return name
	}
	cut := MaxName
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

// normalise sorts, de-duplicates and range-checks the ids.
func normalise(ids []int) ([]int, error) {
	out := make([]int, 0, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id < 0 {
			return nil, fmt.Errorf("%w: song id %d", ErrFormat, id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	if len(out) > MaxSongs {
		return nil, fmt.Errorf("%w: %d songs, and the limit is %d", ErrTooLarge, len(out), MaxSongs)
	}
	sort.Ints(out)
	return out, nil
}

// checksum is the digit string read as one long number, modulo 97.
//
// Taken a digit at a time because the number is far too large for any integer
// type — a three-hundred-song code is over fifteen hundred digits.
func checksum(digits string) int {
	r := 0
	for i := range len(digits) {
		r = (r*10 + int(digits[i]-'0')) % 97
	}
	return r
}
