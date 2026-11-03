package fair

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// A singer is a seat that the evening issues, and the typed name is its label.
// A seat holds an id, an optional name and a token. The token proves that a
// caller holds the seat, because anybody can type a name.
//
// A seat is no account. It holds no password and it ends with the evening.

// MaxSeats is how many seats one evening may hold.
const MaxSeats = 1000

// MaxSeatNameChars is the longest name a seat may take, in characters.
const MaxSeatNameChars = 40

// MaxDeviceIDChars is the longest device id a remote may send, in characters.
const MaxDeviceIDChars = 64

// Seat errors.
var (
	ErrNameTaken      = errors.New("another singer holds that name")
	ErrNameRequired   = errors.New("this machine needs a name for every singer")
	ErrNameUnusable   = errors.New("that name cannot be used")
	ErrDeviceUnusable = errors.New("that device id cannot be used")
	ErrUnnamedTaken   = errors.New("this device already has a singer with no name")
	ErrDeviceFull     = errors.New("this device has as many singers as the machine allows")
	ErrSeatsFull      = fmt.Errorf("the machine has as many singers as it holds (%d)", MaxSeats)
	ErrSeatNotFound   = errors.New("no such singer")
	ErrWrongToken     = errors.New("that token does not belong to this singer")
)

// SeatName is a singer's name as typed and as compared.
type SeatName struct {
	// Shown is the name as typed, trimmed.
	Shown string
	// Key is what two names are compared by: no case, no surrounding spaces,
	// no accents.
	Key string
}

// NameOf turns a typed name into a SeatName. It reports false for a name of
// spaces alone, which is no name.
func NameOf(typed string) (SeatName, bool) {
	shown := strings.TrimSpace(typed)
	if shown == "" {
		return SeatName{}, false
	}
	return SeatName{Shown: shown, Key: Fold(shown)}, true
}

// Fold folds text to a comparison key: lower case, no accents, every run of
// characters that are not letters or digits as one space, trimmed.
func Fold(s string) string {
	stripped, _, err := transform.String(transform.Chain(
		norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err != nil {
		stripped = s
	}
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(stripped) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// Seat is one singer of the evening.
type Seat struct {
	// ID is opaque, stable for the evening and never reused.
	ID uint64 `json:"id"`
	// Name is the name people see, or empty when the singer gave none.
	Name string `json:"name,omitempty"`
	// Guest is the number of an unnamed seat, as in "Guest 4", or 0. A seat
	// that takes a name later keeps the number and shows the name.
	Guest uint32 `json:"guest,omitempty"`
	// Phone reports whether a phone holds the seat. It is false for a singer
	// an operator created and for a seat an operator opened.
	Phone bool `json:"phone"`
}

// SeatRules is what the owner's settings ask of a registration.
type SeatRules struct {
	// NameRequired makes every singer give a name.
	NameRequired bool
	// MaxPerDevice is how many singers one device may hold; 0 is no limit.
	MaxPerDevice uint32
}

// Issued is a seat with the token that now proves it.
type Issued struct {
	Seat  Seat
	Token string
}

// SeatRecord is a seat as the registry holds it, which is also what a store
// keeps of it.
type SeatRecord struct {
	Seat Seat `json:"seat"`
	// Key is the folded name, or empty for an unnamed seat.
	Key string `json:"key,omitempty"`
	// Device registered it; empty while no phone holds it.
	Device string `json:"device,omitempty"`
	// Token proves the seat; empty while no phone holds it.
	Token string `json:"token,omitempty"`
	// Open reports that an operator opened the seat, so the next registration
	// under its name takes it whatever it holds.
	Open bool `json:"open"`
}

// Seats are the evening's singers.
type Seats struct {
	held      []SeatRecord
	nextID    uint64
	nextGuest uint32
}

// NewSeatsFromRecords returns the seats a store kept, in the order each
// registered. Each counter ends past every value here, so no id and no guest
// number is issued twice.
func NewSeatsFromRecords(records []SeatRecord, nextID uint64, nextGuest uint32) *Seats {
	for _, r := range records {
		nextID = max(nextID, r.Seat.ID+1)
		nextGuest = max(nextGuest, r.Seat.Guest)
	}
	return &Seats{held: append([]SeatRecord(nil), records...), nextID: nextID, nextGuest: nextGuest}
}

// Records returns every seat with its token, device and key, for a store.
func (s *Seats) Records() []SeatRecord { return append([]SeatRecord(nil), s.held...) }

// NextID returns the id the next seat takes.
func (s *Seats) NextID() uint64 { return s.nextID }

// NextGuest returns the number the last unnamed singer took.
func (s *Seats) NextGuest() uint32 { return s.nextGuest }

// Len returns how many singers the evening holds.
func (s *Seats) Len() int { return len(s.held) }

// All returns every seat, in the order each registered.
func (s *Seats) All() []Seat {
	out := make([]Seat, len(s.held))
	for i, h := range s.held {
		out[i] = h.Seat
	}
	return out
}

// Get returns one seat by id.
func (s *Seats) Get(id uint64) (Seat, bool) {
	if i := s.indexOf(id); i >= 0 {
		return s.held[i].Seat, true
	}
	return Seat{}, false
}

// Holds reports whether a token proves a seat.
func (s *Seats) Holds(id uint64, token string) bool {
	i := s.indexOf(id)
	return i >= 0 && s.held[i].Token != "" && sameToken(s.held[i].Token, token)
}

// Register registers a singer for a device and returns the seat with its
// token.
//
// A held name comes back to whoever asks while its seat holds no song: that is
// the way back for a phone that lost its token. The seat gets token, its old
// token ends, and the seat moves to this device. While the seat holds a song
// the name is refused, because a reclaim by name alone would hand one singer's
// songs to another. A seat an operator opened comes back whatever it holds.
//
// holdsSong reports whether a seat has a song queued or waiting.
func (s *Seats) Register(device string, name *SeatName, token string, rules SeatRules,
	holdsSong func(uint64) bool) (Issued, error) {
	if err := checkDevice(device); err != nil {
		return Issued{}, err
	}
	if name == nil {
		if rules.NameRequired {
			return Issued{}, ErrNameRequired
		}
		// One unnamed singer for each device, so people who share a tablet
		// can tell their songs apart.
		for _, h := range s.held {
			if h.Device == device && h.Key == "" {
				return Issued{}, ErrUnnamedTaken
			}
		}
		if err := s.checkRoom(device, rules); err != nil {
			return Issued{}, err
		}
		s.nextGuest++
		return s.push(device, nil, s.nextGuest, token, token), nil
	}
	if err := checkName(*name); err != nil {
		return Issued{}, err
	}

	if i := s.indexOfKey(name.Key); i >= 0 {
		h := &s.held[i]
		if !h.Open && holdsSong(h.Seat.ID) {
			return Issued{}, ErrNameTaken
		}
		if h.Device != device {
			if err := s.checkDeviceRoom(device, rules); err != nil {
				return Issued{}, err
			}
		}
		h.Device = device
		h.Token = token
		h.Open = false
		h.Seat.Phone = true
		h.Seat.Name = name.Shown
		return Issued{Seat: h.Seat, Token: token}, nil
	}

	if err := s.checkRoom(device, rules); err != nil {
		return Issued{}, err
	}
	return s.push(device, name, 0, token, token), nil
}

// Create creates a singer who has no phone, for a caller whose level is the
// check. The seat has no token. A name must be free, and a singer with none
// gets a guest number.
func (s *Seats) Create(name *SeatName) (Seat, error) {
	if name != nil {
		if err := checkName(*name); err != nil {
			return Seat{}, err
		}
		if s.indexOfKey(name.Key) >= 0 {
			return Seat{}, ErrNameTaken
		}
	}
	if len(s.held) >= MaxSeats {
		return Seat{}, ErrSeatsFull
	}
	var guest uint32
	if name == nil {
		s.nextGuest++
		guest = s.nextGuest
	}
	return s.push("", name, guest, "", "").Seat, nil
}

// Open opens a seat for another phone to take, whatever it holds, and ends its
// token. The next registration under the seat's name takes it. A seat with no
// name has no name to give, so it is refused.
func (s *Seats) Open(id uint64) (Seat, error) {
	i := s.indexOf(id)
	if i < 0 {
		return Seat{}, ErrSeatNotFound
	}
	h := &s.held[i]
	if h.Key == "" {
		return Seat{}, ErrNameRequired
	}
	h.Token = ""
	h.Device = ""
	h.Open = true
	h.Seat.Phone = false
	return h.Seat, nil
}

// Remove takes a seat out of the evening and returns it.
func (s *Seats) Remove(id uint64) (Seat, bool) {
	i := s.indexOf(id)
	if i < 0 {
		return Seat{}, false
	}
	seat := s.held[i].Seat
	s.held = append(s.held[:i], s.held[i+1:]...)
	return seat, true
}

// Clear ends every seat, for a new evening. Ids go on counting; the guest
// numbers start again.
func (s *Seats) Clear() {
	s.held = nil
	s.nextGuest = 0
}

// Rename gives a seat a name, or another name. The token is the proof. The name
// must be free; a rename never takes a name from another seat.
func (s *Seats) Rename(id uint64, token string, name SeatName) (Seat, error) {
	if err := checkName(name); err != nil {
		return Seat{}, err
	}
	i := s.indexOf(id)
	if i < 0 {
		return Seat{}, ErrSeatNotFound
	}
	if !s.Holds(id, token) {
		return Seat{}, ErrWrongToken
	}
	if other := s.indexOfKey(name.Key); other >= 0 && other != i {
		return Seat{}, ErrNameTaken
	}
	h := &s.held[i]
	h.Key = name.Key
	h.Seat.Name = name.Shown
	return h.Seat, nil
}

// push adds a seat. issued is what the answer carries, empty for a seat with
// no token.
func (s *Seats) push(device string, name *SeatName, guest uint32, token, issued string) Issued {
	id := s.nextID
	s.nextID++
	seat := Seat{ID: id, Guest: guest, Phone: device != ""}
	var key string
	if name != nil {
		seat.Name = name.Shown
		key = name.Key
	}
	s.held = append(s.held, SeatRecord{Seat: seat, Key: key, Device: device, Token: token})
	return Issued{Seat: seat, Token: issued}
}

func (s *Seats) indexOf(id uint64) int {
	for i, h := range s.held {
		if h.Seat.ID == id {
			return i
		}
	}
	return -1
}

func (s *Seats) indexOfKey(key string) int {
	for i, h := range s.held {
		if h.Key != "" && h.Key == key {
			return i
		}
	}
	return -1
}

// checkRoom reports whether one more seat fits, for the evening and the device.
func (s *Seats) checkRoom(device string, rules SeatRules) error {
	if len(s.held) >= MaxSeats {
		return ErrSeatsFull
	}
	return s.checkDeviceRoom(device, rules)
}

func (s *Seats) checkDeviceRoom(device string, rules SeatRules) error {
	if rules.MaxPerDevice == 0 {
		return nil
	}
	n := 0
	for _, h := range s.held {
		if h.Device == device {
			n++
		}
	}
	if n >= int(rules.MaxPerDevice) {
		return ErrDeviceFull
	}
	return nil
}

func checkDevice(device string) error {
	if device == "" || utf8.RuneCountInString(device) > MaxDeviceIDChars {
		return ErrDeviceUnusable
	}
	return nil
}

func checkName(name SeatName) error {
	if name.Key == "" || utf8.RuneCountInString(name.Shown) > MaxSeatNameChars {
		return ErrNameUnusable
	}
	return nil
}

// sameToken compares two tokens in time that depends on their lengths alone.
func sameToken(held, offered string) bool {
	return subtle.ConstantTimeCompare([]byte(held), []byte(offered)) == 1
}
