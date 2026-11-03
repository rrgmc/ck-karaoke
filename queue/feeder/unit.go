package feeder

import (
	"context"
	"log/slog"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/protocol"
)

// Unit is a session with a karaoke unit: the part of client.Client the feeder
// uses. Tests replace it with a simulated unit.
type Unit interface {
	Reserve(ctx context.Context, songID string) error
	RemoveFromQueue(ctx context.Context, songID string) error
	PlayNow(ctx context.Context, songID string) error
	NextSong(ctx context.Context) error
	Pause(ctx context.Context) error
	Stop(ctx context.Context) error
	SetKey(ctx context.Context, semitones int) error
	SetTempo(ctx context.Context, steps int) error
	SetVolume(ctx context.Context, level int) error
	Queue(ctx context.Context, start, count int) (*protocol.Queue, error)
	OnStatus(fn func(*protocol.Status))
	OnQueue(fn func(*protocol.Queue))
	// Context is cancelled when the session ends, for any reason.
	Context() context.Context
	Close() error
}

// Dialer opens sessions with one unit. The feeder keeps one long session for
// status and control, and opens a short one for each song a singer queues, so
// that the unit shows the singer's name beside it.
type Dialer interface {
	Dial(ctx context.Context, identity protocol.Identity) (Unit, error)
}

// ClientDialer dials a unit at a fixed address with client.Dial.
type ClientDialer struct {
	Addr   string
	Logger *slog.Logger
}

// Dial opens a session as identity.
func (d ClientDialer) Dial(ctx context.Context, identity protocol.Identity) (Unit, error) {
	c, err := client.Dial(ctx, d.Addr, client.Options{Identity: &identity, Logger: d.Logger})
	if err != nil {
		return nil, err
	}
	return c, nil
}

var _ Unit = (*client.Client)(nil)
