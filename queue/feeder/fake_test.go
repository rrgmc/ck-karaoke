package feeder

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/protocol"
)

// fakeUnit simulates a karaoke unit as the real one behaves (see the package
// documentation): it starts a song added while idle, plays its queue in order,
// refuses a song it already holds, and reports a pause only as a position that
// stops moving.
type fakeUnit struct {
	mu       sync.Mutex
	length   time.Duration
	cur      uint32
	title    string
	idField  string // what the status reports
	lastID   string // the id of the last song started
	stopped  bool
	pos      time.Duration
	paused   bool
	queue    []fakeEntry
	sessions []*fakeSession
	log      []string
	key      int
	tempo    int
	volume   int
}

type fakeEntry struct {
	number uint32
	by     string
}

type fakeSession struct {
	u        *fakeUnit
	name     string
	ctx      context.Context
	cancel   context.CancelFunc
	onStatus func(*protocol.Status)
	onQueue  func(*protocol.Queue)
}

func newFakeUnit() *fakeUnit { return &fakeUnit{length: 3 * time.Minute, volume: 50} }

func (u *fakeUnit) Dial(_ context.Context, id protocol.Identity) (Unit, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &fakeSession{u: u, name: id.DisplayName(), ctx: ctx, cancel: cancel}
	u.mu.Lock()
	u.sessions = append(u.sessions, s)
	u.mu.Unlock()
	return s, nil
}

// push is a status or queue to deliver once the lock is released.
type push func()

func (u *fakeUnit) deliver(ps []push) {
	for _, p := range ps {
		p()
	}
}

func (u *fakeUnit) statusLocked(pos time.Duration) push {
	st := &protocol.Status{CurrentSong: u.title, CurrentSongID: u.idField, SongPosition: pos,
		Key: u.key, Tempo: u.tempo, MusicVolume: u.volume}
	var fns []func(*protocol.Status)
	for _, s := range u.sessions {
		if s.onStatus != nil {
			fns = append(fns, s.onStatus)
		}
	}
	return func() {
		for _, fn := range fns {
			fn(st)
		}
	}
}

func (u *fakeUnit) queueLocked() push {
	q := &protocol.Queue{Total: len(u.queue), End: len(u.queue)}
	for i, e := range u.queue {
		q.Entries = append(q.Entries, protocol.QueueEntry{SongID: strconv.Itoa(int(e.number)),
			Fields: []string{"123456789", fmt.Sprintf("%d.%s", i+1, e.by), "1"}})
	}
	var fns []func(*protocol.Queue)
	for _, s := range u.sessions {
		if s.onQueue != nil {
			fns = append(fns, s.onQueue)
		}
	}
	return func() {
		for _, fn := range fns {
			fn(q)
		}
	}
}

// startLocked starts a song: a status with the large negative position that
// marks a song change, then the queue.
func (u *fakeUnit) startLocked(number uint32) []push {
	u.cur, u.title, u.idField, u.pos, u.paused = number, fmt.Sprintf("SONG %d", number), strconv.Itoa(int(number)), 0, false
	u.lastID, u.stopped = u.idField, false
	return []push{u.statusLocked(-1580327258), u.queueLocked()}
}

// startStaleLocked starts a song as NextSong does: the change status carries
// the new title but the previous song's id.
func (u *fakeUnit) startStaleLocked(number uint32) []push {
	old := u.lastID
	ps := u.startLocked(number)
	u.idField = old
	ps[0] = u.statusLocked(-1580327258)
	u.idField = strconv.Itoa(int(number))
	return ps
}

// startAfterStopLocked starts a song added while the unit is stopped: its
// first status has no change marker and the previous song's id.
func (u *fakeUnit) startAfterStopLocked(number uint32) []push {
	old := u.lastID
	ps := u.startLocked(number)
	u.idField = old
	ps[0] = u.statusLocked(0)
	u.idField = strconv.Itoa(int(number))
	return ps
}

// Advance plays for d and reports a status, starting the next song when one
// ends.
func (u *fakeUnit) Advance(d time.Duration) {
	u.mu.Lock()
	var ps []push
	if u.title != "" && !u.paused {
		u.pos += d
		if u.pos >= u.length {
			u.pos = u.length
			if len(u.queue) > 0 {
				next := u.queue[0]
				u.queue = u.queue[1:]
				ps = u.startLocked(next.number)
			} else {
				u.title, u.pos = "", 0
			}
		}
	}
	if ps == nil {
		ps = []push{u.statusLocked(u.pos)}
	}
	u.mu.Unlock()
	u.deliver(ps)
}

// End plays the current song to its end, reporting a status one second
// before it, as the real unit's two-second reports would.
func (u *fakeUnit) End() {
	u.mu.Lock()
	rest := u.length - u.pos
	u.mu.Unlock()
	u.Advance(rest - time.Second)
	u.Advance(time.Second)
}

func (u *fakeUnit) record(s string) {
	u.log = append(u.log, s)
}

func (u *fakeUnit) Log() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.log)
}

func (u *fakeUnit) Queue() []fakeEntry {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.queue)
}

func (u *fakeUnit) Current() (uint32, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.title == "" {
		return 0, ""
	}
	return u.cur, u.title
}

// The session methods.

func (s *fakeSession) Reserve(_ context.Context, id string) error {
	n, _ := strconv.Atoi(id)
	u := s.u
	u.mu.Lock()
	u.record("AddOrder " + id + " as " + s.name)
	var ps []push
	switch {
	case u.title == "" && u.stopped:
		ps = u.startAfterStopLocked(uint32(n))
	case u.title == "":
		ps = u.startLocked(uint32(n))
	case slices.ContainsFunc(u.queue, func(e fakeEntry) bool { return e.number == uint32(n) }):
		// Refused silently.
	default:
		u.queue = append(u.queue, fakeEntry{number: uint32(n), by: s.name})
		ps = []push{u.queueLocked()}
	}
	u.mu.Unlock()
	u.deliver(ps)
	return nil
}

func (s *fakeSession) RemoveFromQueue(_ context.Context, id string) error {
	n, _ := strconv.Atoi(id)
	u := s.u
	u.mu.Lock()
	u.record("DelOrder " + id)
	u.queue = slices.DeleteFunc(u.queue, func(e fakeEntry) bool { return e.number == uint32(n) })
	ps := []push{u.queueLocked()}
	u.mu.Unlock()
	u.deliver(ps)
	return nil
}

func (s *fakeSession) PlayNow(_ context.Context, id string) error {
	n, _ := strconv.Atoi(id)
	u := s.u
	u.mu.Lock()
	u.record("PlayEx " + id)
	noID := u.title != "" && u.cur == uint32(n) && u.idField == "0"
	ps := u.startLocked(uint32(n))
	if noID {
		// Playing the current song again after a pause keeps the id at 0.
		u.idField = "0"
		ps[0] = u.statusLocked(-1580327258)
	}
	u.mu.Unlock()
	u.deliver(ps)
	return nil
}

func (s *fakeSession) NextSong(context.Context) error {
	u := s.u
	u.mu.Lock()
	u.record("NextSong")
	var ps []push
	if len(u.queue) > 0 {
		next := u.queue[0]
		u.queue = u.queue[1:]
		ps = u.startStaleLocked(next.number)
	}
	u.mu.Unlock()
	u.deliver(ps)
	return nil
}

func (s *fakeSession) Pause(context.Context) error {
	u := s.u
	u.mu.Lock()
	u.record("Pause")
	u.paused = !u.paused
	u.idField = "0"
	u.mu.Unlock()
	return nil
}

func (s *fakeSession) Stop(context.Context) error {
	u := s.u
	u.mu.Lock()
	u.record("Stop")
	u.title, u.pos, u.paused, u.stopped = "", 0, false, true
	ps := []push{u.statusLocked(0)}
	u.mu.Unlock()
	u.deliver(ps)
	return nil
}

func (s *fakeSession) SetKey(_ context.Context, d int) error {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	s.u.record(fmt.Sprintf("SetKey %d", d))
	s.u.key += d
	return nil
}

func (s *fakeSession) SetTempo(_ context.Context, d int) error {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	s.u.record(fmt.Sprintf("SetTempo %d", d))
	s.u.tempo += d
	return nil
}

func (s *fakeSession) SetVolume(_ context.Context, level int) error {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	s.u.record(fmt.Sprintf("SetVolume %d", level))
	s.u.volume = level
	return nil
}

func (s *fakeSession) Queue(context.Context, int, int) (*protocol.Queue, error) {
	s.u.mu.Lock()
	p := s.u.queueLocked()
	s.u.mu.Unlock()
	p()
	return nil, nil
}

func (s *fakeSession) OnStatus(fn func(*protocol.Status)) {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	s.onStatus = fn
}

func (s *fakeSession) OnQueue(fn func(*protocol.Queue)) {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	s.onQueue = fn
}

func (s *fakeSession) Context() context.Context { return s.ctx }

func (s *fakeSession) Close() error {
	s.cancel()
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	s.u.sessions = slices.DeleteFunc(s.u.sessions, func(x *fakeSession) bool { return x == s })
	return nil
}
