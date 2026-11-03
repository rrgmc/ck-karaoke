package fair

import (
	"errors"
	"fmt"
	"time"
)

// The rule is one sentence: in Turns order, the queue holds one song for each
// singer. A singer's song is a request. The evening accepts it at once when
// that singer has no entry in the queue, and holds it otherwise. When a
// singer's entry leaves the queue, that singer's oldest waiting request goes in
// at the end. So the singers take turns, and nobody is in charge.
//
// Every change to the queue goes through Evening, and each one ends in settle.
// The rule reads the queue and the request list together, so a change made to
// one behind the other's back would leave a singer with two songs or none. An
// entry leaves when its song starts, when somebody removes it and when its
// singer withdraws it, and the rule treats all three alike.
//
// The rule places a song once and never moves an entry afterwards. Whatever
// somebody moves by hand stays where they put it. A song with no singer passes
// the rule: it goes straight to the end and holds back no other song.

// MaxRequests is how many requests the list may hold, finished ones included.
const MaxRequests = 1000

// FinishedFor is how long a finished request stays in the list: long enough
// for the phone that asked to read what became of it.
const FinishedFor = 10 * time.Minute

// RequestState is where a request stands.
type RequestState string

// The request states.
const (
	// Waiting: the singer already has a song in the queue, or the queue is
	// full.
	Waiting RequestState = "waiting"
	// Queued: accepted; Request.Entry names its queue entry.
	Queued RequestState = "queued"
	// Played: the song started.
	Played RequestState = "played"
	// Declined: somebody at the control level took it out.
	Declined RequestState = "declined"
	// Withdrawn: its singer took it back.
	Withdrawn RequestState = "withdrawn"
)

// Finished reports whether nothing further happens to a request in this state.
func (s RequestState) Finished() bool {
	return s == Played || s == Declined || s == Withdrawn
}

// Request is one song a singer asked for, through its whole course.
type Request struct {
	// ID is opaque, never reused, and orders requests by age.
	ID uint64 `json:"id"`
	// Seat is the seat of the singer who asked.
	Seat       uint64       `json:"seat"`
	Number     uint32       `json:"number,string"`
	Title      string       `json:"title"`
	Artist     string       `json:"artist,omitempty"`
	DurationMS uint32       `json:"duration_ms"`
	State      RequestState `json:"state"`
	// Entry is the queue entry it became, from the moment it was accepted.
	// It is kept after the entry is gone.
	Entry *uint64 `json:"entry"`
}

// RequestedSong is the song a request names, as the catalogue describes it.
type RequestedSong struct {
	Number     uint32
	Title      string
	Artist     string
	DurationMS uint32
}

// RequestRules is what the owner's settings ask of a request.
type RequestRules struct {
	// MaxWaiting is how many requests one singer may have waiting.
	MaxWaiting uint32
}

// DefaultRequestRules allows ten waiting songs, more turns than one singer gets
// in an evening of several.
var DefaultRequestRules = RequestRules{MaxWaiting: 10}

// Sung records a song that started this evening.
type Sung struct {
	Number uint32  `json:"number,string"`
	Seat   *uint64 `json:"seat"`
}

func (s Sung) equal(o Sung) bool {
	if s.Number != o.Number || (s.Seat == nil) != (o.Seat == nil) {
		return false
	}
	return s.Seat == nil || *s.Seat == *o.Seat
}

// Proof is who is asking: the seat's own token, or a caller whose level is the
// proof (ByLevel).
type Proof struct {
	Token   string
	ByLevel bool
}

// ByToken returns a proof by the seat's token.
func ByToken(token string) Proof { return Proof{Token: token} }

// ByLevel is the proof of a caller whose access level lets it act for any
// singer.
var ByLevel = Proof{ByLevel: true}

// Request errors.
var (
	ErrNoSinger       = errors.New("no such singer")
	ErrSingerFull     = errors.New("this singer has as many songs waiting as the machine allows")
	ErrRequestsFull   = fmt.Errorf("the machine holds as many requests as it can (%d)", MaxRequests)
	ErrRequestUnknown = errors.New("no such request")
	ErrFinished       = errors.New("that request has already finished")
	ErrClosed         = errors.New("the machine is taking no more requests tonight")
	ErrNotWaiting     = errors.New("that request is not waiting")
)

// Order is the order a singer's request takes into the queue.
type Order string

const (
	// Arrival sends every song to the end of the queue as it is asked for. It
	// is the default: a home queues songs one after another and has no turns.
	Arrival Order = "arrival"
	// Turns keeps one song for each singer in the queue; the singer's next one
	// waits until it starts. A venue chooses it, so one table cannot fill the
	// queue.
	Turns Order = "turns"
)

// Snapshot is everything an evening holds, as a store keeps it.
//
// It holds no time: a finished request carries its state alone, and Restore
// starts its FinishedFor again. So two snapshots of an unchanged evening are
// equal, and a store compares them to learn whether it has anything to write.
type Snapshot struct {
	Queue        []Entry      `json:"queue"`
	QueueNext    uint64       `json:"queue_next"`
	Seats        []SeatRecord `json:"seats"`
	SeatsNext    uint64       `json:"seats_next"`
	GuestsNext   uint32       `json:"guests_next"`
	Requests     []Request    `json:"requests"`
	RequestsNext uint64       `json:"requests_next"`
	Sung         []Sung       `json:"sung"`
	Accepting    bool         `json:"accepting"`
	// Playing is the entry of the song that is playing, when it came from the
	// queue. Restore puts it back at the front, so a singer loses no turn to a
	// restart.
	Playing *Entry `json:"playing"`
}

// ErrDuplicateID reports a snapshot in which two records of one kind share an
// id.
type ErrDuplicateID struct{ What string }

func (e ErrDuplicateID) Error() string { return fmt.Sprintf("two %s carry the same id", e.What) }

type held struct {
	request    Request
	finishedAt time.Time // zero while unfinished
}

// Evening is the queue, the singers and their requests.
type Evening struct {
	queue       *Queue
	seats       *Seats
	requests    []held
	nextRequest uint64
	sung        []Sung
	accepting   bool
	order       Order
}

// NewEvening returns an evening with nobody in it, taking requests in order.
// An empty order is Arrival.
func NewEvening(order Order) *Evening {
	if order == "" {
		order = Arrival
	}
	return &Evening{queue: &Queue{}, seats: &Seats{}, accepting: true, order: order}
}

// Order returns the order a request takes into the queue.
func (e *Evening) Order() Order { return e.order }

// SetOrder changes the order for the rest of the evening. A change to Arrival
// empties the waiting list into the queue in the order the songs were asked
// for; a change to Turns moves nothing already queued.
func (e *Evening) SetOrder(order Order, now time.Time) {
	e.order = order
	e.settle(now)
}

// Sung returns every song that started this evening, in order.
func (e *Evening) Sung() []Sung { return cloneSung(e.sung) }

// Accepting reports whether a phone may ask for a song.
func (e *Evening) Accepting() bool { return e.accepting }

// SetAccepting opens or closes the evening to new requests from a phone. A
// request that already waits still enters the queue in its turn, and a caller
// whose level is the proof still adds one.
func (e *Evening) SetAccepting(accepting bool) { e.accepting = accepting }

// Reset starts a new evening: the queue, the requests, the record and every
// seat go, and the evening takes requests again. Ids go on counting.
func (e *Evening) Reset() {
	e.queue.Clear()
	e.requests = nil
	e.sung = nil
	e.seats.Clear()
	e.accepting = true
}

// Snapshot returns everything the evening holds, for a store. playing is the
// entry of the song that is playing, when it came from the queue. A finished
// request whose FinishedFor has passed is left out.
func (e *Evening) Snapshot(playing *Entry, now time.Time) Snapshot {
	var p *Entry
	if playing != nil {
		c := cloneEntry(*playing)
		p = &c
	}
	return Snapshot{
		Queue:        e.queue.Entries(),
		QueueNext:    e.queue.NextID(),
		Seats:        e.seats.Records(),
		SeatsNext:    e.seats.NextID(),
		GuestsNext:   e.seats.NextGuest(),
		Requests:     e.Requests(now),
		RequestsNext: e.nextRequest,
		Sung:         cloneSung(e.sung),
		Accepting:    e.accepting,
		Playing:      p,
	}
}

// Restore builds the evening a snapshot describes.
//
// The song that was playing goes back to the front, and its record and its
// request's played state are undone, because the song starts again. No id is
// issued twice: each counter ends past every id the snapshot holds, and a
// snapshot in which two records share an id is refused. The lists are cut to
// their caps. A queued request whose entry is absent waits again. A finished
// request stays for FinishedFor from now.
//
// order comes from the settings and not from the snapshot.
func Restore(s Snapshot, order Order, now time.Time) (*Evening, error) {
	queue := cloneEntries(s.Queue)
	seats := append([]SeatRecord(nil), s.Seats...)
	requests := cloneRequests(s.Requests)
	sung := cloneSung(s.Sung)

	if s.Playing != nil {
		playing := cloneEntry(*s.Playing)
		record := Sung{Number: playing.Number, Seat: cloneID(playing.Seat)}
		if n := len(sung); n > 0 && sung[n-1].equal(record) {
			sung = sung[:n-1]
		}
		for i := range requests {
			if r := &requests[i]; r.Entry != nil && *r.Entry == playing.ID && r.State == Played {
				r.State = Queued
			}
		}
		queue = append([]Entry{playing}, queue...)
	}
	queue = queue[:min(len(queue), MaxQueued)]
	seats = seats[:min(len(seats), MaxSeats)]
	requests = requests[:min(len(requests), MaxRequests)]

	if err := distinct("queue entries", len(queue), func(i int) uint64 { return queue[i].ID }); err != nil {
		return nil, err
	}
	if err := distinct("seats", len(seats), func(i int) uint64 { return seats[i].Seat.ID }); err != nil {
		return nil, err
	}
	if err := distinct("requests", len(requests), func(i int) uint64 { return requests[i].ID }); err != nil {
		return nil, err
	}

	queued := map[uint64]bool{}
	for _, q := range queue {
		queued[q.ID] = true
	}
	nextRequest := s.RequestsNext
	hs := make([]held, 0, len(requests))
	for _, r := range requests {
		nextRequest = max(nextRequest, r.ID+1)
		if r.State == Queued && (r.Entry == nil || !queued[*r.Entry]) {
			r.State = Waiting
			r.Entry = nil
		}
		h := held{request: r}
		if r.State.Finished() {
			h.finishedAt = now
		}
		hs = append(hs, h)
	}

	if order == "" {
		order = Arrival
	}
	e := &Evening{
		queue:       NewQueueFromParts(queue, s.QueueNext),
		seats:       NewSeatsFromRecords(seats, s.SeatsNext, s.GuestsNext),
		requests:    hs,
		nextRequest: nextRequest,
		sung:        sung,
		accepting:   s.Accepting,
		order:       order,
	}
	e.settle(now)
	return e, nil
}

// Queue returns the waiting songs. The caller must not change it; every change
// goes through the Evening.
func (e *Evening) Queue() *Queue { return e.queue }

// Seats returns the singers. The caller must not change them.
func (e *Evening) Seats() *Seats { return e.seats }

// Requests returns every request the list holds, oldest first. A finished one
// is left out once FinishedFor has passed.
func (e *Evening) Requests(now time.Time) []Request {
	var out []Request
	for _, h := range e.requests {
		if !expired(h, now) {
			out = append(out, cloneRequest(h.request))
		}
	}
	return out
}

// The queue.

// Add adds a song with no singer to the end of the queue. It passes the rule.
func (e *Evening) Add(s Song) (uint64, error) { return e.queue.Add(s) }

// Pop takes the next song off the front, because it is starting. Its request
// reads Played, and its singer's next request goes in.
func (e *Evening) Pop(now time.Time) (Entry, bool) {
	entry, ok := e.queue.Pop()
	if !ok {
		return Entry{}, false
	}
	e.sung = append(e.sung, Sung{Number: entry.Number, Seat: cloneID(entry.Seat)})
	e.entryLeft(entry.ID, Played, now)
	e.settle(now)
	return entry, true
}

// Remove removes an entry, for a caller at the control level. Its request reads
// Declined, and its singer's next request goes in.
func (e *Evening) Remove(id uint64, now time.Time) (Entry, bool) {
	entry, ok := e.queue.Remove(id)
	if !ok {
		return Entry{}, false
	}
	e.entryLeft(entry.ID, Declined, now)
	e.settle(now)
	return entry, true
}

// MoveTo moves an entry to a position. The rule never moves it again.
func (e *Evening) MoveTo(id uint64, index int) bool { return e.queue.MoveTo(id, index) }

// Clear removes every entry, for a caller at the control level. Each one's
// request reads Declined, and each singer's next waiting request goes in.
func (e *Evening) Clear(now time.Time) []Entry {
	removed := e.queue.Clear()
	for _, entry := range removed {
		e.entryLeft(entry.ID, Declined, now)
	}
	e.settle(now)
	return removed
}

// The singers.

// Register registers a singer; see Seats.Register. A seat holds a song while
// one of its songs is queued or waiting.
func (e *Evening) Register(device string, name *SeatName, token string, rules SeatRules) (Issued, error) {
	return e.seats.Register(device, name, token, rules, func(seat uint64) bool {
		if e.queue.HoldsSeat(seat) {
			return true
		}
		for _, h := range e.requests {
			if h.request.Seat == seat && h.request.State == Waiting {
				return true
			}
		}
		return false
	})
}

// Rename gives a seat a name, or another name; see Seats.Rename. The seat's
// queued songs take the name too.
func (e *Evening) Rename(id uint64, token string, name SeatName) (Seat, error) {
	seat, err := e.seats.Rename(id, token, name)
	if err != nil {
		return Seat{}, err
	}
	e.queue.RelabelSeat(seat.ID, seat.Name)
	return seat, nil
}

// OpenSeat opens a seat for another phone to take; see Seats.Open.
func (e *Evening) OpenSeat(id uint64) (Seat, error) { return e.seats.Open(id) }

// CreateSinger creates a singer who has no phone; see Seats.Create.
func (e *Evening) CreateSinger(name *SeatName) (Seat, error) { return e.seats.Create(name) }

// RemoveSinger removes a singer who went home: the seat, its queued songs and
// its waiting ones. Each of its requests reads Declined, and the rule runs.
func (e *Evening) RemoveSinger(id uint64, now time.Time) (Seat, error) {
	seat, ok := e.seats.Remove(id)
	if !ok {
		return Seat{}, ErrSeatNotFound
	}
	e.queue.RemoveSeat(id)
	for i := range e.requests {
		if h := &e.requests[i]; h.request.Seat == id && !h.request.State.Finished() {
			h.request.State = Declined
			h.finishedAt = now
		}
	}
	e.settle(now)
	return seat, nil
}

// Merge joins two seats that are one person: from goes, and into takes what it
// held. The requests, the queue entries and the record all move. The rule may
// then find two entries for one singer; it leaves both and accepts no third
// while either is there.
func (e *Evening) Merge(from, into uint64) (Seat, error) {
	if from == into {
		return Seat{}, ErrSeatNotFound
	}
	kept, ok := e.seats.Get(into)
	if !ok {
		return Seat{}, ErrSeatNotFound
	}
	if _, ok := e.seats.Remove(from); !ok {
		return Seat{}, ErrSeatNotFound
	}
	e.queue.Reseat(from, into, kept.Name)
	for i := range e.requests {
		if e.requests[i].request.Seat == from {
			e.requests[i].request.Seat = into
		}
	}
	for i := range e.sung {
		if idIs(e.sung[i].Seat, from) {
			v := into
			e.sung[i].Seat = &v
		}
	}
	return kept, nil
}

// The requests.

// Request takes a singer's request and accepts it at once when the rule
// allows. The answer carries the state it reached: Queued with its entry, or
// Waiting.
func (e *Evening) Request(seat uint64, proof Proof, song RequestedSong, rules RequestRules,
	now time.Time) (Request, error) {
	e.sweep(now)
	if _, ok := e.seats.Get(seat); !ok {
		return Request{}, ErrNoSinger
	}
	if !proof.ByLevel {
		if !e.seats.Holds(seat, proof.Token) {
			return Request{}, ErrWrongToken
		}
		if !e.accepting {
			return Request{}, ErrClosed
		}
	}
	if len(e.requests) >= MaxRequests {
		return Request{}, ErrRequestsFull
	}
	waiting := 0
	for _, h := range e.requests {
		if h.request.Seat == seat && h.request.State == Waiting {
			waiting++
		}
	}
	// The cap is on songs that wait. A song the rule accepts at once waits for
	// nothing, so a cap of zero still gives each singer one song in the queue.
	wouldWait := waiting > 0 ||
		(e.order == Turns && e.queue.HoldsSeat(seat)) ||
		e.queue.Len() >= MaxQueued
	if wouldWait && waiting >= int(rules.MaxWaiting) {
		return Request{}, ErrSingerFull
	}

	id := e.nextRequest
	e.nextRequest++
	e.requests = append(e.requests, held{request: Request{
		ID: id, Seat: seat, Number: song.Number, Title: song.Title, Artist: song.Artist,
		DurationMS: song.DurationMS, State: Waiting,
	}})
	e.settle(now)
	return cloneRequest(e.held(id).request), nil
}

// Withdraw takes a request back, for its singer. The seat's token is the
// proof. The same token takes the song out of the queue after it was
// accepted, so a guest who asked for the wrong song needs no control code.
func (e *Evening) Withdraw(id uint64, token string, now time.Time) (Request, error) {
	e.sweep(now)
	h := e.held(id)
	if h == nil {
		return Request{}, ErrRequestUnknown
	}
	if !e.seats.Holds(h.request.Seat, token) {
		return Request{}, ErrWrongToken
	}
	if h.request.State.Finished() {
		return Request{}, ErrFinished
	}
	if h.request.Entry != nil {
		e.queue.Remove(*h.request.Entry)
	}
	e.finish(id, Withdrawn, now)
	e.settle(now)
	return cloneRequest(e.held(id).request), nil
}

// Decline refuses a request, for a caller at the control level. Waiting or
// queued, it reads Declined, and a queued one leaves the queue.
func (e *Evening) Decline(id uint64, now time.Time) (Request, error) {
	e.sweep(now)
	h := e.held(id)
	if h == nil {
		return Request{}, ErrRequestUnknown
	}
	if h.request.State.Finished() {
		return Request{}, ErrFinished
	}
	if h.request.Entry != nil {
		e.queue.Remove(*h.request.Entry)
	}
	e.finish(id, Declined, now)
	e.settle(now)
	return cloneRequest(e.held(id).request), nil
}

// Accept accepts a waiting request early, for a caller at the control level,
// at the end of the queue or next.
//
// Its singer then has a second song in the queue. The rule leaves both entries
// where they are, and accepts no third while either is there.
func (e *Evening) Accept(id uint64, next bool, now time.Time) (Request, error) {
	e.sweep(now)
	h := e.held(id)
	if h == nil {
		return Request{}, ErrRequestUnknown
	}
	switch {
	case h.request.State == Waiting:
	case h.request.State.Finished():
		return Request{}, ErrFinished
	default:
		return Request{}, ErrNotWaiting
	}
	entry, err := e.queue.Add(e.songOf(h.request))
	if err != nil {
		return Request{}, err
	}
	if next {
		e.queue.MoveTo(entry, 0)
	}
	h.request.State = Queued
	h.request.Entry = &entry
	return cloneRequest(h.request), nil
}

// settle applies the rule: it accepts every waiting request whose singer has no
// entry in the queue, or, in Arrival order, every waiting request.
//
// Oldest first, so the request that has waited longest takes the room there
// is. A full queue accepts nothing, and its requests wait until an entry
// leaves.
func (e *Evening) settle(now time.Time) {
	e.sweep(now)
	for i := range e.requests {
		r := &e.requests[i].request
		turnTaken := e.order == Turns && e.queue.HoldsSeat(r.Seat)
		if r.State != Waiting || turnTaken {
			continue
		}
		if e.queue.Len() >= MaxQueued {
			return
		}
		entry, err := e.queue.Add(e.songOf(*r))
		if err != nil {
			return
		}
		r.State = Queued
		r.Entry = &entry
	}
}

// songOf is the queue entry a request becomes, labelled with its seat's name.
func (e *Evening) songOf(r Request) Song {
	seat, _ := e.seats.Get(r.Seat)
	id := r.Seat
	return Song{Number: r.Number, Title: r.Title, Artist: r.Artist, Singer: seat.Name,
		DurationMS: r.DurationMS, Seat: &id}
}

// entryLeft records that a queue entry left, on the request it came from. A
// song with no singer has none.
func (e *Evening) entryLeft(entry uint64, state RequestState, now time.Time) {
	for _, h := range e.requests {
		if idIs(h.request.Entry, entry) && h.request.State == Queued {
			e.finish(h.request.ID, state, now)
			return
		}
	}
}

func (e *Evening) finish(id uint64, state RequestState, now time.Time) {
	if h := e.held(id); h != nil {
		h.request.State = state
		h.finishedAt = now
	}
}

func (e *Evening) held(id uint64) *held {
	for i := range e.requests {
		if e.requests[i].request.ID == id {
			return &e.requests[i]
		}
	}
	return nil
}

// sweep drops every finished request whose time has passed.
func (e *Evening) sweep(now time.Time) {
	kept := e.requests[:0]
	for _, h := range e.requests {
		if !expired(h, now) {
			kept = append(kept, h)
		}
	}
	e.requests = kept
}

func expired(h held, now time.Time) bool {
	return !h.finishedAt.IsZero() && now.Sub(h.finishedAt) >= FinishedFor
}

func distinct(what string, n int, id func(int) uint64) error {
	seen := make(map[uint64]bool, n)
	for i := range n {
		if seen[id(i)] {
			return ErrDuplicateID{What: what}
		}
		seen[id(i)] = true
	}
	return nil
}

func cloneRequest(r Request) Request {
	r.Entry = cloneID(r.Entry)
	return r
}

func cloneRequests(rs []Request) []Request {
	out := make([]Request, len(rs))
	for i, r := range rs {
		out[i] = cloneRequest(r)
	}
	return out
}

func cloneSung(ss []Sung) []Sung {
	out := make([]Sung, len(ss))
	for i, s := range ss {
		out[i] = Sung{Number: s.Number, Seat: cloneID(s.Seat)}
	}
	return out
}
