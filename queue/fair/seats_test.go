package fair

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	phone  = "phone-a"
	tablet = "tablet-b"
)

func seatName(shown string) *SeatName {
	return &SeatName{Shown: shown, Key: strings.ToLower(shown)}
}

func free(uint64) bool { return false }

func registerSeat(t *testing.T, s *Seats, device string, name *SeatName, token string) Issued {
	t.Helper()
	issued, err := s.Register(device, name, token, SeatRules{}, free)
	require.NoError(t, err)
	return issued
}

func TestSeats(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"a_registered_singer_gets_a_seat_and_a_token_that_proves_it", func(t *testing.T) {
			s := &Seats{}
			ana := registerSeat(t, s, phone, seatName("Ana"), "t1")
			assert.Equal(t, "Ana", ana.Seat.Name)
			assert.Zero(t, ana.Seat.Guest)
			assert.True(t, s.Holds(ana.Seat.ID, "t1"))
			assert.False(t, s.Holds(ana.Seat.ID, "t2"))
			assert.False(t, s.Holds(ana.Seat.ID, ""))
			assert.Equal(t, 1, s.Len())
		}},
		{"a_singer_needs_no_name_and_gets_a_number_instead", func(t *testing.T) {
			s := &Seats{}
			first := registerSeat(t, s, phone, nil, "t1")
			second := registerSeat(t, s, tablet, nil, "t2")
			assert.Empty(t, first.Seat.Name)
			assert.Equal(t, uint32(1), first.Seat.Guest)
			assert.Equal(t, uint32(2), second.Seat.Guest)
			assert.NotEqual(t, first.Seat.ID, second.Seat.ID)
		}},
		{"one_device_holds_one_unnamed_singer_at_most", func(t *testing.T) {
			s := &Seats{}
			registerSeat(t, s, phone, nil, "t1")
			_, err := s.Register(phone, nil, "t2", SeatRules{}, free)
			assert.ErrorIs(t, err, ErrUnnamedTaken)
			registerSeat(t, s, phone, seatName("Beto"), "t3")
			assert.Equal(t, 2, s.Len())
		}},
		{"a_name_is_held_by_one_seat_however_it_is_typed", func(t *testing.T) {
			s := &Seats{}
			ana := registerSeat(t, s, phone, seatName("Ana"), "t1")
			_, err := s.Register(tablet, &SeatName{Shown: "ANA", Key: "ana"}, "t2", SeatRules{},
				func(id uint64) bool { return id == ana.Seat.ID })
			assert.ErrorIs(t, err, ErrNameTaken)
			assert.True(t, s.Holds(ana.Seat.ID, "t1"), "the first token still holds")
		}},
		{"a_free_seat_comes_back_by_its_name_with_a_new_token", func(t *testing.T) {
			s := &Seats{}
			ana := registerSeat(t, s, phone, seatName("Ana"), "t1")
			back := registerSeat(t, s, tablet, seatName("ana"), "t2")
			assert.Equal(t, ana.Seat.ID, back.Seat.ID, "the same seat")
			assert.Equal(t, "ana", back.Seat.Name)
			assert.True(t, s.Holds(ana.Seat.ID, "t2"))
			assert.False(t, s.Holds(ana.Seat.ID, "t1"), "the old token ends")
			assert.Equal(t, 1, s.Len())
		}},
		{"a_rename_keeps_the_seat_and_needs_its_token", func(t *testing.T) {
			s := &Seats{}
			guest := registerSeat(t, s, phone, nil, "t1")
			registerSeat(t, s, tablet, seatName("Ana"), "t2")
			id := guest.Seat.ID

			_, err := s.Rename(id, "t2", *seatName("Beto"))
			assert.ErrorIs(t, err, ErrWrongToken)
			_, err = s.Rename(id, "t1", *seatName("Ana"))
			assert.ErrorIs(t, err, ErrNameTaken, "even a free seat's name")

			named, err := s.Rename(id, "t1", *seatName("Beto"))
			require.NoError(t, err)
			assert.Equal(t, id, named.ID)
			assert.Equal(t, "Beto", named.Name)
			assert.True(t, s.Holds(id, "t1"), "the token stays")

			again, err := s.Rename(id, "t1", *seatName("BETO"))
			require.NoError(t, err, "its own name")
			assert.Equal(t, "BETO", again.Name)

			registerSeat(t, s, phone, nil, "t3")
		}},
		{"an_opened_seat_goes_to_the_next_phone_that_gives_its_name", func(t *testing.T) {
			s := &Seats{}
			ana := registerSeat(t, s, phone, seatName("Ana"), "t1")
			holding := func(id uint64) bool { return id == ana.Seat.ID }

			opened, err := s.Open(ana.Seat.ID)
			require.NoError(t, err)
			assert.False(t, opened.Phone, "no phone holds it now")
			assert.False(t, s.Holds(ana.Seat.ID, "t1"), "the old token ends")

			taken, err := s.Register(tablet, seatName("ana"), "t2", SeatRules{}, holding)
			require.NoError(t, err, "an opened seat comes back whatever it holds")
			assert.Equal(t, ana.Seat.ID, taken.Seat.ID)
			assert.True(t, taken.Seat.Phone)
			assert.True(t, s.Holds(ana.Seat.ID, "t2"))

			_, err = s.Register("phone-c", seatName("Ana"), "t3", SeatRules{}, holding)
			assert.ErrorIs(t, err, ErrNameTaken)
			_, err = s.Open(9999)
			assert.ErrorIs(t, err, ErrSeatNotFound)
		}},
		{"a_seat_with_no_name_cannot_be_opened", func(t *testing.T) {
			s := &Seats{}
			guest := registerSeat(t, s, phone, nil, "t1")
			_, err := s.Open(guest.Seat.ID)
			assert.ErrorIs(t, err, ErrNameRequired)
			assert.True(t, s.Holds(guest.Seat.ID, "t1"), "and it keeps its token")
		}},
		{"an_operator_creates_a_singer_with_no_phone", func(t *testing.T) {
			s := &Seats{}
			hosted, err := s.Create(seatName("Ana"))
			require.NoError(t, err)
			assert.False(t, hosted.Phone)
			assert.False(t, s.Holds(hosted.ID, ""), "no token proves it")
			_, err = s.Create(seatName("ana"))
			assert.ErrorIs(t, err, ErrNameTaken)

			unnamed, err := s.Create(nil)
			require.NoError(t, err)
			assert.Equal(t, uint32(1), unnamed.Guest)
			second, err := s.Create(nil)
			require.NoError(t, err, "and another, because no device holds them")
			assert.Equal(t, uint32(2), second.Guest)

			taken := registerSeat(t, s, phone, seatName("Ana"), "t1")
			assert.Equal(t, hosted.ID, taken.Seat.ID)
			assert.True(t, taken.Seat.Phone)
		}},
		{"a_removed_seat_frees_its_name_and_a_new_evening_ends_every_seat", func(t *testing.T) {
			s := &Seats{}
			ana := registerSeat(t, s, phone, seatName("Ana"), "t1")
			registerSeat(t, s, tablet, nil, "t2")

			gone, ok := s.Remove(ana.Seat.ID)
			require.True(t, ok)
			assert.Equal(t, ana.Seat.ID, gone.ID)
			_, ok = s.Remove(ana.Seat.ID)
			assert.False(t, ok)
			again := registerSeat(t, s, phone, seatName("Ana"), "t3")
			assert.NotEqual(t, ana.Seat.ID, again.Seat.ID, "ids are never reused")

			s.Clear()
			assert.Zero(t, s.Len())
			first := registerSeat(t, s, phone, nil, "t4")
			assert.Equal(t, uint32(1), first.Seat.Guest, "the numbers start again")
			assert.Greater(t, first.Seat.ID, again.Seat.ID, "and the ids go on counting")
		}},
		{"the_owner_may_require_a_name", func(t *testing.T) {
			s := &Seats{}
			rules := SeatRules{NameRequired: true}
			_, err := s.Register(phone, nil, "t1", rules, free)
			assert.ErrorIs(t, err, ErrNameRequired)
			_, err = s.Register(phone, seatName("Ana"), "t1", rules, free)
			assert.NoError(t, err)
		}},
		{"the_owner_may_cap_the_singers_on_one_device", func(t *testing.T) {
			s := &Seats{}
			rules := SeatRules{MaxPerDevice: 2}
			for i, n := range []string{"Ana", "Beto"} {
				_, err := s.Register(phone, seatName(n), fmt.Sprintf("t%d", i+1), rules, free)
				require.NoError(t, err, "under the cap")
			}
			_, err := s.Register(phone, seatName("Caio"), "t3", rules, free)
			assert.ErrorIs(t, err, ErrDeviceFull)
			_, err = s.Register(tablet, seatName("Caio"), "t3", rules, free)
			assert.NoError(t, err, "another device has its own count")
			_, err = s.Register(phone, seatName("Caio"), "t4", rules, free)
			assert.ErrorIs(t, err, ErrDeviceFull, "a full device cannot take a free seat's name")
			_, err = s.Register(phone, seatName("Ana"), "t5", rules, free)
			assert.NoError(t, err, "it can ask again for a name it holds")
		}},
		{"the_evening_is_capped", func(t *testing.T) {
			s := &Seats{}
			for n := range MaxSeats {
				registerSeat(t, s, fmt.Sprintf("device-%d", n), nil, "t")
			}
			_, err := s.Register("one-more", nil, "t", SeatRules{}, free)
			assert.ErrorIs(t, err, ErrSeatsFull)
		}},
		{"a_name_or_a_device_that_cannot_be_used_is_refused", func(t *testing.T) {
			s := &Seats{}
			cases := []struct {
				device string
				name   *SeatName
				want   error
			}{
				{phone, &SeatName{Shown: "!!!", Key: ""}, ErrNameUnusable},
				{phone, seatName(strings.Repeat("a", MaxSeatNameChars+1)), ErrNameUnusable},
				{"", seatName("Ana"), ErrDeviceUnusable},
				{strings.Repeat("d", MaxDeviceIDChars+1), seatName("Ana"), ErrDeviceUnusable},
			}
			for _, c := range cases {
				_, err := s.Register(c.device, c.name, "t", SeatRules{}, free)
				assert.ErrorIs(t, err, c.want)
			}
			assert.Zero(t, s.Len())
		}},
		{"ids_are_never_reused_and_seats_list_in_order", func(t *testing.T) {
			s := &Seats{}
			ana := registerSeat(t, s, phone, seatName("Ana"), "t1")
			beto := registerSeat(t, s, tablet, seatName("Beto"), "t2")
			assert.Greater(t, beto.Seat.ID, ana.Seat.ID)
			var names []string
			for _, seat := range s.All() {
				names = append(names, seat.Name)
			}
			assert.Equal(t, []string{"Ana", "Beto"}, names)
			got, ok := s.Get(beto.Seat.ID)
			assert.True(t, ok)
			assert.Equal(t, beto.Seat, got)
			_, ok = s.Get(9999)
			assert.False(t, ok)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestNameOf(t *testing.T) {
	tests := []struct {
		typed string
		want  SeatName
		ok    bool
	}{
		{"Ana", SeatName{"Ana", "ana"}, true},
		{"  Aná  ", SeatName{"Aná", "ana"}, true},
		{"João  da Silva!", SeatName{"João  da Silva!", "joao da silva"}, true},
		{"   ", SeatName{}, false},
		{"!!!", SeatName{"!!!", ""}, true},
	}
	for _, tt := range tests {
		got, ok := NameOf(tt.typed)
		assert.Equal(t, tt.ok, ok, tt.typed)
		assert.Equal(t, tt.want, got, tt.typed)
	}
}
