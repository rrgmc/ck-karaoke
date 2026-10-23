package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Field list markers. Requests mark each field with 'B'; the device marks the
// fields of a response list with 'J'.
const (
	MarkerRequest  byte = 'B'
	MarkerResponse byte = 'J'

	// markerTerminator introduces the command id that closes a field list.
	markerTerminator byte = 'P'
)

// FieldList is the payload of a command frame: a run of length-prefixed strings
// followed by a terminator carrying the command id.
//
//	<marker><len><bytes> ... 'P' <command> [<extra>]
//
// The command id doubles as the list terminator, which is why a frame's meaning
// lives at its end rather than its start.
type FieldList struct {
	// Marker introduces each field, MarkerRequest or MarkerResponse.
	Marker byte
	// Fields are the arguments, always encoded as text on the wire.
	Fields []string
	// Command identifies what the frame does.
	Command Command
	// Extra is the byte following the command id, present when the id has its
	// high bit set. Nil otherwise.
	Extra *byte
}

// NewFieldList builds a request field list for a command, supplying the
// terminator's extra byte when the command needs one.
func NewFieldList(cmd Command, fields ...string) *FieldList {
	l := &FieldList{Marker: MarkerRequest, Fields: fields, Command: cmd}
	if extra, ok := ExtraFor(cmd); ok {
		l.Extra = &extra
	}
	return l
}

func (l *FieldList) String() string {
	return fmt.Sprintf("%s(%s)", l.Command, strings.Join(quoteAll(l.Fields), ", "))
}

// Field returns the field at i, or "" if there is no such field.
func (l *FieldList) Field(i int) string {
	if i < 0 || i >= len(l.Fields) {
		return ""
	}
	return l.Fields[i]
}

func (l *FieldList) Encode() []byte {
	var buf bytes.Buffer
	for _, f := range l.Fields {
		_, _ = buf.Write([]byte{l.Marker, byte(len(f))})
		_, _ = buf.WriteString(f)
	}
	_, _ = buf.Write([]byte{markerTerminator, byte(l.Command)})
	if l.Extra != nil {
		_ = buf.WriteByte(*l.Extra)
	}
	return buf.Bytes()
}

// ParseFieldList reads a request-marked field list.
func ParseFieldList(buf *bytes.Buffer) (*FieldList, error) {
	return parseFieldList(MarkerRequest, buf, []byte{MarkerRequest})
}

// parseFieldList reads a field list whose marker is one of allowed. Pass marker
// 0 to detect it from the first byte — responses reuse the request marker when
// echoing and switch to MarkerResponse when returning new data.
//
// Returns (nil, nil) when the buffer holds no list at all, which is a valid
// state for commands that take no arguments and return nothing.
func parseFieldList(marker byte, buf *bytes.Buffer, allowed []byte) (*FieldList, error) {
	list := &FieldList{Marker: marker}
	first := true

	for {
		b, err := buf.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && first {
				return nil, nil
			}
			return nil, fmt.Errorf("reading field marker: %w", err)
		}

		if b == markerTerminator {
			if err := binary.Read(buf, binary.BigEndian, &list.Command); err != nil {
				return nil, fmt.Errorf("reading command id: %w", err)
			}
			// A high bit in the command id means one more byte follows.
			if list.Command&0x80 != 0 {
				extra, err := buf.ReadByte()
				if err != nil {
					return nil, fmt.Errorf("reading command extra byte: %w", err)
				}
				list.Extra = &extra
			}
			return list, nil
		}

		if list.Marker == 0 && slices.Contains(allowed, b) {
			list.Marker = b
		}
		if b != list.Marker {
			if first {
				// Not a field list at all; leave the byte for the caller.
				if err := buf.UnreadByte(); err != nil {
					return nil, fmt.Errorf("unreading byte: %w", err)
				}
				return nil, nil
			}
			return nil, fmt.Errorf("expected field marker %q, got %q", list.Marker, b)
		}
		first = false

		var n byte
		if err := binary.Read(buf, binary.BigEndian, &n); err != nil {
			return nil, fmt.Errorf("reading field length: %w", err)
		}
		text, err := readN(buf, int(n), "field value")
		if err != nil {
			return nil, err
		}
		list.Fields = append(list.Fields, string(text))
	}
}
