package webui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/protocol"
)

// UnitBackend drives the pages from a RakNet session to the unit.
func UnitBackend(dev *Device) Backend { return unitBackend{dev} }

type unitBackend struct{ dev *Device }

func (u unitBackend) Online() bool     { return u.dev.Online() }
func (u unitBackend) Addr() string     { return u.dev.Addr() }
func (u unitBackend) LastError() error { return u.dev.LastError() }

func (u unitBackend) Status() *Status { return unitStatus(u.dev.Status()) }
func (u unitBackend) Queue() *Queue   { return unitQueue(u.dev.LastQueue()) }

func (u unitBackend) RefreshQueue(ctx context.Context) error {
	return u.dev.Do(func(c *client.Client) error {
		_, err := c.Queue(ctx, 0, 50)
		return err
	})
}

func (u unitBackend) Control(ctx context.Context, action string, amount, mic int) error {
	return unitAck(u.dev.Do(func(c *client.Client) error {
		switch action {
		case "pause":
			return c.Pause(ctx)
		case "stop":
			return c.Stop(ctx)
		case "next":
			return c.NextSong(ctx)
		case "skip":
			return c.Skip(ctx)
		case "repeat":
			return c.Repeat(ctx)
		case "key":
			return c.SetKey(ctx, amount)
		case "tempo":
			return c.SetTempo(ctx, amount)
		case "reset-key":
			return c.ResetKey(ctx)
		case "reset-tempo":
			return c.ResetTempo(ctx)
		case "volume":
			return c.SetVolume(ctx, amount)
		case "mic":
			return c.SetMicVolume(ctx, mic, amount)
		case "reverb":
			return c.SetReverb(ctx, amount)
		case "melody":
			return c.SetMelody(ctx)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	}))
}

func (u unitBackend) SongAction(ctx context.Context, action, songID string) error {
	return unitAck(u.dev.Do(func(c *client.Client) error {
		switch action {
		case "reserve":
			return c.Reserve(ctx, songID)
		case "reserve-top":
			return c.ReserveFirst(ctx, songID)
		case "play":
			return c.PlayNow(ctx, songID)
		case "remove":
			return c.RemoveFromQueue(ctx, songID)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	}))
}

// EntryAction is not offered: the unit's queue has no entry ids.
func (u unitBackend) EntryAction(context.Context, string, string) error { return ErrUnsupported }

// Capabilities are the zero value, which is the unit's.
func (u unitBackend) Capabilities() Capabilities { return Capabilities{} }

func (u unitBackend) OnStatus(fn func(*Status)) {
	u.dev.OnStatus(func(st *protocol.Status) { fn(unitStatus(st)) })
}

func (u unitBackend) OnQueue(fn func(*Queue)) {
	u.dev.OnQueue(func(q *protocol.Queue) { fn(unitQueue(q)) })
}

func (u unitBackend) OnConnection(fn func(online bool, reason error)) {
	u.dev.OnConnection(fn)
}

// unitAck turns a missing echo into ErrNotAcknowledged.
//
// The unit acknowledges most commands but not always, and it ignores key and
// tempo outright when nothing is playing. The command was still sent, so only
// a broken connection is a failure.
func unitAck(err error) error {
	if err == nil {
		return nil
	}
	// interactiveTimeout is shorter than the client's own echo timer, so in
	// practice the deadline is what fires; match it directly rather than relying
	// on the wording that carries it.
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out waiting for echo") {
		return fmt.Errorf("%w: %v", ErrNotAcknowledged, err)
	}
	return err
}

func unitStatus(st *protocol.Status) *Status {
	if st == nil {
		return nil
	}
	return &Status{
		Title:    st.CurrentSong,
		SongID:   st.CurrentSongID,
		Next:     st.NextSong,
		Position: st.SongPosition,
		Key:      st.Key,
		Tempo:    st.Tempo,
		Volume:   st.MusicVolume,
		Mic:      st.MicVolume,
		Echo:     st.Echo,
		Lyrics:   st.Lyrics,
	}
}

func unitQueue(q *protocol.Queue) *Queue {
	if q == nil {
		return nil
	}
	out := &Queue{Total: q.Total, Start: q.Start, Entries: make([]QueueEntry, len(q.Entries))}
	for i, e := range q.Entries {
		out.Entries[i] = QueueEntry{SongID: e.SongID, By: e.QueuedBy()}
	}
	return out
}
