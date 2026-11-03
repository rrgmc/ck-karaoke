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

	// OnStatus, OnQueue and OnConnection register the push callbacks.
	// OnConnection fires only on a change.
	OnStatus(fn func(*Status))
	OnQueue(fn func(*Queue))
	OnConnection(fn func(online bool, reason error))
}

// ErrNotAcknowledged reports a command that was sent but not confirmed. The
// pages treat it as done, and say so.
var ErrNotAcknowledged = errors.New("command not acknowledged")

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
	SongID string
	// By names who queued it.
	By string
}
