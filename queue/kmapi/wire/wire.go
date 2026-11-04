// Package wire holds the JSON types of KaraokeMachine's API
// (crates/machine/km-api/src/dto.rs), shared by the queue manager's server
// and the remote's client.
//
// KM Remote deserialises these with serde, so a missing required field or a
// wrong type fails the whole response without an error the person sees. Every
// Rust Option is a pointer here and is written as null, never left out, unless
// the Rust side skips it. The golden files in queue/kmapi/testdata pin the
// shapes.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrNotNumber reports a song number that is not a string of digits from 1
// to 9,999,999.
var ErrNotNumber = errors.New("not a song number")

// Level is an access level. The levels are ordered: a caller may do whatever
// its level or a lower one allows.
type Level int

const (
	// LevelView reads the catalogue, the queue and the state.
	LevelView Level = iota
	// LevelRequest registers a singer, names one, asks for a song and withdraws one.
	LevelRequest
	// LevelQueue adds a song with no singer and changes key, tempo and volume.
	LevelQueue
	// LevelControl interrupts: transport, moving or removing any entry, clearing.
	LevelControl
	// LevelHost runs the night: accepts and declines requests, acts for a singer,
	// creates, removes and merges singers, opens and closes requests, starts
	// a new evening.
	LevelHost
	// LevelAdmin is never granted by the queue manager; it has no admin routes.
	LevelAdmin
)

var levelNames = []string{"view", "request", "queue", "control", "host", "admin"}

func (l Level) String() string {
	if l < 0 || int(l) >= len(levelNames) {
		return "view"
	}
	return levelNames[l]
}

// ParseLevel reads a level's name.
func ParseLevel(s string) (Level, error) {
	for i, n := range levelNames {
		if n == s {
			return Level(i), nil
		}
	}
	return LevelView, fmt.Errorf("unknown access level %q", s)
}

// MarshalJSON writes the level's name.
func (l Level) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// UnmarshalJSON reads a level's name.
func (l *Level) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := ParseLevel(s)
	*l = v
	return err
}

// Number is a song number. It travels as a JSON string of digits.
type Number uint32

// MarshalJSON writes the number as a string.
func (n Number) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatUint(uint64(n), 10) + `"`), nil
}

// UnmarshalJSON reads a string of digits from 1 to 9,999,999.
func (n *Number) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return ErrNotNumber
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || v == 0 || v > 9_999_999 {
		return ErrNotNumber
	}
	*n = Number(v)
	return nil
}

type Transport string

type Origin struct {
	Kind    string  `json:"kind"`
	Number  *Number `json:"number,omitempty"`
	EntryID *uint64 `json:"entry_id,omitempty"`
	Path    *string `json:"path,omitempty"`
}

type NowPlaying struct {
	Origin             Origin  `json:"origin"`
	Title              string  `json:"title"`
	Artist             *string `json:"artist"`
	Language           *string `json:"language"`
	Singer             *string `json:"singer"`
	Kind               string  `json:"kind"`
	DurationMS         uint32  `json:"duration_ms"`
	MelodyChannel      *uint8  `json:"melody_channel"`
	MelodyAvailable    bool    `json:"melody_available"`
	TransposeAvailable bool    `json:"transpose_available"`
	TempoAvailable     bool    `json:"tempo_available"`
	HasLyrics          bool    `json:"has_lyrics"`
}

type Settings struct {
	Transpose     int     `json:"transpose"`
	TempoRatio    float64 `json:"tempo_ratio"`
	MelodyEnabled bool    `json:"melody_enabled"`
	MusicVolume   float64 `json:"music_volume"`
	LyricOffsetMS int     `json:"lyric_offset_ms"`
}

type SettingsPatch struct {
	Transpose     *int     `json:"transpose"`
	TempoRatio    *float64 `json:"tempo_ratio"`
	MelodyEnabled *bool    `json:"melody_enabled"`
	MusicVolume   *float64 `json:"music_volume"`
	LyricOffsetMS *int     `json:"lyric_offset_ms"`
}

type State struct {
	Transport  Transport   `json:"transport"`
	NowPlaying *NowPlaying `json:"now_playing"`
	PositionMS uint32      `json:"position_ms"`
	QueueLen   int         `json:"queue_len"`
	Settings   Settings    `json:"settings"`
	// PlayerOnline is false while the unit is not connected, so a remote
	// says the player is away rather than the server.
	PlayerOnline bool `json:"player_online"`
}

type QueueEntry struct {
	ID         uint64  `json:"id"`
	Position   int     `json:"position"`
	Number     Number  `json:"number"`
	Title      string  `json:"title"`
	Artist     *string `json:"artist"`
	Singer     *string `json:"singer"`
	DurationMS uint32  `json:"duration_ms"`
	AheadMS    *uint64 `json:"ahead_ms"`
	Seat       *uint64 `json:"seat"`
}

type Queue struct {
	Entries  []QueueEntry `json:"entries"`
	Len      int          `json:"len"`
	Capacity int          `json:"capacity"`
}

type AddToQueue struct {
	Number Number `json:"number"`
}

type AddedToQueue struct {
	EntryID  uint64  `json:"entry_id"`
	Position int     `json:"position"`
	Title    string  `json:"title"`
	Artist   *string `json:"artist"`
}

type Move struct {
	ToIndex int `json:"to_index"`
}

type Singer struct {
	ID    uint64  `json:"id"`
	Name  *string `json:"name"`
	Guest *uint32 `json:"guest"`
	Phone bool    `json:"phone"`
}

type Singers struct {
	Singers  []Singer `json:"singers"`
	Capacity int      `json:"capacity"`
	Evening  string   `json:"evening"`
}

type RegisterSinger struct {
	Device string  `json:"device"`
	Name   *string `json:"name"`
}

type SingerIssued struct {
	Singer  Singer `json:"singer"`
	Token   string `json:"token"`
	Evening string `json:"evening"`
}

type RenameSinger struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

type CreateSinger struct {
	Name *string `json:"name"`
}

type MergeSinger struct {
	Into uint64 `json:"into"`
}

type Request struct {
	ID         uint64  `json:"id"`
	Singer     uint64  `json:"singer"`
	Number     Number  `json:"number"`
	Title      string  `json:"title"`
	Artist     *string `json:"artist"`
	DurationMS uint32  `json:"duration_ms"`
	State      string  `json:"state"`
	EntryID    *uint64 `json:"entry_id"`
	Position   *int    `json:"position"`
}

type Sung struct {
	Number Number  `json:"number"`
	Singer *uint64 `json:"singer"`
}

type Requests struct {
	Requests       []Request `json:"requests"`
	Capacity       int       `json:"capacity"`
	Accepting      bool      `json:"accepting"`
	Sung           []Sung    `json:"sung"`
	WaitingForHost bool      `json:"waiting_for_host"`
}

type AddRequest struct {
	Singer uint64 `json:"singer"`
	Token  string `json:"token"`
	Number Number `json:"number"`
}

type AddSingerRequest struct {
	Number Number `json:"number"`
}

type Withdraw struct {
	Token string `json:"token"`
}

type Accept struct {
	Next bool `json:"next"`
}

type Accepting struct {
	Accepting bool `json:"accepting"`
}

type Login struct {
	Password string `json:"password"`
}

type AccessGrant struct {
	Token         string `json:"token"`
	ExpiresInSecs uint64 `json:"expires_in_secs"`
	Access        Level  `json:"access"`
}

type Access struct {
	Access      Level `json:"access"`
	Room        Level `json:"room"`
	QueueCode   bool  `json:"queue_code"`
	ControlCode bool  `json:"control_code"`
	HostCode    bool  `json:"host_code"`
}

type Discovery struct {
	V               uint32   `json:"v"`
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	App             string   `json:"app"`
	Version         string   `json:"version"`
	API             string   `json:"api"`
	FactoryPassword bool     `json:"factory_password"`
	Port            uint16   `json:"port"`
	URLs            []string `json:"urls"`
	Reachable       bool     `json:"reachable"`
	Problem         *struct {
		Kind string `json:"kind"`
	} `json:"problem"`
	SongCount      *int    `json:"song_count"`
	CatalogVersion *uint64 `json:"catalog_version"`
	DebugEnabled   bool    `json:"debug_enabled"`
	// Features lists the optional parts of the API a remote may offer. A
	// remote that predates it reads every flag as true.
	Features Features `json:"features"`
}

type Features struct {
	// Demo: POST /demo/start plays a song of the server's choosing.
	Demo bool `json:"demo"`
	// SongBook: GET /songs/book.pdf answers with a song book.
	SongBook bool `json:"song_book"`
	// WebControl: the server's own address serves a page to control it.
	WebControl bool `json:"web_control"`
}

type Song struct {
	Number           Number  `json:"number"`
	Title            string  `json:"title"`
	Artist           *string `json:"artist"`
	Language         *string `json:"language"`
	Kind             string  `json:"kind"`
	DurationMS       uint32  `json:"duration_ms"`
	Suitability      *uint8  `json:"suitability"`
	MelodyAvailable  bool    `json:"melody_available"`
	DefaultTranspose int     `json:"default_transpose"`
	PackageID        string  `json:"package_id"`
}

type Package struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	SongCount   int      `json:"song_count"`
	InstalledAt string   `json:"installed_at"`
	Bank        uint16   `json:"bank"`
	Removable   bool     `json:"removable"`
	Flags       uint32   `json:"flags"`
	FlagNames   []string `json:"flag_names"`
}

type Packages struct {
	Packages  []Package `json:"packages"`
	SongCount int       `json:"song_count"`
}

type Error struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// OptString is a Rust Option<String>: nil for an empty string.
func OptString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
