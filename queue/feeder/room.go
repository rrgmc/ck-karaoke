package feeder

import (
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/queue/fair"
)

// Room is the evening shared by the feeder and whatever takes requests (the
// KaraokeMachine API, the operator page). It is a fair.Evening behind one lock,
// and it tells its subscribers whenever the evening changes.
type Room struct {
	mu      sync.Mutex
	evening *fair.Evening
	lookup  func(number uint32) (fair.Song, bool)
	now     func() time.Time

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// NewRoom returns a room around an evening. lookup describes a song number
// from the catalogue, for the songs the feeder finds on the unit; it may be
// nil.
func NewRoom(e *fair.Evening, lookup func(uint32) (fair.Song, bool)) *Room {
	if lookup == nil {
		lookup = func(n uint32) (fair.Song, bool) { return fair.Song{Number: n}, false }
	}
	return &Room{evening: e, lookup: lookup, now: time.Now, subs: map[chan struct{}]struct{}{}}
}

// Do runs fn on the evening under the lock and then tells every subscriber
// that the evening may have changed.
func (r *Room) Do(fn func(e *fair.Evening, now time.Time)) {
	r.mu.Lock()
	fn(r.evening, r.now())
	r.mu.Unlock()
	r.notify()
}

// View runs fn on the evening under the lock, for reading. fn must not change
// the evening.
func (r *Room) View(fn func(e *fair.Evening, now time.Time)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.evening, r.now())
}

// Subscribe returns a channel that receives a value after each change, and a
// function that ends the subscription. Changes that arrive while a value is
// still unread are folded into it.
func (r *Room) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	r.subMu.Lock()
	r.subs[ch] = struct{}{}
	r.subMu.Unlock()
	return ch, func() {
		r.subMu.Lock()
		delete(r.subs, ch)
		r.subMu.Unlock()
	}
}

func (r *Room) notify() {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Entries returns the queue in play order.
func (r *Room) Entries() []fair.Entry {
	var out []fair.Entry
	r.View(func(e *fair.Evening, _ time.Time) { out = e.Queue().Entries() })
	return out
}

// start records that an entry started playing.
func (r *Room) start(id uint64) (fair.Entry, bool) {
	var entry fair.Entry
	var ok bool
	r.Do(func(e *fair.Evening, now time.Time) { entry, ok = e.Start(id, now) })
	return entry, ok
}

// adopt adds a song found on the unit as a song with no singer, at index.
func (r *Room) adopt(number uint32, index int) (uint64, error) {
	song, _ := r.lookup(number)
	song.Number, song.Singer, song.Seat = number, "", nil
	var id uint64
	var err error
	r.Do(func(e *fair.Evening, _ time.Time) {
		if id, err = e.Add(song); err == nil {
			e.MoveTo(id, index)
		}
	})
	return id, err
}

// place moves an entry to index, and reports whether it moved.
func (r *Room) place(id uint64, index int) {
	moved := false
	r.mu.Lock()
	if q := r.evening.Queue(); q.PositionOf(id) >= 0 && q.PositionOf(id) != min(index, q.Len()-1) {
		moved = r.evening.MoveTo(id, index)
	}
	r.mu.Unlock()
	if moved {
		r.notify()
	}
}

// drop removes a song that left the unit without playing.
func (r *Room) drop(id uint64) {
	r.Do(func(e *fair.Evening, now time.Time) { e.Remove(id, now) })
}

// add adds a song with no singer to the end, for a song taken off the unit.
func (r *Room) add(number uint32) error {
	song, _ := r.lookup(number)
	song.Number, song.Singer, song.Seat = number, "", nil
	var err error
	r.Do(func(e *fair.Evening, _ time.Time) { _, err = e.Add(song) })
	return err
}
