package protocol

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Message is a decoded application message. Every message can render itself back
// to a Frame, and from there to bytes.
type Message interface {
	// Frame returns the wire form.
	Frame() Frame
	// Encode renders the message to bytes.
	Encode() ([]byte, error)
	String() string
}

// SessionStart opens a session. It is the one step the device requires before it
// will accept commands or push status.
type SessionStart struct {
	Seq      uint32
	ClientID string
	// Name is shown on the device's screen; empty uses DefaultClientName.
	Name string
	// Tag overrides the frame's checksum, which is otherwise computed from the
	// body. Only useful in tests.
	Tag *[4]byte
}

func (m *SessionStart) String() string { return fmt.Sprintf("session-start seq=%d", m.Seq) }

func (m *SessionStart) Frame() Frame {
	tag := SessionChecksum(m.ClientID, m.Name)
	if m.Tag != nil {
		tag = *m.Tag
	}
	return &CommandFrame{
		FrameHeader: FrameHeader{Class: ClassControl, Kind: KindSession, Seq: m.Seq, Tag: tag},
		ClientID:    m.ClientID,
		Name:        m.Name,
	}
}

func (m *SessionStart) Encode() ([]byte, error) { return m.Frame().Encode() }

// CommandRequest is any command sent to the device. One type covers all of them:
// commands differ only in their id and their string arguments.
type CommandRequest struct {
	Seq      uint32
	ClientID string
	Command  Command
	Args     []string
	// Name is shown on the device's screen; empty uses DefaultClientName.
	Name string
	// Tag overrides the per-command header tag; nil uses TagFor.
	Tag *[4]byte
}

// NewCommandRequest builds a command with the arguments given.
func NewCommandRequest(seq uint32, clientID string, cmd Command, args ...string) *CommandRequest {
	return &CommandRequest{Seq: seq, ClientID: clientID, Command: cmd, Args: args}
}

func (m *CommandRequest) String() string {
	if len(m.Args) == 0 {
		return fmt.Sprintf("%s seq=%d", m.Command, m.Seq)
	}
	return fmt.Sprintf("%s(%s) seq=%d", m.Command, joinQuoted(m.Args, ", "), m.Seq)
}

func (m *CommandRequest) Frame() Frame {
	tag := TagFor(m.Command)
	if m.Tag != nil {
		tag = *m.Tag
	}
	return &CommandFrame{
		FrameHeader: FrameHeader{Class: ClassControl, Kind: KindCommand, Seq: m.Seq, Tag: tag},
		ClientID:    m.ClientID,
		Name:        m.Name,
		Request:     NewFieldList(m.Command, m.Args...),
	}
}

func (m *CommandRequest) Encode() ([]byte, error) { return m.Frame().Encode() }

// OrderToken generates the second argument the queueing commands carry. Its
// meaning is unknown. Conventional values are 8-digit numbers around 84 to 87
// million, and the device accepts any value.
func OrderToken() string {
	// Stay in the conventional range rather than sending a wall-clock value.
	const base = 84_000_000
	return strconv.Itoa(base + int(time.Now().UnixMilli()%3_000_000))
}

// Status is the device's periodic state broadcast.
type Status struct {
	Seq uint32

	// Playback.
	SongPosition  time.Duration
	CurrentSong   string
	CurrentSongID string
	NextSong      string

	// Controls. Key is in semitones and Tempo in steps either side of normal,
	// both positive for higher/faster.
	Key         int
	Tempo       int
	MusicVolume int
	MicVolume   int
	Echo        int

	// Lyrics holds up to two on-screen lines, with Highlight giving each line's
	// karaoke sweep as a percentage.
	Lyrics    []string
	Highlight []int

	// DatabaseID is the song database identifier.
	DatabaseID string
}

// TempoScale is the per-mille unit the device reports tempo in: 1000 is normal
// and one step is 1000/30. The raw value is a duration ratio, so it rises as the
// tempo slows — 934/967/1000/1034/1067 correspond to +2..-2.
const TempoScale = 1000

func (m *Status) String() string {
	song := m.CurrentSong
	if song == "" {
		song = "(none)"
	} else {
		song = strconv.Quote(song)
	}
	s := fmt.Sprintf("status song=%s", song)
	if m.CurrentSongID != "" {
		s += fmt.Sprintf(" [%s]", m.CurrentSongID)
	}
	s += fmt.Sprintf(" pos=%s key=%d tempo=%d vol=%d mic=%d echo=%d",
		m.SongPosition.Round(time.Second), m.Key, m.Tempo, m.MusicVolume, m.MicVolume, m.Echo)
	if m.NextSong != "" {
		s += fmt.Sprintf(" next=%q", m.NextSong)
	}
	for i, line := range m.Lyrics {
		pct := -1
		if i < len(m.Highlight) {
			pct = m.Highlight[i]
		}
		s += fmt.Sprintf(" lyric%d=%q@%d%%", i+1, line, pct)
	}
	return s
}

func (m *Status) Frame() Frame {
	return &StatusFrame{FrameHeader: FrameHeader{
		Class: ClassControl, Kind: KindStatus, Seq: m.Seq, FromDevice: true,
	}}
}

func (m *Status) Encode() ([]byte, error) { return m.Frame().Encode() }

// QueueEntry is one reserved song.
type QueueEntry struct {
	SongID string
	// Fields are the three trailing values. The middle one names the client
	// that added the song, as "<counter>.<name>" — see QueuedBy.
	Fields []string
}

// QueuedBy is the name of the client that reserved this song, or empty if the
// entry does not carry one.
//
// The device stores it as "<counter>.<name>", where the counter increments per
// entry and the name is the one that client's *session* announced. It is not
// taken from the command frame: reserving with a different name in the AddOrder
// body still records the session's, which is why every song queued through one
// server carries that server's name.
func (e QueueEntry) QueuedBy() string {
	if len(e.Fields) < 2 {
		return ""
	}
	if _, name, ok := strings.Cut(e.Fields[1], "."); ok {
		return name
	}
	return e.Fields[1]
}

// Queue is the reserved-song list, the reply to CmdQueryorder.
type Queue struct {
	Seq     uint32
	Total   int
	Start   int
	End     int
	Entries []QueueEntry
}

func (m *Queue) String() string {
	if len(m.Entries) == 0 {
		return fmt.Sprintf("queue empty (total=%d)", m.Total)
	}
	ids := make([]string, len(m.Entries))
	for i, e := range m.Entries {
		ids[i] = e.SongID
	}
	return fmt.Sprintf("queue total=%d showing=%d-%d songs=[%s]",
		m.Total, m.Start, m.End, joinQuoted(ids, ", "))
}

func (m *Queue) Frame() Frame {
	return &StatusFrame{FrameHeader: FrameHeader{
		Class: ClassControl, Kind: KindStatus, Seq: m.Seq, FromDevice: true,
	}}
}

func (m *Queue) Encode() ([]byte, error) { return m.Frame().Encode() }

// Volumes is the reply to CmdQueryVolInfo: the device's full mixer state.
type Volumes struct {
	Seq uint32
	// Values is the raw reply. The field order is only partly known; a typical
	// reply is ["0","1000","75","50","50","75","50","50","50","0","0"].
	Values []string
}

func (m *Volumes) String() string {
	return fmt.Sprintf("volumes %s", joinQuoted(m.Values, ", "))
}

func (m *Volumes) Frame() Frame {
	return &StatusFrame{FrameHeader: FrameHeader{
		Class: ClassControl, Kind: KindStatus, Seq: m.Seq, FromDevice: true,
	}}
}

func (m *Volumes) Encode() ([]byte, error) { return m.Frame().Encode() }

// Database transfer messages.

// DefaultDatabasePath and DefaultDatabaseName locate the song database on the
// device. The path embeds DatabaseID.
const (
	DefaultDatabasePath = `/storage/emulated/0/Android/data/com.multak.cookara_br_a/files/db/` + DatabaseID
	DefaultDatabaseName = `song.db`
)

// DatabaseRequest asks the device to prepare the song database for transfer.
type DatabaseRequest struct {
	Seq      uint32
	ClientID string
	Path     string
	Name     string
}

func (m *DatabaseRequest) String() string {
	return fmt.Sprintf("database-request %s/%s seq=%d", m.Path, m.Name, m.Seq)
}

func (m *DatabaseRequest) Frame() Frame {
	return (&CommandRequest{
		Seq: m.Seq, ClientID: m.ClientID, Command: CmdAskDB,
		Args: []string{m.Path, m.Name},
	}).Frame()
}

func (m *DatabaseRequest) Encode() ([]byte, error) { return m.Frame().Encode() }

// DatabaseStep is one of the bare opcodes that drive the transfer.
type DatabaseStep struct {
	ID      byte
	Payload []byte
}

// The transfer steps, in the order a client sends them.
func NewDatabaseStart() *DatabaseStep { return &DatabaseStep{ID: dbStartID} }
func NewDatabaseContinue() *DatabaseStep {
	return &DatabaseStep{ID: dbContinueID, Payload: []byte{0x00, 0x00}}
}
func NewDatabaseFinish() *DatabaseStep { return &DatabaseStep{ID: dbFinishID} }

func (m *DatabaseStep) String() string {
	return (&DatabaseFrame{ID: m.ID, Payload: m.Payload}).String()
}
func (m *DatabaseStep) Frame() Frame            { return &DatabaseFrame{ID: m.ID, Payload: m.Payload} }
func (m *DatabaseStep) Encode() ([]byte, error) { return m.Frame().Encode() }

// DatabaseChunk is a slab of the database as it streams in, header included.
// Feed Raw to catalog.Reassembler rather than trying to strip the header here.
type DatabaseChunk struct {
	Raw []byte
}

func (m *DatabaseChunk) String() string {
	return fmt.Sprintf("database-chunk %d bytes raw", len(m.Raw))
}
func (m *DatabaseChunk) Frame() Frame            { return &DatabaseChunkFrame{Raw: m.Raw} }
func (m *DatabaseChunk) Encode() ([]byte, error) { return m.Frame().Encode() }
