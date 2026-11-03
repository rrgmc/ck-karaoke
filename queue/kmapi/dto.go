package kmapi

import "strconv"

// The wire types of KaraokeMachine's API (crates/machine/km-api/src/dto.rs).
// KM Remote deserialises these with serde, so a missing required field or a
// wrong type fails the whole response without an error the person sees. Every
// Rust Option is a pointer here and is written as null, never left out, unless
// the Rust side skips it. The golden files in testdata pin the shapes.

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
		return errNotNumber
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || v == 0 || v > 9_999_999 {
		return errNotNumber
	}
	*n = Number(v)
	return nil
}

type transportDTO string

type originDTO struct {
	Kind    string  `json:"kind"`
	Number  *Number `json:"number,omitempty"`
	EntryID *uint64 `json:"entry_id,omitempty"`
	Path    *string `json:"path,omitempty"`
}

type nowPlayingDTO struct {
	Origin             originDTO `json:"origin"`
	Title              string    `json:"title"`
	Artist             *string   `json:"artist"`
	Language           *string   `json:"language"`
	Singer             *string   `json:"singer"`
	Kind               string    `json:"kind"`
	DurationMS         uint32    `json:"duration_ms"`
	MelodyChannel      *uint8    `json:"melody_channel"`
	MelodyAvailable    bool      `json:"melody_available"`
	TransposeAvailable bool      `json:"transpose_available"`
	TempoAvailable     bool      `json:"tempo_available"`
	HasLyrics          bool      `json:"has_lyrics"`
}

type settingsDTO struct {
	Transpose     int     `json:"transpose"`
	TempoRatio    float64 `json:"tempo_ratio"`
	MelodyEnabled bool    `json:"melody_enabled"`
	MusicVolume   float64 `json:"music_volume"`
	LyricOffsetMS int     `json:"lyric_offset_ms"`
}

type settingsPatchDTO struct {
	Transpose     *int     `json:"transpose"`
	TempoRatio    *float64 `json:"tempo_ratio"`
	MelodyEnabled *bool    `json:"melody_enabled"`
	MusicVolume   *float64 `json:"music_volume"`
	LyricOffsetMS *int     `json:"lyric_offset_ms"`
}

type stateDTO struct {
	Transport  transportDTO   `json:"transport"`
	NowPlaying *nowPlayingDTO `json:"now_playing"`
	PositionMS uint32         `json:"position_ms"`
	QueueLen   int            `json:"queue_len"`
	Settings   settingsDTO    `json:"settings"`
	// PlayerOnline is false while the unit is not connected, so a remote
	// says the player is away rather than the server.
	PlayerOnline bool `json:"player_online"`
}

type queueEntryDTO struct {
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

type queueDTO struct {
	Entries  []queueEntryDTO `json:"entries"`
	Len      int             `json:"len"`
	Capacity int             `json:"capacity"`
}

type addToQueueRequest struct {
	Number Number `json:"number"`
}

type addedToQueueDTO struct {
	EntryID  uint64  `json:"entry_id"`
	Position int     `json:"position"`
	Title    string  `json:"title"`
	Artist   *string `json:"artist"`
}

type moveRequest struct {
	ToIndex int `json:"to_index"`
}

type singerDTO struct {
	ID    uint64  `json:"id"`
	Name  *string `json:"name"`
	Guest *uint32 `json:"guest"`
	Phone bool    `json:"phone"`
}

type singersDTO struct {
	Singers  []singerDTO `json:"singers"`
	Capacity int         `json:"capacity"`
	Evening  string      `json:"evening"`
}

type registerSingerRequest struct {
	Device string  `json:"device"`
	Name   *string `json:"name"`
}

type singerIssuedDTO struct {
	Singer  singerDTO `json:"singer"`
	Token   string    `json:"token"`
	Evening string    `json:"evening"`
}

type renameSingerRequest struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

type createSingerRequest struct {
	Name *string `json:"name"`
}

type mergeSingerRequest struct {
	Into uint64 `json:"into"`
}

type requestDTO struct {
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

type sungDTO struct {
	Number Number  `json:"number"`
	Singer *uint64 `json:"singer"`
}

type requestsDTO struct {
	Requests       []requestDTO `json:"requests"`
	Capacity       int          `json:"capacity"`
	Accepting      bool         `json:"accepting"`
	Sung           []sungDTO    `json:"sung"`
	WaitingForHost bool         `json:"waiting_for_host"`
}

type addRequest struct {
	Singer uint64 `json:"singer"`
	Token  string `json:"token"`
	Number Number `json:"number"`
}

type addSingerRequest struct {
	Number Number `json:"number"`
}

type withdrawRequest struct {
	Token string `json:"token"`
}

type acceptRequest struct {
	Next bool `json:"next"`
}

type acceptingRequest struct {
	Accepting bool `json:"accepting"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type accessGrantDTO struct {
	Token         string `json:"token"`
	ExpiresInSecs uint64 `json:"expires_in_secs"`
	Access        Level  `json:"access"`
}

type accessDTO struct {
	Access      Level `json:"access"`
	Room        Level `json:"room"`
	QueueCode   bool  `json:"queue_code"`
	ControlCode bool  `json:"control_code"`
	HostCode    bool  `json:"host_code"`
}

type discoveryDTO struct {
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
	Features featuresDTO `json:"features"`
}

type featuresDTO struct {
	// Demo: POST /demo/start plays a song of the server's choosing.
	Demo bool `json:"demo"`
	// SongBook: GET /songs/book.pdf answers with a song book.
	SongBook bool `json:"song_book"`
	// WebControl: the server's own address serves a page to control it.
	WebControl bool `json:"web_control"`
}

type songDTO struct {
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

type packageDTO struct {
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

type packagesDTO struct {
	Packages  []packageDTO `json:"packages"`
	SongCount int          `json:"song_count"`
}

type errorDTO struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// optString is a Rust Option<String>: nil for an empty string.
func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
