// Package fair is the song queue of the queue manager: the singers, their
// requests, and the rule that gives each singer a fair turn.
//
// It is a port of KaraokeMachine's km-queue crate (evening.rs, queue.rs and
// seats.rs), and it keeps that crate's behaviour exactly, so that a remote sees
// the same queue from either server. The tests are the Rust tests, ported.
//
// Everything here is pure state with no I/O and no locking. The caller hands in
// the time, holds one lock around an Evening, and stores snapshots if it wants
// the evening to survive a restart.
package fair

import "fmt"

// MaxQueued is how many songs may wait in the queue.
//
// A cap exists so a stuck remote or a bored guest cannot queue tens of
// thousands of songs. It is high enough that a real party never reaches it.
const MaxQueued = 200

// ErrQueueFull reports that the queue is at MaxQueued.
var ErrQueueFull = fmt.Errorf("the queue is full (%d songs)", MaxQueued)

// Entry is a queued song.
type Entry struct {
	// ID is opaque, stable for the entry's lifetime and never reused.
	ID uint64 `json:"id"`
	// Number is the catalogue number.
	Number uint32 `json:"number,string"`
	// Title, so a list needs no catalogue.
	Title string `json:"title"`
	// Artist is the performer, or empty.
	Artist string `json:"artist,omitempty"`
	// Singer is the label people read, or empty for a song with no singer.
	Singer string `json:"singer,omitempty"`
	// DurationMS is the length in milliseconds, or 0 when unknown.
	DurationMS uint32 `json:"duration_ms"`
	// Seat is the seat of the singer it belongs to, or nil for a song with no
	// singer. Singer is the label; this is the identity.
	Seat *uint64 `json:"seat"`
}

// Song is what a song should be queued as.
type Song struct {
	Number     uint32
	Title      string
	Artist     string
	Singer     string
	DurationMS uint32
	Seat       *uint64
}

// Queue holds the waiting songs, in play order.
//
// Entries are addressed by id rather than position: a remote that deletes
// "entry 3" after somebody else removed entry 1 would otherwise delete the
// wrong song.
type Queue struct {
	entries []Entry
	nextID  uint64
}

// NewQueueFromParts returns a queue holding entries in play order, for a store
// that kept them. The next id is past every id here, whatever nextID says, so
// no id is issued twice.
func NewQueueFromParts(entries []Entry, nextID uint64) *Queue {
	for _, e := range entries {
		nextID = max(nextID, e.ID+1)
	}
	return &Queue{entries: append([]Entry(nil), entries...), nextID: nextID}
}

// NextID returns the id the next entry takes.
func (q *Queue) NextID() uint64 { return q.nextID }

// Len returns how many songs are waiting.
func (q *Queue) Len() int { return len(q.entries) }

// Entries returns a copy of the waiting songs, in play order.
func (q *Queue) Entries() []Entry { return cloneEntries(q.entries) }

// Peek returns the song that plays next.
func (q *Queue) Peek() (Entry, bool) {
	if len(q.entries) == 0 {
		return Entry{}, false
	}
	return cloneEntry(q.entries[0]), true
}

// Add adds a song to the back and returns its id.
func (q *Queue) Add(s Song) (uint64, error) {
	if len(q.entries) >= MaxQueued {
		return 0, ErrQueueFull
	}
	id := q.nextID
	// Ids are never reused, so a stale reference from a remote fails to match
	// rather than hitting whatever took the old id's place.
	q.nextID++
	q.entries = append(q.entries, Entry{
		ID: id, Number: s.Number, Title: s.Title, Artist: s.Artist,
		Singer: s.Singer, DurationMS: s.DurationMS, Seat: cloneID(s.Seat),
	})
	return id, nil
}

// Remove removes an entry by id and returns it if it was there.
func (q *Queue) Remove(id uint64) (Entry, bool) {
	i := q.PositionOf(id)
	if i < 0 {
		return Entry{}, false
	}
	e := q.entries[i]
	q.entries = append(q.entries[:i], q.entries[i+1:]...)
	return e, true
}

// Pop takes the next song off the front.
func (q *Queue) Pop() (Entry, bool) {
	if len(q.entries) == 0 {
		return Entry{}, false
	}
	e := q.entries[0]
	q.entries = q.entries[1:]
	return e, true
}

// MoveTo moves an entry to a position, clamped to the queue's bounds. It
// reports false when the id is unknown.
func (q *Queue) MoveTo(id uint64, index int) bool {
	e, ok := q.Remove(id)
	if !ok {
		return false
	}
	// After removal the queue is one shorter, so the target is clamped against
	// the new length.
	index = min(max(index, 0), len(q.entries))
	q.entries = append(q.entries, Entry{})
	copy(q.entries[index+1:], q.entries[index:])
	q.entries[index] = e
	return true
}

// Clear empties the queue and returns what was removed.
func (q *Queue) Clear() []Entry {
	gone := q.entries
	q.entries = nil
	return gone
}

// PositionOf returns an entry's position, or -1.
func (q *Queue) PositionOf(id uint64) int {
	for i, e := range q.entries {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// HoldsSeat reports whether a seat has a song waiting here.
func (q *Queue) HoldsSeat(seat uint64) bool {
	for _, e := range q.entries {
		if e.Seat != nil && *e.Seat == seat {
			return true
		}
	}
	return false
}

// RelabelSeat puts a seat's name on each of its entries, or takes it off.
func (q *Queue) RelabelSeat(seat uint64, name string) {
	for i := range q.entries {
		if e := &q.entries[i]; e.Seat != nil && *e.Seat == seat {
			e.Singer = name
		}
	}
}

// Reseat gives every entry of one seat to another, with the other's name. The
// entries keep their places.
func (q *Queue) Reseat(from, into uint64, name string) {
	for i := range q.entries {
		if e := &q.entries[i]; e.Seat != nil && *e.Seat == from {
			e.Seat = &into
			e.Singer = name
		}
	}
}

// RemoveSeat removes every entry of one seat and returns them.
func (q *Queue) RemoveSeat(seat uint64) []Entry {
	var gone, kept []Entry
	for _, e := range q.entries {
		if e.Seat != nil && *e.Seat == seat {
			gone = append(gone, e)
		} else {
			kept = append(kept, e)
		}
	}
	q.entries = kept
	return gone
}

// ContainsNumber reports whether a song number is already waiting. Duplicates
// are allowed; a remote may want to warn first.
func (q *Queue) ContainsNumber(number uint32) bool {
	for _, e := range q.entries {
		if e.Number == number {
			return true
		}
	}
	return false
}

// Label is a one-line description of an entry, for a log or a display.
func (e Entry) Label() string {
	s := fmt.Sprintf("%d  %s", e.Number, e.Title)
	if e.Artist != "" {
		s += " — " + e.Artist
	}
	if e.Singer != "" {
		s += " (" + e.Singer + ")"
	}
	return s
}

func cloneID(s *uint64) *uint64 {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

func cloneEntry(e Entry) Entry {
	e.Seat = cloneID(e.Seat)
	return e
}

func cloneEntries(es []Entry) []Entry {
	out := make([]Entry, len(es))
	for i, e := range es {
		out[i] = cloneEntry(e)
	}
	return out
}

// idIs reports whether an optional id is the given one.
func idIs(p *uint64, id uint64) bool { return p != nil && *p == id }
