package protocol

import "fmt"

// Command is a command id: the value that terminates the field list of a
// command frame (class 0x50, kind 0x02).
type Command byte

// The command table.
//
// The first group is what this library sends and has verified against the
// device. The second group is defined by the device but not used here. Several
// of those change stored state or reach external services (the YouTube,
// recording, UploadPic, AddBarrage and Cheer families), so treat them with care.
const (
	CmdAskDB        Command = 0x01 // request the song database
	CmdAddOrder     Command = 0x02 // queue a song
	CmdDelOrder     Command = 0x03 // remove a song from the queue
	CmdPause        Command = 0x05 // pause / resume
	CmdStop         Command = 0x06 // stop (NextSong resumes)
	CmdNextSong     Command = 0x07 // start the next queued song
	CmdQueryorder   Command = 0x0A // query the reserved list
	CmdSetKey       Command = 0x0B // change key
	CmdSetTempo     Command = 0x0C // change tempo
	CmdSetVolume    Command = 0x0D // music volume
	CmdSetMic1Vol   Command = 0x0E // microphone 1 volume
	CmdSetMic2Vol   Command = 0x0F // microphone 2 volume
	CmdResetTempo   Command = 0x11 // tempo back to normal
	CmdResetKey     Command = 0x12 // key back to the original
	CmdQueryVolInfo Command = 0x13 // query all volume/tone settings
	CmdFirstOrder   Command = 0x15 // queue a song at the front
	CmdRepeatFlag   Command = 0x18 // repeat current song
	CmdPlayEx       Command = 0x1C // play a song immediately
	CmdSetReverbVol Command = 0x26 // reverb/echo level
	CmdSkip         Command = 0x2C // skip the current song
	CmdASKRECCOUNT  Command = 0x2E // number of stored recordings

	// CmdSetMelody toggles the guide melody. Its high bit means the field list
	// terminator carries an extra byte; see ExtraFor.
	CmdSetMelody Command = 0xF8

	// Defined by the device, not used by this library.
	CmdPlay                 Command = 0x04
	CmdSetMusicvol          Command = 0x10
	CmdAskRecFile           Command = 0x16
	CmdAskRecList           Command = 0x17
	CmdUpOrder              Command = 0x1D
	CmdDownOrder            Command = 0x1E
	CmdChangeNickName       Command = 0x25
	CmdSetVocalMode         Command = 0x28
	CmdAskRecListHTTPInf    Command = 0x2F
	CmdAddBarrage           Command = 0x30
	CmdCheer                Command = 0x31
	CmdUploadPic            Command = 0x32
	CmdAddYouTubeOrder      Command = 0x65
	CmdFirstYouTubeOrder    Command = 0x66
	CmdTubeDownload         Command = 0x67
	CmdGetDownloadListCount Command = 0x68
	CmdAskDownloadList      Command = 0x69
)

// commandNames maps ids to the device's own command names, for logging.
var commandNames = map[Command]string{
	CmdAskDB: "AskDB", CmdAddOrder: "AddOrder", CmdDelOrder: "DelOrder",
	CmdPlay: "Play", CmdPause: "Pause", CmdStop: "Stop", CmdNextSong: "NextSong",
	CmdQueryorder: "Queryorder", CmdSetKey: "SetKey", CmdSetTempo: "SetTempo",
	CmdSetVolume: "SetVolume", CmdSetMic1Vol: "SetMic1Vol", CmdSetMic2Vol: "SetMic2Vol",
	CmdSetMusicvol: "SetMusicvol", CmdResetTempo: "ResetTempo", CmdResetKey: "ResetKey",
	CmdQueryVolInfo: "QueryVolInfo", CmdFirstOrder: "FirstOrder", CmdAskRecFile: "AskRecFile",
	CmdAskRecList: "AskRecList", CmdRepeatFlag: "RepeatFlag", CmdPlayEx: "PlayEx",
	CmdUpOrder: "UpOrder", CmdDownOrder: "DownOrder", CmdChangeNickName: "ChangeNickName",
	CmdSetReverbVol: "SetReverbVol", CmdSetVocalMode: "SetVocalMode", CmdSkip: "Skip",
	CmdSetMelody:   "SetMelody",
	CmdASKRECCOUNT: "ASKRECCOUNT", CmdAskRecListHTTPInf: "AskRecList_HttpInf",
	CmdAddBarrage: "AddBarrage", CmdCheer: "Cheer", CmdUploadPic: "UploadPic",
	CmdAddYouTubeOrder: "AddYouTubeOrder", CmdFirstYouTubeOrder: "FirstYouTubeOrder",
	CmdTubeDownload: "TubeDownload", CmdGetDownloadListCount: "GetDownloadListCount",
	CmdAskDownloadList: "AskDownloadList",
}

func (c Command) String() string {
	if n, ok := commandNames[c]; ok {
		return n
	}
	return fmt.Sprintf("Command(%#02x)", byte(c))
}

// commandTags is the four-byte tag the frame header carries for each command.
// Most read as a little-endian fragment of the command's name (`_Add` for
// AddOrder, `_Que` for the query family); a few are not ASCII (Pause sends
// zeros, and four commands send UTF-16 "Se").
//
// The device accepts a frame with the wrong tag, so nothing fails loudly if
// this table is wrong. TestCommandTagsMatchGolden holds it to the reference
// encodings.
var commandTags = map[Command][4]byte{
	CmdAskDB:        {0x6b, 0x73, 0x41, 0x5f}, // _Ask
	CmdAddOrder:     {0x64, 0x64, 0x41, 0x5f}, // _Add
	CmdPause:        {0x00, 0x00, 0x00, 0x00},
	CmdStop:         {0x00, 0x65, 0x00, 0x53}, // UTF-16 "Se"
	CmdNextSong:     {0x78, 0x65, 0x4e, 0x5f}, // _Nex
	CmdQueryorder:   {0x65, 0x75, 0x51, 0x5f}, // _Que
	CmdSetKey:       {0x00, 0x65, 0x00, 0x53}, // UTF-16 "Se"
	CmdSetTempo:     {0x74, 0x65, 0x53, 0x5f}, // _Set
	CmdSetVolume:    {0x74, 0x65, 0x53, 0x5f}, // _Set
	CmdSetMic1Vol:   {0x74, 0x65, 0x53, 0x5f}, // _Set
	CmdQueryVolInfo: {0x65, 0x75, 0x51, 0x5f}, // _Que
	CmdFirstOrder:   {0x72, 0x69, 0x46, 0x5f}, // _Fir
	CmdRepeatFlag:   {0x69, 0x77, 0x53, 0x5f}, // _Swi
	CmdPlayEx:       {0x00, 0x65, 0x00, 0x53}, // UTF-16 "Se"
	CmdSetReverbVol: {0x74, 0x65, 0x53, 0x5f}, // _Set
	CmdSkip:         {0x00, 0x65, 0x00, 0x53}, // UTF-16 "Se"
	CmdASKRECCOUNT:  {0x65, 0x75, 0x51, 0x5f}, // _Que
	CmdSetMelody:    {0x74, 0x65, 0x53, 0x5f}, // _Set
}

// defaultCommandTag is used for commands without an entry above. `_Que` is the
// most common tag, and the device tolerates a mismatch.
var defaultCommandTag = [4]byte{0x65, 0x75, 0x51, 0x5f}

// TagFor returns the header tag for a command.
func TagFor(c Command) [4]byte {
	if t, ok := commandTags[c]; ok {
		return t
	}
	return defaultCommandTag
}

// commandExtras holds the trailing byte that follows the command id in a field
// list when the id has its high bit set. CmdSetMelody (0xF8) is the only such
// command; the meaning of its extra byte (0x06) is unknown.
var commandExtras = map[Command]byte{
	CmdSetMelody: 0x06,
}

// ExtraFor returns the trailing terminator byte for a command and whether one is
// required. A command id with the high bit set always carries one.
func ExtraFor(c Command) (byte, bool) {
	if c&0x80 == 0 {
		return 0, false
	}
	b, ok := commandExtras[c]
	return b, ok
}
