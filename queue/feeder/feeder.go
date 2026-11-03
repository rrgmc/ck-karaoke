// Package feeder plays a fair queue on a karaoke unit.
//
// The unit cannot reorder its own queue, so the real queue lives in a
// fair.Evening (shared through a Room), and the unit's queue is a buffer of one
// song. While a song plays, the unit holds exactly the next song of the
// evening. When the unit starts it, the unit pushes an empty queue, and the
// feeder adds the one after. When the order changes, only the buffered song is
// removed and replaced.
//
// The unit behaves as follows, and the feeder relies on each point:
//
//   - The unit starts a buffered song by itself when the current one ends.
//   - AddOrder while the unit is idle or stopped starts the song at once and
//     does not show it in the queue. So nothing is added while a host holds
//     playback, and the first song of an evening starts as soon as it is
//     queued.
//   - NextSong starts the buffered song. Skip has no effect.
//   - PlayEx with the current song's id restarts it.
//   - There is no pause flag. A pause shows as a position that stops moving,
//     and the song id field reads 0 from a pause until the next song starts.
//   - The first status after a song change carries a large negative position.
//   - The unit names each queue entry after the session that added it, so each
//     song is queued through a short session named after its singer.
//
// Songs that another remote or the unit's own remote queues are foreign. The
// feeder cannot prevent them. By default it adopts them into the evening as
// songs with no singer, at the place the unit will play them.
package feeder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/rrgmc/ck-karaoke/queue/fair"
)

// Transport is what the unit is doing, as KaraokeMachine names it.
type Transport string

const (
	Idle    Transport = "idle"
	Playing Transport = "playing"
	Paused  Transport = "paused"
	Stopped Transport = "stopped"
)

// ForeignPolicy is what the feeder does with a song queued on the unit by
// anyone else.
type ForeignPolicy string

const (
	// Adopt shows a foreign song in the evening as a song with no singer, at
	// the place the unit will play it.
	Adopt ForeignPolicy = "adopt"
	// Reroute removes a foreign song from the unit and adds it to the end of
	// the evening, so it plays in the evening's order.
	Reroute ForeignPolicy = "reroute"
)

// pauseShows is how long a pause or resume the feeder sent decides the
// transport before the position shows it. The unit can apply it one status
// late, and statuses come every two seconds.
const pauseShows = 5 * time.Second

// ErrOffline reports that no session with the unit is open.
var ErrOffline = errors.New("the karaoke unit is not connected")

// Song is the song the unit is playing.
type Song struct {
	Number uint32
	Title  string
	// Entry is the evening's entry for it, or nil for a song the evening did
	// not hold.
	Entry   *fair.Entry
	Started time.Time
}

// State is what the feeder knows of the unit.
type State struct {
	Connected bool
	Transport Transport
	Position  time.Duration
	Song      *Song
	// Key is in semitones; TempoSteps in the unit's steps either side of
	// normal; Volume 0 to 100.
	Key        int
	TempoSteps int
	Volume     int
	// Held reports that a host stopped playback, or that a restored evening
	// waits for a host to press Play. Nothing is started while it holds.
	Held bool
}

// Event is something the feeder reports.
type Event interface{ isEvent() }

// SongStarted reports that a song started.
type SongStarted struct{ Song Song }

// SongEnded reports that a song stopped playing. Natural is true when it played
// to its end rather than being skipped, stopped or restarted, and Length is
// then its length, as far as the status reports showed it.
type SongEnded struct {
	Song    Song
	Natural bool
	Length  time.Duration
}

// StateChanged reports a new status from the unit, or a change of connection.
type StateChanged struct{ State State }

func (SongStarted) isEvent()  {}
func (SongEnded) isEvent()    {}
func (StateChanged) isEvent() {}

// Options configures a Feeder.
type Options struct {
	Dialer Dialer
	Room   *Room
	// Identity is the long session's identity. Its name labels songs with no
	// singer. Defaults to a generated identity.
	Identity *protocol.Identity
	Foreign  ForeignPolicy
	// Held starts the feeder holding playback until Play, as after a restored
	// evening.
	Held    bool
	Logger  *slog.Logger
	OnEvent func(Event)
	// Silence is how long without status counts as a dropped session.
	// Defaults to 15 seconds.
	Silence time.Duration
	// Retry is the wait between connection attempts. Defaults to 3 seconds.
	Retry time.Duration
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
}

// Feeder plays a Room's evening on a unit.
type Feeder struct {
	opts   Options
	log    *slog.Logger
	now    func() time.Time
	cmds   chan command
	mu     sync.Mutex
	state  State
	singer map[uint64]protocol.Identity
	// reconnect ends the session, so the next one finds the unit again.
	reconnect chan struct{}
	foreign   atomic.Value // ForeignPolicy

	// Everything below belongs to the run loop's goroutine.
	unit       Unit
	status     *protocol.Status
	lastID     string        // the last song id that was not 0
	lastTitle  string        // the title that came with lastID
	waits      int           // stale statuses in a row
	lastPos    time.Duration // the last position that was not a song change
	stalled    bool
	playing    *Song
	pending    *slot // a song added while idle, or played with PlayEx
	buffered   *slot // the song the unit holds next
	adopted    map[uint32]uint64
	vanished   map[uint32]uint64
	held       bool
	stopped    bool
	pauseUntil time.Time // a pause or resume the feeder sent, until it shows
	pauseWant  bool
	restarting bool
	cut        bool // the playing song was cut short by a command
	retryAt    time.Time
}

// slot is an evening entry the feeder sent to the unit.
type slot struct {
	entry     uint64
	number    uint32
	title     string
	sent      time.Time
	confirmed bool // seen in the unit's queue
	gone      bool // left the unit's queue; its fate shows in the next status
}

type command struct {
	fn    func(ctx context.Context) error
	reply chan error
}

// New returns a feeder. Call Run to start it.
func New(opts Options) *Feeder {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Silence == 0 {
		opts.Silence = 15 * time.Second
	}
	if opts.Retry == 0 {
		opts.Retry = 3 * time.Second
	}
	if opts.Foreign == "" {
		opts.Foreign = Adopt
	}
	if opts.Identity == nil {
		id := protocol.NewIdentity()
		opts.Identity = &id
	}
	f := &Feeder{
		opts:      opts,
		log:       opts.Logger,
		now:       opts.Now,
		cmds:      make(chan command),
		reconnect: make(chan struct{}, 1),
		singer:    map[uint64]protocol.Identity{},
		adopted:   map[uint32]uint64{},
		vanished:  map[uint32]uint64{},
		held:      opts.Held,
		state:     State{Transport: Idle, Held: opts.Held},
	}
	f.foreign.Store(opts.Foreign)
	return f
}

// Foreign returns the policy for songs queued on the unit by anyone else.
func (f *Feeder) Foreign() ForeignPolicy { return f.foreign.Load().(ForeignPolicy) }

// SetForeign changes the policy for songs queued on the unit by anyone else.
// Songs already adopted stay adopted.
func (f *Feeder) SetForeign(p ForeignPolicy) { f.foreign.Store(p) }

// Reconnect ends the session with the unit; the feeder then connects again
// and finds the unit afresh.
func (f *Feeder) Reconnect() {
	select {
	case f.reconnect <- struct{}{}:
	default:
	}
}

// State returns what the feeder knows of the unit.
func (f *Feeder) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.state
	if s.Song != nil {
		c := *s.Song
		s.Song = &c
	}
	return s
}

// Run connects to the unit and feeds it until ctx is cancelled. It reconnects
// when the session drops, and it returns only after the session has closed
// cleanly.
func (f *Feeder) Run(ctx context.Context) {
	changed, unsubscribe := f.opts.Room.Subscribe()
	defer unsubscribe()
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		unit, err := f.opts.Dialer.Dial(dialCtx, *f.opts.Identity)
		cancel()
		if err == nil {
			f.serve(ctx, unit, changed)
		} else if ctx.Err() == nil {
			f.log.Warn("cannot reach the karaoke unit", "error", err)
		}
		f.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(f.opts.Retry):
		}
	}
}

// drain answers commands sent while offline, until the retry wait is over.
func (f *Feeder) drain(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case c := <-f.cmds:
			c.reply <- ErrOffline
		case <-ctx.Done():
			return
		case <-t.C:
			return
		}
	}
}

// serve runs one session until it drops or ctx is cancelled.
func (f *Feeder) serve(ctx context.Context, unit Unit, changed <-chan struct{}) {
	// Statuses and queues share one channel, so they are read in the order the
	// unit sent them. Read apart, a queue push that shows the buffered song
	// gone could be handled before an older status, which would take the song
	// for removed rather than started.
	pushes := make(chan any, 64)
	push := func(p any) {
		select {
		case pushes <- p:
		default:
			f.log.Warn("the feeder fell behind the unit; a push was dropped")
		}
	}
	unit.OnStatus(func(s *protocol.Status) { push(s) })
	unit.OnQueue(func(q *protocol.Queue) { push(q) })
	f.unit = unit
	f.setConnected(true)
	f.log.Info("connected to the karaoke unit")
	defer func() {
		unit.OnStatus(nil)
		unit.OnQueue(nil)
		f.unit = nil
		f.status = nil
		f.setConnected(false)
		if err := unit.Close(); err != nil {
			f.log.Warn("closing the unit session", "error", err)
		}
	}()

	// The reply arrives as a pushed queue, which the loop below handles.
	go func() {
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, err := unit.Queue(qctx, 0, 10); err != nil && ctx.Err() == nil {
			f.log.Debug("asking for the unit's queue", "error", err)
		}
	}()

	silence := time.NewTimer(f.opts.Silence)
	defer silence.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-unit.Context().Done():
			f.log.Warn("the unit session ended")
			return
		case <-silence.C:
			f.log.Warn("no status from the unit; reconnecting", "after", f.opts.Silence)
			return
		case p := <-pushes:
			switch p := p.(type) {
			case *protocol.Status:
				silence.Reset(f.opts.Silence)
				f.onStatus(ctx, p)
			case *protocol.Queue:
				f.onQueue(ctx, p)
			}
		case <-changed:
		case c := <-f.cmds:
			c.reply <- c.fn(ctx)
		case <-f.reconnect:
			f.log.Info("reconnecting to the karaoke unit")
			return
		case <-tick.C:
		}
		f.reconcile(ctx)
	}
}

// do runs fn on the loop's goroutine with the session open.
func (f *Feeder) do(ctx context.Context, fn func(ctx context.Context) error) error {
	if !f.State().Connected {
		return ErrOffline
	}
	reply := make(chan error, 1)
	select {
	case f.cmds <- command{fn: fn, reply: reply}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// onStatus reads one status push.
//
// A new song shows in three ways: its first status carries a large negative
// position, its id differs from the last one, or its title differs. The id
// field cannot always be trusted. It reads 0 from a pause until the next song
// starts, and the first status after NextSong, or after a song added while the
// unit was stopped, carries the new title with the previous song's id. So an
// id is stale when it is the last song's id under another title. A stale
// status waits for the next one, and after two the song is matched by title.
func (f *Feeder) onStatus(ctx context.Context, s *protocol.Status) {
	now := f.now()
	title := s.CurrentSong
	id := s.CurrentSongID
	knownID := id != "" && id != "0"
	change := s.SongPosition < 0
	prevTitle := ""
	if f.status != nil {
		prevTitle = f.status.CurrentSong
	}
	stale := knownID && id == f.lastID && !strings.EqualFold(title, f.lastTitle)

	newSong, wait := false, false
	switch {
	case title == "":
	case change && strings.EqualFold(title, prevTitle):
		// The same song from the start: a restart, or the same song again.
		newSong = true
	case stale:
		wait = f.waits < 2
		newSong = !wait
	case change, knownID && id != f.lastID:
		newSong = true
	case f.playing == nil, !strings.EqualFold(title, f.playing.Title):
		// Also a song that was already playing, or paused, when the session
		// opened.
		newSong = true
	}
	if wait {
		f.waits++
	} else {
		f.waits = 0
	}
	switch {
	case stale || !knownID:
		id = ""
	case title != "":
		f.lastID, f.lastTitle = id, title
	}

	switch {
	case wait:
		f.log.Debug("song change with a stale id; waiting for the next status", "title", title, "id", s.CurrentSongID)
	case newSong && f.restarting && f.playing != nil && strings.EqualFold(title, f.playing.Title):
		// Our own PlayEx of the current song: the same song from the start.
		f.restarting = false
		f.playing.Started = now
		f.cut = false
	case newSong:
		n, _ := strconv.ParseUint(id, 10, 32)
		f.songChanged(uint32(n), title, now)
	case title == "" && f.playing != nil:
		f.songEnded()
	}

	if !wait {
		// Adopted songs that left the unit's queue and did not start now were
		// removed by whoever queued them.
		for n, entry := range f.vanished {
			f.opts.Room.drop(entry)
			delete(f.vanished, n)
		}
		if f.buffered != nil && f.buffered.gone {
			// Removed by another remote. The evening still holds it, so it
			// goes back on the unit.
			f.buffered = nil
		}
	}

	if !change {
		f.stalled = title != "" && f.status != nil && f.status.CurrentSong == title && s.SongPosition == f.lastPos
		f.lastPos = s.SongPosition
	}
	f.status = s
	f.publishState(now)
}

// songChanged handles the start of a song the unit reports as number.
func (f *Feeder) songChanged(number uint32, title string, now time.Time) {
	if f.playing != nil {
		f.songEnded()
	}
	song := Song{Number: number, Title: title, Started: now}
	// By number, or by title when the unit reports no id.
	is := func(sl *slot) bool {
		return sl != nil && (number != 0 && sl.number == number || number == 0 && strings.EqualFold(sl.title, title))
	}
	var entry uint64
	found := false
	switch {
	case is(f.pending):
		entry, found, song.Number = f.pending.entry, true, f.pending.number
		f.pending = nil
	case is(f.buffered):
		entry, found, song.Number = f.buffered.entry, true, f.buffered.number
		f.buffered = nil
	default:
		for n, e := range f.adopted {
			if n == number || number == 0 && strings.EqualFold(f.entryTitle(e), title) {
				entry, found, song.Number = e, true, n
				delete(f.adopted, n)
				delete(f.vanished, n)
				break
			}
		}
	}
	if found {
		if e, ok := f.opts.Room.start(entry); ok {
			song.Entry = &e
		}
	}
	f.playing = &song
	f.stopped = false
	f.cut = false
	f.log.Info("song started", "number", number, "title", title, "fromQueue", song.Entry != nil)
	f.emit(SongStarted{Song: song})
}

// entryTitle is the title of an evening entry, or empty.
func (f *Feeder) entryTitle(entry uint64) string {
	for _, e := range f.opts.Room.Entries() {
		if e.ID == entry {
			return e.Title
		}
	}
	return ""
}

// songEnded records the end of the playing song.
func (f *Feeder) songEnded() {
	ended := *f.playing
	f.playing = nil
	f.emit(SongEnded{Song: ended, Natural: !f.cut, Length: f.lastPos})
	f.cut = false
	f.restarting = false
}

// onQueue reads one push of the unit's queue.
func (f *Feeder) onQueue(ctx context.Context, q *protocol.Queue) {
	inUnit := map[uint32]bool{}
	for i, e := range q.Entries {
		n64, err := strconv.ParseUint(e.SongID, 10, 32)
		if err != nil {
			continue
		}
		n := uint32(n64)
		inUnit[n] = true
		if f.buffered != nil && f.buffered.number == n {
			f.buffered.confirmed = true
			continue
		}
		if f.pending != nil && f.pending.number == n {
			continue
		}
		if entry, ok := f.adopted[n]; ok {
			delete(f.vanished, n)
			f.opts.Room.place(entry, i)
			continue
		}
		switch f.Foreign() {
		case Reroute:
			if err := f.unit.RemoveFromQueue(ctx, e.SongID); err != nil {
				f.log.Warn("removing a foreign song from the unit", "number", n, "error", err)
				continue
			}
			if err := f.opts.Room.add(n); err != nil {
				f.log.Warn("adding a foreign song to the queue", "number", n, "error", err)
			}
			f.log.Info("rerouted a foreign song", "number", n, "by", e.QueuedBy())
		default:
			entry, err := f.opts.Room.adopt(n, i)
			if err != nil {
				f.log.Warn("adopting a foreign song", "number", n, "error", err)
				continue
			}
			f.adopted[n] = entry
			f.log.Info("adopted a foreign song", "number", n, "by", e.QueuedBy())
		}
	}
	if f.buffered != nil && f.buffered.confirmed && !inUnit[f.buffered.number] {
		f.buffered.gone = true
	}
	for n, entry := range f.adopted {
		if !inUnit[n] {
			f.vanished[n] = entry
		}
	}
}

// reconcile brings the unit's queue in line with the evening.
func (f *Feeder) reconcile(ctx context.Context) {
	if f.unit == nil || f.status == nil {
		return
	}
	now := f.now()
	entries := f.opts.Room.Entries()
	inEvening := map[uint64]bool{}
	for _, e := range entries {
		inEvening[e.ID] = true
	}

	// A host removed an adopted song: take it off the unit too.
	for n, entry := range f.adopted {
		if !inEvening[entry] {
			delete(f.adopted, n)
			delete(f.vanished, n)
			if err := f.unit.RemoveFromQueue(ctx, strconv.FormatUint(uint64(n), 10)); err != nil {
				f.log.Warn("removing a song from the unit", "number", n, "error", err)
			}
		}
	}

	adoptedEntry := map[uint64]bool{}
	for _, entry := range f.adopted {
		adoptedEntry[entry] = true
	}
	var next *fair.Entry
	for i := range entries {
		if !adoptedEntry[entries[i].ID] {
			next = &entries[i]
			break
		}
	}

	if f.buffered != nil && (next == nil || next.ID != f.buffered.entry) {
		f.unbuffer(ctx)
	}
	if f.buffered != nil && !f.buffered.confirmed && now.Sub(f.buffered.sent) > 8*time.Second {
		// The unit refuses a song it already holds, silently.
		f.log.Warn("the unit did not take the next song; trying again later", "number", f.buffered.number)
		f.buffered = nil
		f.retryAt = now.Add(30 * time.Second)
	}
	if f.pending != nil && (now.Sub(f.pending.sent) > 10*time.Second || next == nil || next.ID != f.pending.entry) {
		f.pending = nil
	}

	if next == nil || f.held || f.pending != nil || f.buffered != nil || now.Before(f.retryAt) {
		return
	}
	idle := f.status.CurrentSong == ""
	if err := f.queueSong(ctx, *next); err != nil {
		f.log.Warn("queueing a song on the unit", "number", next.Number, "error", err)
		f.retryAt = now.Add(5 * time.Second)
		return
	}
	s := &slot{entry: next.ID, number: next.Number, title: next.Title, sent: now}
	if idle {
		// The unit starts a song added while it is idle at once.
		f.pending = s
	} else {
		f.buffered = s
	}
}

// unbuffer takes the buffered song off the unit.
func (f *Feeder) unbuffer(ctx context.Context) {
	b := f.buffered
	f.buffered = nil
	if err := f.unit.RemoveFromQueue(ctx, strconv.FormatUint(uint64(b.number), 10)); err != nil {
		f.log.Warn("removing the next song from the unit", "number", b.number, "error", err)
	}
}

// queueSong adds an entry to the unit through a short session named after its
// singer, or through the long session for a song with no singer.
func (f *Feeder) queueSong(ctx context.Context, e fair.Entry) error {
	number := strconv.FormatUint(uint64(e.Number), 10)
	if e.Seat == nil {
		return f.unit.Reserve(ctx, number)
	}
	id, ok := f.singer[*e.Seat]
	name := protocol.CleanName(e.Singer)
	if strings.TrimSpace(e.Singer) == "" {
		// A singer with no name is known by number, as KM Remote shows it.
		name = "Singer"
		f.opts.Room.View(func(ev *fair.Evening, _ time.Time) {
			if seat, ok := ev.Seats().Get(*e.Seat); ok && seat.Guest != 0 {
				name = fmt.Sprintf("Guest %d", seat.Guest)
			}
		})
	}
	if !ok || id.Name != name {
		if !ok {
			id = protocol.NewIdentity()
		}
		id.Name = name
		f.singer[*e.Seat] = id
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s, err := f.opts.Dialer.Dial(dialCtx, id)
	if err != nil {
		return fmt.Errorf("opening a session as %q: %w", name, err)
	}
	err = s.Reserve(ctx, number)
	if cerr := s.Close(); cerr != nil {
		f.log.Warn("closing a singer's session", "error", cerr)
	}
	return err
}

// transport works out what the unit is doing.
func (f *Feeder) transport(now time.Time) Transport {
	switch {
	case f.status == nil || f.status.CurrentSong == "":
		if f.stopped {
			return Stopped
		}
		return Idle
	case now.Before(f.pauseUntil):
		if f.pauseWant {
			return Paused
		}
		return Playing
	case f.stalled:
		return Paused
	default:
		return Playing
	}
}

func (f *Feeder) publishState(now time.Time) {
	st := State{Connected: f.unit != nil, Transport: f.transport(now), Held: f.held}
	if f.status != nil {
		if f.status.SongPosition >= 0 {
			st.Position = f.status.SongPosition
		} else {
			st.Position = 0
		}
		st.Key, st.TempoSteps, st.Volume = f.status.Key, f.status.Tempo, f.status.MusicVolume
	}
	if f.playing != nil {
		c := *f.playing
		st.Song = &c
	}
	f.mu.Lock()
	f.state = st
	f.mu.Unlock()
	f.emit(StateChanged{State: st})
}

func (f *Feeder) setConnected(on bool) {
	if !on {
		f.playing = nil
	}
	f.publishState(f.now())
}

func (f *Feeder) emit(e Event) {
	if f.opts.OnEvent != nil {
		f.opts.OnEvent(e)
	}
}

// Transport controls. Each runs on the loop's goroutine and fails with
// ErrOffline while no session is open.

// Play starts playback: it resumes a paused song, or ends a hold so the next
// song starts.
func (f *Feeder) Play(ctx context.Context) error {
	return f.do(ctx, func(ctx context.Context) error {
		now := f.now()
		f.held = false
		if f.transport(now) == Paused {
			if err := f.unit.Pause(ctx); err != nil {
				return err
			}
			f.pauseWant, f.pauseUntil = false, now.Add(pauseShows)
		}
		f.publishState(now)
		return nil
	})
}

// Pause pauses the playing song.
func (f *Feeder) Pause(ctx context.Context) error {
	return f.do(ctx, func(ctx context.Context) error {
		now := f.now()
		if f.transport(now) != Playing {
			return nil
		}
		if err := f.unit.Pause(ctx); err != nil {
			return err
		}
		f.pauseWant, f.pauseUntil = true, now.Add(pauseShows)
		f.publishState(now)
		return nil
	})
}

// Skip ends the playing song and starts the next one.
func (f *Feeder) Skip(ctx context.Context) error {
	return f.do(ctx, func(ctx context.Context) error {
		if f.playing == nil {
			return nil
		}
		f.cut = true
		if f.buffered != nil && f.buffered.confirmed {
			return f.unit.NextSong(ctx)
		}
		// Nothing is buffered yet: play the next song directly, or end this one.
		var next *fair.Entry
		adoptedEntry := map[uint64]bool{}
		for _, entry := range f.adopted {
			adoptedEntry[entry] = true
		}
		for _, e := range f.opts.Room.Entries() {
			if !adoptedEntry[e.ID] {
				next = &e
				break
			}
		}
		if next == nil || f.held {
			return f.unit.Stop(ctx)
		}
		if f.buffered != nil {
			f.unbuffer(ctx)
		}
		if err := f.unit.PlayNow(ctx, strconv.FormatUint(uint64(next.Number), 10)); err != nil {
			return err
		}
		f.pending = &slot{entry: next.ID, number: next.Number, title: next.Title, sent: f.now()}
		return nil
	})
}

// Restart plays the current song again from the start.
func (f *Feeder) Restart(ctx context.Context) error {
	return f.do(ctx, func(ctx context.Context) error {
		if f.playing == nil {
			return nil
		}
		// The song id field reads 0 after a pause, so the number comes from
		// the song as it started.
		if err := f.unit.PlayNow(ctx, strconv.FormatUint(uint64(f.playing.Number), 10)); err != nil {
			return err
		}
		f.restarting = true
		f.cut = true
		return nil
	})
}

// Stop stops playback and holds it until Play.
func (f *Feeder) Stop(ctx context.Context) error {
	return f.do(ctx, func(ctx context.Context) error {
		f.held = true
		if f.buffered != nil {
			f.unbuffer(ctx)
		}
		f.pending = nil
		if f.playing == nil {
			f.publishState(f.now())
			return nil
		}
		f.cut = true
		if err := f.unit.Stop(ctx); err != nil {
			return err
		}
		f.stopped = true
		f.publishState(f.now())
		return nil
	})
}

// Hold holds playback without stopping the playing song: nothing new starts
// until Play.
func (f *Feeder) Hold(ctx context.Context) error {
	return f.do(ctx, func(ctx context.Context) error {
		f.held = true
		f.publishState(f.now())
		return nil
	})
}

// SetTranspose sets the key, in semitones from the song's own.
func (f *Feeder) SetTranspose(ctx context.Context, semitones int) error {
	return f.do(ctx, func(ctx context.Context) error {
		if f.status == nil {
			return ErrOffline
		}
		delta := semitones - f.status.Key
		if delta == 0 {
			return nil
		}
		return f.unit.SetKey(ctx, delta)
	})
}

// SetTempo sets the tempo as a ratio of the written tempo.
func (f *Feeder) SetTempo(ctx context.Context, ratio float64) error {
	return f.do(ctx, func(ctx context.Context) error {
		if f.status == nil {
			return ErrOffline
		}
		delta := TempoSteps(ratio) - f.status.Tempo
		if delta == 0 {
			return nil
		}
		return f.unit.SetTempo(ctx, delta)
	})
}

// SetVolume sets the music volume, 0 to 1.
func (f *Feeder) SetVolume(ctx context.Context, level float64) error {
	return f.do(ctx, func(ctx context.Context) error {
		return f.unit.SetVolume(ctx, VolumeLevel(level))
	})
}

// TempoSteps converts a tempo ratio to the unit's steps. The unit reports
// tempo as a duration ratio of 1000 per mille, one step being 1000/30, so a
// step of s plays at 1/(1-s/30) of the written tempo.
func TempoSteps(ratio float64) int {
	if ratio <= 0 {
		return 0
	}
	return int(math.Round(30 * (1 - 1/ratio)))
}

// TempoRatio converts the unit's tempo steps to a ratio.
func TempoRatio(steps int) float64 {
	return 1 / (1 - float64(steps)/30)
}

// VolumeLevel converts a volume of 0 to 1 to the unit's grid of 0 to 100.
func VolumeLevel(level float64) int {
	return protocol.VolumeLevel(int(math.Round(min(max(level, 0), 1) * protocol.VolumeSteps)))
}
