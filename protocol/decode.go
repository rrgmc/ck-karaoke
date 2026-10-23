package protocol

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Decode parses bytes straight to a Message, skipping the intermediate Frame.
func Decode(data []byte, fromDevice bool) (Message, error) {
	frame, err := ParseFrame(data, fromDevice)
	if err != nil {
		return nil, err
	}
	return ParseMessage(frame)
}

// ParseMessage interprets a Frame. It returns (nil, nil) for frames that decode
// structurally but have no semantic mapping yet — callers should fall back to
// showing the Frame.
func ParseMessage(frame Frame) (Message, error) {
	switch f := frame.(type) {
	case *CommandFrame:
		return parseCommandMessage(f)
	case *StatusFrame:
		return parseStatusMessage(f)
	case *DatabaseFrame:
		return &DatabaseStep{ID: f.ID, Payload: f.Payload}, nil
	case *DatabaseChunkFrame:
		return &DatabaseChunk{Raw: f.Raw}, nil
	default:
		return nil, fmt.Errorf("unknown frame type %T", frame)
	}
}

func parseCommandMessage(f *CommandFrame) (Message, error) {
	if f.Kind == KindSession {
		tag := f.Tag
		return &SessionStart{Seq: f.Seq, ClientID: f.ClientID, Name: f.Name, Tag: &tag}, nil
	}
	if f.Kind != KindCommand || f.Request == nil {
		return nil, nil
	}

	cmd := f.Request.Command
	if cmd == CmdAskDB && len(f.Request.Fields) >= 2 {
		return &DatabaseRequest{
			Seq: f.Seq, ClientID: f.ClientID,
			Path: f.Request.Fields[0], Name: f.Request.Fields[1],
		}, nil
	}

	// Everything else is a plain command; preserve the tag so a parsed message
	// re-encodes to the original bytes even when it differs from TagFor.
	tag := f.Tag
	return &CommandRequest{
		Seq:      f.Seq,
		ClientID: f.ClientID,
		Name:     f.Name,
		Command:  cmd,
		Args:     f.Request.Fields,
		Tag:      &tag,
	}, nil
}

// Status frame discriminators, carried in the field-list terminator.
const (
	statusPlayback Command = 0x00
	statusQueue    Command = 0x03
)

func parseStatusMessage(f *StatusFrame) (Message, error) {
	if f.Fields == nil {
		return nil, errors.New("status frame has no field list")
	}
	switch f.Fields.Command {
	case statusPlayback:
		return parsePlaybackStatus(f)
	case statusQueue:
		return parseQueue(f)
	case Command(CmdQueryVolInfo):
		return &Volumes{Seq: f.Seq, Values: f.Fields.Fields}, nil
	default:
		return nil, fmt.Errorf("unknown status discriminator %#02x", byte(f.Fields.Command))
	}
}

// Status field indices. Fields 3, 5, 7, 11 and 12 have no known meaning and are
// ignored.
const (
	fLyric1     = 0
	fLyric2     = 1
	fPosition   = 2
	fSongName   = 4
	fNextSong   = 6
	fSongID     = 8
	fHighlight1 = 9
	fHighlight2 = 10
	fDatabaseID = 13
	fMusicVol   = 14
	fMicVol     = 15
	fEcho       = 16
	fKey        = 17
	fTempo      = 18

	statusFieldCount = 19
)

func parsePlaybackStatus(f *StatusFrame) (Message, error) {
	fields := f.Fields.Fields
	if len(fields) != statusFieldCount {
		return nil, fmt.Errorf("status must have %d fields, got %d", statusFieldCount, len(fields))
	}

	posMS, err := strconv.Atoi(fields[fPosition])
	if err != nil {
		return nil, fmt.Errorf("status position %q: %w", fields[fPosition], err)
	}

	s := &Status{
		Seq:           f.Seq,
		SongPosition:  time.Duration(posMS) * time.Millisecond,
		CurrentSong:   fields[fSongName],
		CurrentSongID: fields[fSongID],
		NextSong:      fields[fNextSong],
		DatabaseID:    fields[fDatabaseID],
		MusicVolume:   optInt(fields[fMusicVol], -1),
		MicVolume:     optInt(fields[fMicVol], -1),
		Echo:          optInt(fields[fEcho], -1),
		Key:           optInt(fields[fKey], -1),
	}

	// Tempo arrives as a per-mille duration ratio centred on 1000, one step per
	// 1000/30. Higher means longer, i.e. slower, so the sign is flipped to match
	// what the user asked for: SetTempo(-1) takes the raw value to 1034, and that
	// must read as -1 rather than +1.
	//
	// Round rather than truncate. A step is 33.33 per-mille but the device
	// reports it whole, so one step up lands on 967: 0.99 steps, which
	// truncation would turn into 0.
	if raw := optInt(fields[fTempo], 0); raw != 0 {
		s.Tempo = int(math.Round(float64(TempoScale-raw) * 30 / TempoScale))
	}

	for i := range 2 {
		line, pct := fields[fLyric1+i], fields[fHighlight1+i]
		if line == "" {
			continue
		}
		s.Lyrics = append(s.Lyrics, line)
		s.Highlight = append(s.Highlight, optInt(pct, -1))
	}

	return s, nil
}

func parseQueue(f *StatusFrame) (Message, error) {
	fields := f.Fields.Fields
	if len(fields) < 3 {
		return nil, fmt.Errorf("queue needs at least 3 fields, got %d", len(fields))
	}
	total, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil, fmt.Errorf("queue total %q: %w", fields[0], err)
	}
	start, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, fmt.Errorf("queue start %q: %w", fields[1], err)
	}
	end, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("queue end %q: %w", fields[2], err)
	}
	if end < start {
		return nil, fmt.Errorf("queue end %d before start %d", end, start)
	}

	q := &Queue{Seq: f.Seq, Total: total, Start: start, End: end}
	// Entries follow as groups of four: the song id and three values; see QueueEntry.
	const groupSize = 4
	pos := 3
	for range end - start {
		if pos+groupSize > len(fields) {
			return nil, errors.New("queue truncated mid-entry")
		}
		q.Entries = append(q.Entries, QueueEntry{
			SongID: fields[pos],
			Fields: fields[pos+1 : pos+groupSize],
		})
		pos += groupSize
	}
	return q, nil
}

// optInt parses a status field that may be blank, in which case it means "not
// applicable" rather than zero.
func optInt(s string, blank int) int {
	if s == "" {
		return blank
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return blank
	}
	return n
}
