package protocol

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func readN(r *bytes.Buffer, n int, what string) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("reading %s: %w (%d bytes left)", what, err, r.Len())
	}
	return buf, nil
}

func readNEquals(r *bytes.Buffer, n int, expected []byte, what string) error {
	_, err := readNEqualsValue(r, n, expected, what)
	return err
}

func readNEqualsValue(r *bytes.Buffer, n int, expected []byte, what string) ([]byte, error) {
	buf, err := readN(r, n, what)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(buf, expected) {
		return nil, fmt.Errorf("%s: expected % x, got % x", what, expected, buf)
	}
	return buf, nil
}

// checkReadFull reports an error if anything is left unparsed, which means the
// frame layout is not what we think it is.
func checkReadFull(buf *bytes.Buffer) error {
	if buf.Len() == 0 {
		return nil
	}
	return fmt.Errorf("%d bytes left unparsed: % x", buf.Len(), buf.Bytes())
}

// DiffBytes describes where two encodings differ, for test failures.
func DiffBytes(a, b []byte) string {
	if len(a) != len(b) {
		return fmt.Sprintf("different lengths: %d vs %d", len(a), len(b))
	}
	var out []string
	for i := range a {
		if a[i] != b[i] {
			out = append(out, fmt.Sprintf("[%d] %02x != %02x", i, a[i], b[i]))
		}
	}
	return strings.Join(out, ", ")
}

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = strconv.Quote(s)
	}
	return out
}

func joinQuoted(items []string, sep string) string {
	return strings.Join(quoteAll(items), sep)
}

// tagString renders a frame tag as its little-endian name fragment when it is
// printable, and as hex otherwise.
func tagString(tag [4]byte) string {
	reversed := []byte{tag[3], tag[2], tag[1], tag[0]}
	printable := true
	for _, b := range reversed {
		if b < 32 || b > 126 {
			printable = false
			break
		}
	}
	if printable {
		return strconv.Quote(string(reversed))
	}
	return fmt.Sprintf("%x", tag)
}

func directionName(fromDevice bool) string {
	if fromDevice {
		return "device"
	}
	return "client"
}
