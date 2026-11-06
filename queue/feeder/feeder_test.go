package feeder

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/rrgmc/ck-karaoke/queue/fair"
)

type harness struct {
	t    *testing.T
	unit *fakeUnit
	room *Room
	f    *Feeder

	mu     sync.Mutex
	events []Event
	seats  map[string]uint64
}

// testLog sends the feeder's log to the test's, which go test shows only for
// a test that fails. The feeder has stopped before a test's cleanup returns,
// so nothing logs after the test ends.
type testLog struct{ t *testing.T }

func (l *testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func startFeeder(t *testing.T, order fair.Order, configure func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, unit: newFakeUnit(), seats: map[string]uint64{}}
	h.room = NewRoom(fair.NewEvening(order), func(n uint32) (fair.Song, bool) {
		return fair.Song{Number: n, Title: fmt.Sprintf("SONG %d", n)}, true
	})
	id := protocol.Identity{ClientID: protocol.RandomClientID(), Name: "QM"}
	opts := Options{
		Dialer: h.unit, Room: h.room, Identity: &id, Retry: 10 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(&testLog{t: t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		OnEvent: func(e Event) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.events = append(h.events, e)
		},
	}
	if configure != nil {
		configure(&opts)
	}
	h.f = New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.f.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	h.waitFor("connected", func() bool { return h.f.State().Connected })
	return h
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	if !assert.Eventually(h.t, cond, 2*time.Second, 5*time.Millisecond, what) {
		n, title := h.unit.Current()
		h.t.Fatalf("waiting for %s: unit plays %d %q, holds %v, evening %v, started %v, commands %v",
			what, n, title, h.unitQueue(), h.evening(), h.started(), h.unit.Log())
	}
}

func (h *harness) singer(name string) uint64 {
	var seat uint64
	h.room.Do(func(e *fair.Evening, _ time.Time) {
		device := e.Seats().Len()
		issued, err := e.Register(fmt.Sprintf("device-%d", device), &fair.SeatName{Shown: name, Key: strings.ToLower(name)},
			"token-"+name, fair.SeatRules{})
		require.NoError(h.t, err)
		seat = issued.Seat.ID
	})
	h.seats[name] = seat
	return seat
}

func (h *harness) ask(name string, number uint32) {
	h.room.Do(func(e *fair.Evening, now time.Time) {
		_, err := e.Request(h.seats[name], fair.ByToken("token-"+name),
			fair.RequestedSong{Number: number, Title: fmt.Sprintf("SONG %d", number)}, fair.DefaultRequestRules, now)
		require.NoError(h.t, err)
	})
}

func (h *harness) evening() []uint32 {
	var out []uint32
	for _, e := range h.room.Entries() {
		out = append(out, e.Number)
	}
	return out
}

func (h *harness) unitQueue() []string {
	var out []string
	for _, e := range h.unit.Queue() {
		out = append(out, fmt.Sprintf("%d by %s", e.number, e.by))
	}
	return out
}

func (h *harness) started() []uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []uint32
	for _, e := range h.events {
		if s, ok := e.(SongStarted); ok {
			out = append(out, s.Song.Number)
		}
	}
	return out
}

func (h *harness) ended() []SongEnded {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []SongEnded
	for _, e := range h.events {
		if s, ok := e.(SongEnded); ok {
			out = append(out, s)
		}
	}
	return out
}

func (h *harness) playing(number uint32) func() bool {
	return func() bool { n, _ := h.unit.Current(); return n == number }
}

func equal[T comparable](got func() []T, want ...T) func() bool {
	return func() bool {
		g := got()
		if len(g) != len(want) {
			return false
		}
		for i := range g {
			if g[i] != want[i] {
				return false
			}
		}
		return true
	}
}

// Two singers in Turns order: each song is queued through a session named
// after its singer, the unit holds one song ahead, and the songs play in turns.
func TestFeederPlaysTurnsOneSongAhead(t *testing.T) {
	h := startFeeder(t, fair.Turns, nil)
	h.singer("Ana")
	h.singer("Beto")
	for _, n := range []uint32{11, 12, 13} {
		h.ask("Ana", n)
	}
	h.ask("Beto", 21)
	h.ask("Beto", 22)
	require.Equal(t, []uint32{11, 21}, h.evening())

	h.unit.Advance(0) // the first status: the unit is idle
	h.waitFor("Ana's first song starts", h.playing(11))
	h.waitFor("Beto's song waits on the unit", equal(h.unitQueue, "21 by Beto"))
	assert.Equal(t, []uint32{21, 12}, h.evening())

	for _, want := range []struct {
		playing uint32
		next    string
	}{{21, "12 by Ana"}, {12, "22 by Beto"}, {22, "13 by Ana"}} {
		h.unit.End()
		h.waitFor(fmt.Sprintf("%d starts", want.playing), h.playing(want.playing))
		h.waitFor("the next song waits on the unit", equal(h.unitQueue, want.next))
	}
	h.unit.End()
	h.waitFor("the last song starts", h.playing(13))
	h.waitFor("nothing is left", equal(h.evening))
	h.unit.End()
	h.waitFor("the unit stops", h.playing(0))

	// The unit stops before the feeder reads its last statuses.
	h.waitFor("every song is reported as started", equal(h.started, 11, 21, 12, 22, 13))
	assert.Contains(t, h.unit.Log(), "AddOrder 11 as Ana")
	var sung []uint32
	h.room.View(func(e *fair.Evening, _ time.Time) {
		for _, s := range e.Sung() {
			sung = append(sung, s.Number)
		}
	})
	assert.Equal(t, []uint32{11, 21, 12, 22, 13}, sung, "the evening records each song as played")
	for _, e := range h.ended() {
		assert.True(t, e.Natural)
		assert.Equal(t, 3*time.Minute-time.Second, e.Length, "the last position reported")
	}
}

// inLoop runs fn on the feeder's loop goroutine. It returns after the loop has
// finished handling every push it took before fn.
func (h *harness) inLoop(fn func(f *Feeder)) {
	h.t.Helper()
	require.NoError(h.t, h.f.do(context.Background(), func(context.Context) error {
		fn(h.f)
		return nil
	}))
}

// bufferedConfirmed reports whether the feeder has seen number, its next song,
// in the unit's queue.
func (h *harness) bufferedConfirmed(number uint32) func() bool {
	return func() bool {
		// Eventually polls on another goroutine, so this cannot use inLoop's
		// require.
		var ok bool
		err := h.f.do(context.Background(), func(context.Context) error {
			b := h.f.buffered
			ok = b != nil && b.number == number && b.confirmed
			return nil
		})
		return err == nil && ok
	}
}

// count is how many commands the unit received that equal cmd.
func (h *harness) count(cmd string) int {
	n := 0
	for _, c := range h.unit.Log() {
		if c == cmd {
			n++
		}
	}
	return n
}

// A queue push read before the feeder added the next song arrives after the
// push that showed the song, and a status follows: the feeder keeps the song as
// queued and does not send it again.
func TestFeederKeepsTheBufferedSongAfterAnOlderQueuePush(t *testing.T) {
	h := startFeeder(t, fair.Turns, nil)
	h.singer("Ana")
	h.singer("Beto")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.ask("Beto", 21)
	h.ask("Beto", 22)

	h.unit.Advance(0)
	h.waitFor("Ana's first song starts", h.playing(11))
	h.waitFor("Beto's song waits on the unit", equal(h.unitQueue, "21 by Beto"))
	h.unit.End()
	h.waitFor("21 starts", h.playing(21))
	h.waitFor("the feeder sees 12 on the unit", h.bufferedConfirmed(12))

	h.unit.PushStaleQueue() // the queue from when 21 started, before 12 was added
	h.unit.Advance(time.Second)
	h.waitFor("the feeder reads the status", func() bool { return h.f.State().Position == time.Second })
	h.inLoop(func(*Feeder) {}) // the feeder has acted on the status
	assert.Equal(t, 1, h.count("AddOrder 12 as Ana"), "12 is sent once; commands %v", h.unit.Log())
	assert.Equal(t, []string{"12 by Ana"}, h.unitQueue())

	h.unit.End()
	h.waitFor("12 starts", h.playing(12))
	h.waitFor("the next song waits on the unit", equal(h.unitQueue, "22 by Beto"))
	assert.Equal(t, 1, h.count("AddOrder 12 as Ana"), "12 is sent once; commands %v", h.unit.Log())
	h.waitFor("the songs start in turns", equal(h.started, 11, 21, 12))
}

// A Skip that the feeder handles before the unit shows an earlier Restart
// still cuts the song short.
func TestFeederSkipAfterARestartTheUnitHasNotShown(t *testing.T) {
	h := startFeeder(t, fair.Arrival, nil)
	h.singer("Ana")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.unit.Advance(0)
	h.waitFor("the next song waits", equal(h.unitQueue, "12 by Ana"))
	h.waitFor("the feeder sees 12 on the unit", h.bufferedConfirmed(12))
	ctx := context.Background()

	h.unit.Advance(time.Minute)
	h.unit.HoldNextPlayEx()
	require.NoError(t, h.f.Restart(ctx))
	require.NoError(t, h.f.Skip(ctx))
	h.waitFor("the next song plays", h.playing(12))
	h.unit.Advance(time.Second)
	h.waitFor("the restart is no song of its own", equal(h.started, 11, 12))

	ended := h.ended()
	require.Len(t, ended, 1)
	assert.Equal(t, uint32(11), ended[0].Song.Number)
	assert.False(t, ended[0].Natural, "11 was skipped")
}

// A host moves another song ahead of the buffered one: the feeder swaps the
// unit's next song.
func TestFeederReplacesTheBufferedSongWhenTheOrderChanges(t *testing.T) {
	h := startFeeder(t, fair.Arrival, nil)
	h.singer("Ana")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.ask("Ana", 13)
	h.unit.Advance(0)
	h.waitFor("the next song waits", equal(h.unitQueue, "12 by Ana"))

	h.room.Do(func(e *fair.Evening, _ time.Time) {
		entries := e.Queue().Entries()
		e.MoveTo(entries[1].ID, 0)
	})
	h.waitFor("the unit holds the new next song", equal(h.unitQueue, "13 by Ana"))
	assert.Contains(t, h.unit.Log(), "DelOrder 12")

	h.unit.End()
	h.waitFor("the moved song plays", h.playing(13))
}

// A song another remote queues is adopted at the place the unit plays it,
// plays in the unit's order, and leaves the evening when it starts.
func TestFeederAdoptsForeignSongs(t *testing.T) {
	h := startFeeder(t, fair.Turns, nil)
	h.singer("Ana")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.unit.Advance(0)
	h.waitFor("the next song waits", equal(h.unitQueue, "12 by Ana"))

	other, err := h.unit.Dial(context.Background(), protocol.Identity{ClientID: "x", Name: "Phone"})
	require.NoError(t, err)
	require.NoError(t, other.Reserve(context.Background(), "5000"))
	h.waitFor("the foreign song is adopted behind the next song", equal(h.evening, 12, 5000))
	entries := h.room.Entries()
	assert.Nil(t, entries[1].Seat, "a song with no singer")
	assert.Equal(t, "SONG 5000", entries[1].Title, "described from the catalogue")

	h.unit.End()
	h.waitFor("12 plays", h.playing(12))
	h.waitFor("the foreign song is next", equal(h.evening, 5000))
	h.unit.End()
	h.waitFor("the foreign song plays", h.playing(5000))
	h.waitFor("and leaves the evening", equal(h.evening))
}

// A foreign song its owner removes leaves the evening; one a host removes
// leaves the unit.
func TestFeederFollowsRemovalsOfForeignSongs(t *testing.T) {
	h := startFeeder(t, fair.Turns, nil)
	h.singer("Ana")
	h.ask("Ana", 11)
	h.unit.Advance(0)
	h.waitFor("11 plays", h.playing(11))

	other, err := h.unit.Dial(context.Background(), protocol.Identity{ClientID: "x", Name: "Phone"})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, other.Reserve(ctx, "5000"))
	require.NoError(t, other.Reserve(ctx, "6000"))
	h.waitFor("both are adopted", equal(h.evening, 5000, 6000))

	require.NoError(t, other.RemoveFromQueue(ctx, "5000"))
	h.unit.Advance(time.Second)
	h.waitFor("the owner's removal reaches the evening", equal(h.evening, 6000))

	h.room.Do(func(e *fair.Evening, now time.Time) {
		e.Remove(e.Queue().Entries()[0].ID, now)
	})
	h.waitFor("the host's removal reaches the unit", equal(h.unitQueue))
}

// With rerouting on, a foreign song is taken off the unit and queued in the
// evening's order.
func TestFeederReroutesForeignSongs(t *testing.T) {
	h := startFeeder(t, fair.Turns, func(o *Options) { o.Foreign = Reroute })
	h.singer("Ana")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.unit.Advance(0)
	h.waitFor("the next song waits", equal(h.unitQueue, "12 by Ana"))

	other, err := h.unit.Dial(context.Background(), protocol.Identity{ClientID: "x", Name: "Phone"})
	require.NoError(t, err)
	require.NoError(t, other.Reserve(context.Background(), "5000"))
	h.waitFor("the foreign song joins the end of the evening", equal(h.evening, 12, 5000))
	h.waitFor("and leaves the unit", equal(h.unitQueue, "12 by Ana"))
}

// Stop holds playback; nothing starts until Play, which starts the next song.
func TestFeederHoldsAfterStop(t *testing.T) {
	h := startFeeder(t, fair.Arrival, nil)
	h.singer("Ana")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.unit.Advance(0)
	h.waitFor("the next song waits", equal(h.unitQueue, "12 by Ana"))

	ctx := context.Background()
	require.NoError(t, h.f.Stop(ctx))
	h.waitFor("the unit stops", h.playing(0))
	assert.Empty(t, h.unitQueue(), "the buffered song came off the unit")
	// The unit's own report of the stop reaches the feeder a moment later.
	h.waitFor("the feeder sees the stop", func() bool {
		st := h.f.State()
		return st.Transport == Stopped && st.Held && len(h.ended()) == 1
	})
	h.unit.Advance(time.Second)
	assert.Equal(t, []uint32{12}, h.evening(), "nothing started")
	assert.False(t, h.ended()[0].Natural)

	require.NoError(t, h.f.Play(ctx))
	h.unit.Advance(time.Second)
	h.waitFor("Play starts the next song", h.playing(12))
}

// Skip starts the buffered song with NextSong, and Restart plays the current
// song again without touching the evening.
func TestFeederSkipAndRestart(t *testing.T) {
	h := startFeeder(t, fair.Arrival, nil)
	h.singer("Ana")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.unit.Advance(0)
	h.waitFor("the next song waits", equal(h.unitQueue, "12 by Ana"))
	ctx := context.Background()

	h.unit.Advance(time.Minute)
	require.NoError(t, h.f.Restart(ctx))
	h.unit.Advance(time.Second)
	assert.Contains(t, h.unit.Log(), "PlayEx 11")
	assert.Equal(t, []uint32{11}, h.started(), "a restart is no new song")
	assert.Equal(t, []uint32{12}, h.evening())

	require.NoError(t, h.f.Skip(ctx))
	h.waitFor("the next song plays", h.playing(12))
	assert.Contains(t, h.unit.Log(), "NextSong")
	assert.NotContains(t, h.unit.Log(), "Skip", "the unit ignores Skip")
	// The first status after NextSong still carries 11's id; the next one
	// names 12.
	h.unit.Advance(time.Second)
	h.waitFor("12 left the evening", equal(h.evening))
	h.waitFor("the stale id is no song of its own", equal(h.started, 11, 12))
	assert.False(t, h.ended()[0].Natural)
}

// A pause shows only as a position that stops moving.
func TestFeederInfersPause(t *testing.T) {
	h := startFeeder(t, fair.Arrival, func(o *Options) {})
	h.singer("Ana")
	h.ask("Ana", 11)
	h.unit.Advance(0)
	h.waitFor("11 plays", h.playing(11))
	h.unit.Advance(2 * time.Second)
	h.waitFor("playing", func() bool { return h.f.State().Transport == Playing })

	h.unit.mu.Lock()
	h.unit.paused, h.unit.idField = true, "0"
	h.unit.mu.Unlock()
	h.unit.Advance(2 * time.Second)
	h.unit.Advance(2 * time.Second)
	h.waitFor("a stalled position is a pause", func() bool { return h.f.State().Transport == Paused })
	assert.Equal(t, []uint32{11}, h.started(), "the id reading 0 is no new song")

	require.NoError(t, h.f.Play(context.Background()))
	assert.Contains(t, h.unit.Log(), "Pause", "Play toggles the pause")
	h.unit.Advance(2 * time.Second)
	h.waitFor("playing again", func() bool { return h.f.State().Transport == Playing })
}

// The sequence measured on the real unit: pause, resume, restart, stop and
// play. After the pause the id reads 0, so the restart is known by its title;
// the song Play starts first reports the previous song's id.
func TestFeederTransportSequence(t *testing.T) {
	h := startFeeder(t, fair.Arrival, nil)
	h.singer("Ana")
	h.ask("Ana", 11)
	h.ask("Ana", 12)
	h.unit.Advance(0)
	h.waitFor("the next song waits", equal(h.unitQueue, "12 by Ana"))
	ctx := context.Background()

	h.unit.Advance(5 * time.Second)
	require.NoError(t, h.f.Pause(ctx))
	h.unit.Advance(2 * time.Second)
	h.unit.Advance(2 * time.Second)
	require.NoError(t, h.f.Play(ctx))
	h.unit.Advance(2 * time.Second)
	require.NoError(t, h.f.Restart(ctx))
	h.unit.Advance(2 * time.Second)
	h.waitFor("the restart reaches the state", func() bool { return h.f.State().Position == 2*time.Second })
	assert.Equal(t, []uint32{11}, h.started(), "a restart is no new song")

	require.NoError(t, h.f.Stop(ctx))
	h.waitFor("stopped", func() bool { return h.f.State().Transport == Stopped })
	require.NoError(t, h.f.Play(ctx))
	h.unit.Advance(time.Second)
	h.waitFor("Play starts the next song", h.playing(12))
	h.unit.Advance(2 * time.Second)
	h.unit.Advance(2 * time.Second)
	h.waitFor("12 left the evening", equal(h.evening))
	h.waitFor("the stale id is no song of its own", equal(h.started, 11, 12))
	for _, e := range h.ended() {
		assert.False(t, e.Natural)
	}
}

// A singer with no name is queued on the unit by number.
func TestFeederNamesAnUnnamedSingerByNumber(t *testing.T) {
	h := startFeeder(t, fair.Arrival, nil)
	var seat uint64
	h.room.Do(func(e *fair.Evening, now time.Time) {
		issued, err := e.Register("phone", nil, "token-guest", fair.SeatRules{})
		require.NoError(t, err)
		seat = issued.Seat.ID
		_, err = e.Request(seat, fair.ByToken("token-guest"), fair.RequestedSong{Number: 11, Title: "SONG 11"},
			fair.DefaultRequestRules, now)
		require.NoError(t, err)
	})
	h.unit.Advance(0)
	h.waitFor("the song starts", h.playing(11))
	assert.Contains(t, h.unit.Log(), "AddOrder 11 as Guest 1")
}

// Transpose and tempo are absolute in the API and deltas on the unit.
func TestFeederSendsControlsAsDeltas(t *testing.T) {
	h := startFeeder(t, fair.Arrival, nil)
	h.unit.Advance(0)
	// The controls are deltas from the reported values, so they wait for the
	// first status.
	h.waitFor("the first status", func() bool { return h.f.State().Volume == 50 })
	ctx := context.Background()
	require.NoError(t, h.f.SetTranspose(ctx, 2))
	h.unit.Advance(0)
	h.waitFor("the key reaches 2", func() bool { return h.f.State().Key == 2 })
	require.NoError(t, h.f.SetTranspose(ctx, -1))
	require.NoError(t, h.f.SetTempo(ctx, 1.25))
	require.NoError(t, h.f.SetVolume(ctx, 0.5))
	assert.Equal(t, []string{"SetKey 2", "SetKey -3", "SetTempo 6", "SetVolume 50"}, h.unit.Log())
}

func TestTempoAndVolumeConversions(t *testing.T) {
	tests := []struct {
		ratio float64
		steps int
	}{{1, 0}, {1.25, 6}, {0.75, -10}, {1 / (1 - 1.0/30), 1}}
	for _, tt := range tests {
		assert.Equal(t, tt.steps, TempoSteps(tt.ratio), tt.ratio)
		assert.InDelta(t, tt.ratio, TempoRatio(tt.steps), 1e-9)
	}
	assert.Equal(t, 0, VolumeLevel(-1))
	assert.Equal(t, 50, VolumeLevel(0.5))
	assert.Equal(t, 100, VolumeLevel(2))
}

func TestDurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "durations.json")
	d, err := OpenDurations(path)
	require.NoError(t, err)
	took, err := d.Learn(1001, 10*time.Second)
	require.NoError(t, err)
	assert.False(t, took, "too short to trust")
	took, err = d.Learn(1001, 3*time.Minute)
	require.NoError(t, err)
	assert.True(t, took)

	again, err := OpenDurations(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(180_000), again.Get(1001))
	assert.Zero(t, again.Get(1002))
}
