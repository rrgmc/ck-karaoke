package webui

import (
	"context"
	"errors"
	"time"
)

// Backend is what the pages drive. The unit over RakNet is one backend
// (UnitBackend); the queue manager's HTTP API is another.
//
// The pages see only the types in this file, never protocol messages, so a
// backend that is not the unit can fill them in.
type Backend interface {
	// Online reports whether commands can be sent right now.
	Online() bool
	// Addr names what the backend talks to, for the banner.
	Addr() string
	// LastError is why the backend is unavailable, or nil when it is online.
	LastError() error

	// Status is the last playback state, or nil before the first one.
	Status() *Status
	// Queue is the last known queue, or nil before the first one.
	Queue() *Queue
	// RefreshQueue asks for a new queue. It arrives through OnQueue.
	RefreshQueue(ctx context.Context) error

	// Control runs one transport or mixer action. amount is the action's
	// value; mic picks the microphone for "mic".
	Control(ctx context.Context, action string, amount, mic int) error
	// SongAction queues, plays or removes one song.
	SongAction(ctx context.Context, action, songID string) error
	// EntryAction moves one queue entry ("up", "down") or removes it
	// ("remove"), by its EntryID. A backend without entry ids answers
	// ErrUnsupported.
	EntryAction(ctx context.Context, action, entryID string) error

	// Capabilities says what the pages may offer.
	Capabilities() Capabilities

	// OnStatus, OnQueue and OnConnection register the push callbacks.
	// OnConnection fires only on a change.
	OnStatus(fn func(*Status))
	OnQueue(fn func(*Queue))
	OnConnection(fn func(online bool, reason error))
}

// Capabilities says what a backend can do. The zero value is the unit's.
type Capabilities struct {
	// Requests: a song is requested for this phone's singer rather than put
	// on the queue directly, and the My songs page is offered. The backend
	// implements Singer.
	Requests bool
	// EntryIDs: queue entries carry an id and are moved and removed by it.
	EntryIDs bool
	// NoMixer: no key, tempo, volume, echo or melody controls, and no Next
	// button beside Skip.
	NoMixer bool
	// NoCutAhead: no "play next" or "play now".
	NoCutAhead bool
}

// Singer is this phone's singer at a queue manager, for the My songs page.
type Singer interface {
	// Me describes this phone's singer and their requests.
	Me(ctx context.Context) Me
	// SetName names this phone's singer.
	SetName(ctx context.Context, name string) error
	// Withdraw takes back one of this phone's requests.
	Withdraw(ctx context.Context, requestID string) error
}

// Me is this phone's singer.
type Me struct {
	// Name is empty until the singer is named.
	Name     string
	Requests []MyRequest
	// Accepting is false while the host has closed requests.
	Accepting bool
	// Err is why the rest could not be read, or nil.
	Err error
}

// MyRequest is one of this phone's requests.
type MyRequest struct {
	ID     string
	SongID string
	// State is "waiting", "queued", "played", "declined" or "withdrawn".
	State string
	// Position is the place in the queue, from 1, or 0 when not queued.
	Position int
}

// ErrNameRequired reports that the queue manager wants the singer named
// before they request a song.
var ErrNameRequired = errors.New("the singer needs a name")

// Host is what the host runs the evening with, on the Host page.
type Host interface {
	// HostAllowed reports whether this app holds the host level, so the
	// tab bar offers the Host page. It must not wait on the network.
	HostAllowed() bool
	// HostView reads the requests waiting for the host and the singers.
	HostView(ctx context.Context) HostView
	// HostAction runs one of the host's actions:
	//   - "accept", "accept-next", "decline": a waiting request, by id;
	//   - "open", "close": requests from phones;
	//   - "add-singer": a singer with no phone, named value;
	//   - "remove-singer": a singer, by id;
	//   - "reset": a new evening.
	HostAction(ctx context.Context, action, id, value string) error
}

// HostView is what the Host page shows.
type HostView struct {
	Accepting bool
	Waiting   []HostRequest
	Singers   []HostSinger
	// Err is why the rest could not be read, or nil.
	Err error
}

// HostRequest is a request waiting for the host.
type HostRequest struct {
	ID     string
	SongID string
	Singer string
}

// HostSinger is one of the evening's singers.
type HostSinger struct {
	ID   string
	Name string
	// Phone is false for a singer the host added.
	Phone bool
}

// ErrRequestsClosed reports that the host has paused requests.
var ErrRequestsClosed = errors.New("requests are closed")

// Setup is what a backend offers on the Setup page. A backend that has
// nothing to set up, such as the unit's, does not implement it, and the page
// is not offered.
type Setup interface {
	// SetupInfo describes what the backend is connected to and what the
	// caller may do there.
	SetupInfo(ctx context.Context) SetupInfo
	// EnterCode exchanges an access code for a higher level, and keeps it.
	EnterCode(ctx context.Context, code string) error
	// ForgetCode drops the code, back to the level everybody has.
	ForgetCode() error
}

// SetupInfo is what the Setup page shows.
type SetupInfo struct {
	// Name and Addr identify the server.
	Name string
	Addr string
	// Level is what this app may do there: "view", "request", "queue",
	// "control" or "host". Room is what everybody may do without a code.
	Level string
	Room  string
	// HasCode reports a code in use.
	HasCode bool
	// Err is why the rest could not be read, or nil.
	Err error
}

// ErrWrongCode reports an access code that opens nothing.
var ErrWrongCode = errors.New("that code opens nothing")

// ErrNotAcknowledged reports a command that was sent but not confirmed. The
// pages treat it as done, and say so.
var ErrNotAcknowledged = errors.New("command not acknowledged")

// Errors a backend that is not the unit reports, so the pages can word them.
var (
	// ErrQueueManager: the queue manager does not answer.
	ErrQueueManager = errors.New("the queue manager is unreachable")
	// ErrNoQueueManager: no queue manager has been found yet, so there is
	// no address to try.
	ErrNoQueueManager = errors.New("no queue manager found yet")
	// ErrAway: the queue manager answers, but the karaoke unit is not
	// connected to it.
	ErrAway = errors.New("the karaoke unit is not connected to the queue manager")
	// ErrNeedsCode: the action needs an access code, or a better one.
	ErrNeedsCode = errors.New("this needs an access code")
	// ErrUnsupported: the backend has no such action.
	ErrUnsupported = errors.New("not available here")
)

// Status is the playback state the Now page shows.
type Status struct {
	Title    string
	SongID   string
	Next     string
	Position time.Duration
	Key      int
	Tempo    int
	Volume   int
	Mic      int
	Echo     int
	Lyrics   []string
}

// Queue is the list of songs waiting to play.
type Queue struct {
	// Total counts every entry; Entries may hold only a window of them,
	// starting at position Start.
	Total   int
	Start   int
	Entries []QueueEntry
}

// QueueEntry is one waiting song.
type QueueEntry struct {
	// EntryID identifies the entry, when the backend has Capabilities.EntryIDs.
	EntryID string
	SongID  string
	// By names who queued it.
	By string
}
