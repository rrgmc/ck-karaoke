package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/adler32"
	"os"
	"strings"
)

// Frame classes and kinds.
const (
	// ClassControl is the frame class of all control traffic.
	ClassControl byte = 0x50

	KindSession byte = 0x00 // session establishment
	KindCommand byte = 0x02 // a command and its echo
	KindStatus  byte = 0x03 // device-pushed state
)

// DefaultClientName is the fallback when no name is chosen.
//
// The name is a length-prefixed string, 0x12 <len> <name>, and the device does
// not validate it: with a correct session checksum, any name is accepted. The
// checksum covers the name, so changing one means recomputing the other.
//
// The device tags each reserved song with the name of the session that added
// it, so this is what the unit shows beside a song on screen.
const DefaultClientName = "NE2211"

// maxClientName bounds the name, since its length is written as one byte.
const maxClientName = 255

// NameLimit is how much of a name to keep.
//
// Far below maxClientName on purpose: the wire allows 255 bytes but the unit's
// screen is the real constraint, and a name long enough to push the song title
// off it helps nobody.
const NameLimit = 20

// CleanName makes a name safe to put on the wire and legible on the device.
//
// Control characters and anything non-ASCII are dropped rather than escaped:
// the unit's font is unknown, and a name that renders as boxes is worse than
// one spelled without its accents. Runs of whitespace collapse, and the result
// is capped at NameLimit. An empty result falls back to DefaultClientName, so
// this never produces a frame the device would reject for a zero-length name.
func CleanName(name string) string {
	var b strings.Builder
	space := false
	for _, r := range name {
		switch {
		case r == ' ' || r == '	':
			space = b.Len() > 0
		case r < 0x20 || r > 0x7E:
			// Dropped: outside printable ASCII.
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
		if b.Len() >= NameLimit {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > NameLimit {
		out = strings.TrimSpace(out[:NameLimit])
	}
	if out == "" {
		return DefaultClientName
	}
	return out
}

// HostName is the machine's name, cleaned for the wire — the default a program
// announces itself as, so the device shows something meaningful without anyone
// configuring anything.
func HostName() string {
	h, err := os.Hostname()
	if err != nil {
		return DefaultClientName
	}
	// Strip any domain suffix: "kitchen.local" is "kitchen" on a TV.
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return CleanName(h)
}

// DatabaseID identifies the song database on the device. It appears as status
// field 13 and inside the default database path.
const DatabaseID = "d91c983b52ce486f24c53af1430f21e0"

// Frame is one decoded application message in its wire form. It is the layer
// below Message: framing and field lists, with no interpretation of what the
// fields mean.
type Frame interface {
	// Encode renders the frame back to bytes.
	Encode() ([]byte, error)
	isFrame()
}

// FrameHeader is common to every control frame.
type FrameHeader struct {
	// Class is always ClassControl in practice.
	Class byte
	// Kind distinguishes session, command and status frames.
	Kind byte
	// Seq is the sender's sequence number. Requests and their echoes share one;
	// the device's status frames run an independent counter that overlaps it, so
	// never correlate on Seq alone.
	Seq uint32
	// Tag is a four-byte little-endian fragment of the command's name. See
	// TagFor.
	Tag [4]byte
	// FromDevice reports the direction; device frames carry four extra bytes.
	FromDevice bool
}

func (h *FrameHeader) String() string {
	return fmt.Sprintf("%02x/%02x seq=%d tag=%s from=%s",
		h.Class, h.Kind, h.Seq, tagString(h.Tag), directionName(h.FromDevice))
}

func (h *FrameHeader) Encode() ([]byte, error) {
	var buf bytes.Buffer
	_, _ = buf.Write([]byte{h.Class, h.Kind})
	_, _ = buf.Write([]byte{0x00, 0x00, 0x00, 0x00})
	_ = binary.Write(&buf, binary.BigEndian, h.Seq)
	_, _ = buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	if h.FromDevice {
		_, _ = buf.Write([]byte{0x00, 0x00, 0x00, 0x00})
	}
	_, _ = buf.Write(h.Tag[:])
	return buf.Bytes(), nil
}

// CommandFrame carries a command and its arguments. The device echoes commands
// back with the body repeated twice: once as received, once with its response.
type CommandFrame struct {
	FrameHeader
	// ClientID is the caller's UUID, 36 ASCII characters.
	ClientID string
	// Request is the field list as sent.
	Request *FieldList
	// Name is the client name the device displays. Empty means DefaultClientName.
	Name string
	// Response is the device's reply list, present only on echoes that return
	// data rather than repeating the request.
	Response *FieldList
	// Trailer is extra bytes some session echoes append.
	Trailer []byte
}

func (f *CommandFrame) isFrame() {}

func (f *CommandFrame) String() string {
	s := fmt.Sprintf("command[%s client=%s", f.FrameHeader.String(), f.ClientID)
	if f.Name != "" && f.Name != DefaultClientName {
		s += " name=" + f.Name
	}
	if f.Request != nil {
		s += " req=" + f.Request.String()
	}
	if f.Response != nil {
		s += " resp=" + f.Response.String()
	}
	if len(f.Trailer) > 0 {
		s += fmt.Sprintf(" trailer=%x", f.Trailer)
	}
	return s + "]"
}

// Command returns the frame's command id, or 0 when it carries no field list
// (session frames).
func (f *CommandFrame) Command() Command {
	if f.Request == nil {
		return 0
	}
	return f.Request.Command
}

// bodyPrefix is how every frame body starts: the client id, then the name.
// Shared with SessionChecksum, because the checksum is taken over exactly these
// bytes and the two drifting apart would be undetectable until the device
// refused a session.
func bodyPrefix(clientID, name string) []byte {
	var b bytes.Buffer
	_, _ = b.Write([]byte{0x0A, 0x24})
	_, _ = b.WriteString(clientID)
	_, _ = b.Write([]byte{0x12, byte(len(name))})
	_, _ = b.WriteString(name)
	return b.Bytes()
}

// SessionChecksum is the four bytes a session-start frame carries where command
// frames carry their label: an Adler-32 of the frame body, big-endian.
//
// The device checks it. With a correct checksum any UUID and any name are
// accepted.
func SessionChecksum(clientID, name string) [4]byte {
	if name == "" {
		name = DefaultClientName
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], adler32.Checksum(bodyPrefix(clientID, name)))
	return out
}

func (f *CommandFrame) Encode() ([]byte, error) {
	if len(f.ClientID) != 36 {
		return nil, fmt.Errorf("client id must be 36 characters, got %d", len(f.ClientID))
	}
	name := f.Name
	if name == "" {
		name = DefaultClientName
	}
	if len(name) > maxClientName {
		return nil, fmt.Errorf("client name is %d bytes, the length prefix holds at most %d",
			len(name), maxClientName)
	}
	header, err := f.FrameHeader.Encode()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	_, _ = buf.Write(header)

	// Device frames repeat the body: the request as received, then the response.
	bodies := 1
	if f.FromDevice {
		bodies = 2
	}
	for i := range bodies {
		var body bytes.Buffer
		_, _ = body.Write(bodyPrefix(f.ClientID, name))

		list := f.Request
		if i == 1 && f.Response != nil {
			list = f.Response
		}
		if list != nil {
			_, _ = body.Write(list.Encode())
		}
		if i == 1 && len(f.Trailer) > 0 {
			_, _ = body.Write(f.Trailer)
		}

		_ = binary.Write(&buf, binary.BigEndian, uint32(body.Len()))
		_, _ = buf.Write(body.Bytes())
	}
	return buf.Bytes(), nil
}

// StatusFrame is state the device pushes without being asked, plus the replies
// to the query commands.
type StatusFrame struct {
	FrameHeader
	Fields *FieldList
}

func (f *StatusFrame) isFrame() {}

func (f *StatusFrame) String() string {
	s := "status[" + f.FrameHeader.String()
	if f.Fields != nil {
		s += " " + f.Fields.String()
	}
	return s + "]"
}

// Command returns the status frame's discriminator.
func (f *StatusFrame) Command() Command {
	if f.Fields == nil {
		return 0
	}
	return f.Fields.Command
}

func (f *StatusFrame) Encode() ([]byte, error) {
	header, err := f.FrameHeader.Encode()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	_, _ = buf.Write(header)
	_, _ = buf.Write([]byte{0x00, 0x00, 0x00, 0x00})
	_, _ = buf.Write([]byte{0x00})

	var body bytes.Buffer
	if f.Fields != nil {
		_, _ = body.Write(f.Fields.Encode())
	}
	_ = binary.Write(&buf, binary.BigEndian, uint32(body.Len()))
	_, _ = buf.Write(body.Bytes())
	return buf.Bytes(), nil
}

// Database transfer frames. These are single-byte opcodes outside the control
// frame format, exchanged while streaming the song database.
const (
	dbChunkID    byte = 0x4B
	dbReadyID    byte = 0x84
	dbStartID    byte = 0x85
	dbContinueID byte = 0x24
	dbFinishID   byte = 0x7F
	dbInfoID     byte = 0x22
)

// DatabaseFrame is one step of the database transfer handshake. Payload carries
// the bytes that follow the opcode, which only some steps have.
type DatabaseFrame struct {
	ID      byte
	Payload []byte
}

func (f *DatabaseFrame) isFrame() {}

func (f *DatabaseFrame) String() string {
	name := map[byte]string{
		dbReadyID: "ready", dbStartID: "start", dbContinueID: "continue",
		dbFinishID: "finish", dbInfoID: "info",
	}[f.ID]
	if name == "" {
		name = fmt.Sprintf("%#02x", f.ID)
	}
	return fmt.Sprintf("database-%s[%d bytes]", name, len(f.Payload))
}

func (f *DatabaseFrame) Encode() ([]byte, error) {
	return append([]byte{f.ID}, f.Payload...), nil
}

// DatabaseChunkFrame is a slab of the song database, still carrying its header.
//
// The header is opaque and not a fixed size: typically 72 bytes on the first
// chunk and 75 on later ones, with no field in it encoding the payload length.
// So this layer does not split header from payload; doing that needs the SQLite
// header and the running byte total, which is catalog.Reassembler's job.
type DatabaseChunkFrame struct {
	// Raw is the whole frame, including the leading id and the opaque header.
	Raw []byte
}

func (f *DatabaseChunkFrame) isFrame() {}

func (f *DatabaseChunkFrame) String() string {
	return fmt.Sprintf("database-chunk[%d bytes raw]", len(f.Raw))
}

// Encode returns the frame unchanged, since it was never decomposed.
func (f *DatabaseChunkFrame) Encode() ([]byte, error) {
	if len(f.Raw) == 0 {
		return nil, errors.New("database chunk has no bytes")
	}
	return f.Raw, nil
}
