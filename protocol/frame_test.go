package protocol

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

// fixture is one reference frame from testdata/golden_frames.txt.
type fixture struct {
	line       int
	fromDevice bool
	shape      string
	data       []byte
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	f, err := os.Open("testdata/golden_frames.txt")
	if err != nil {
		t.Fatalf("opening fixtures: %v", err)
	}
	defer f.Close()

	var out []fixture
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		shape := ""
		if i := strings.Index(line, "#"); i >= 0 {
			shape = strings.TrimSpace(line[i+1:])
			line = strings.TrimSpace(line[:i])
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("testdata/golden_frames.txt:%d: expected '<fromDevice> <hex>', got %q", n, line)
		}
		fromDevice, err := strconv.ParseBool(fields[0])
		if err != nil {
			t.Fatalf("testdata/golden_frames.txt:%d: %v", n, err)
		}
		data, err := hex.DecodeString(fields[1])
		if err != nil {
			t.Fatalf("testdata/golden_frames.txt:%d: %v", n, err)
		}
		out = append(out, fixture{line: n, fromDevice: fromDevice, shape: shape, data: data})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no fixtures loaded")
	}
	return out
}

// Every reference frame must parse, and re-encoding what we parsed must reproduce
// the original bytes exactly. This is the guard against silent wire-format
// regressions: a mis-ported field offset still parses and a tolerant device
// still echoes it back, so byte equality is the only reliable check.
func TestFrameRoundTrip(t *testing.T) {
	for _, fx := range loadFixtures(t) {
		t.Run(fx.shape, func(t *testing.T) {
			parsed, err := ParseFrame(fx.data, fx.fromDevice)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if parsed == nil {
				t.Fatal("parsed to nil")
			}

			// The database chunk frames carry an opaque payload and are
			// explicitly not re-encodable.
			if _, ok := parsed.(*DatabaseChunkFrame); ok {
				t.Skip("database chunk frames cannot be re-encoded")
			}

			got, err := parsed.Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !bytes.Equal(got, fx.data) {
				t.Errorf("re-encode differs (%d bytes in, %d out)\n%s",
					len(fx.data), len(got), DiffBytes(fx.data, got))
			}
		})
	}
}

// TagFor must reproduce the tag in the reference frames. Parsing preserves the
// tag it reads, so the round-trip tests cannot see a wrong table entry; this
// rebuilds each reference command from scratch instead, supplying only the
// command and its arguments.
func TestCommandTagsMatchGolden(t *testing.T) {
	for _, fx := range loadFixtures(t) {
		if fx.fromDevice {
			continue
		}
		frame, err := ParseFrame(fx.data, fx.fromDevice)
		if err != nil {
			t.Fatalf("%s: parse: %v", fx.shape, err)
		}
		cf, ok := frame.(*CommandFrame)
		if !ok || cf.Kind != KindCommand || cf.Request == nil {
			continue
		}

		t.Run(cf.Request.Command.String(), func(t *testing.T) {
			// No Tag override: the encoder must derive it from the command.
			rebuilt, err := (&CommandRequest{
				Seq:      cf.Seq,
				ClientID: cf.ClientID,
				Command:  cf.Request.Command,
				Args:     cf.Request.Fields,
			}).Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !bytes.Equal(rebuilt, fx.data) {
				t.Errorf("rebuilt from command alone differs\n%s\nwant tag %s, TagFor gives %s",
					DiffBytes(fx.data, rebuilt),
					tagString(cf.Tag), tagString(TagFor(cf.Request.Command)))
			}
		})
	}
}

// Client frames must also survive the semantic layer: parsing to a Message
// and rebuilding the wire form has to produce the original bytes. This is what
// catches a wrong per-command tag, which the structure-level round trip above
// cannot see because it copies the tag straight from the input.
func TestMessageRoundTrip(t *testing.T) {
	for _, fx := range loadFixtures(t) {
		if fx.fromDevice {
			continue // only client-originated frames are rebuildable
		}
		t.Run(fx.shape, func(t *testing.T) {
			parsed, err := ParseFrame(fx.data, fx.fromDevice)
			if err != nil {
				t.Fatalf("parse structure: %v", err)
			}
			msg, err := ParseMessage(parsed)
			if err != nil {
				t.Fatalf("parse message: %v", err)
			}
			if msg == nil {
				t.Skip("no semantic message for this frame yet")
			}
			got, err := msg.Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !bytes.Equal(got, fx.data) {
				t.Errorf("%T rebuilt differently (%d bytes in, %d out)\n%s",
					msg, len(fx.data), len(got), DiffBytes(fx.data, got))
			}
		})
	}
}
