package fair

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// singer is a phone with one singer on it.
type singer struct {
	seat  uint64
	token string
}

func newSinger(t *testing.T, e *Evening, name string) singer {
	t.Helper()
	issued, err := e.Register("device-"+name, &SeatName{Shown: name, Key: strings.ToLower(name)},
		"token-"+name, SeatRules{})
	require.NoError(t, err)
	return singer{seat: issued.Seat.ID, token: issued.Token}
}

func requested(number uint32) RequestedSong {
	return RequestedSong{Number: number, Title: fmt.Sprintf("Song %d", number), DurationMS: 180_000}
}

func ask(t *testing.T, e *Evening, who singer, number uint32, now time.Time) Request {
	t.Helper()
	r, err := e.Request(who.seat, ByToken(who.token), requested(number), DefaultRequestRules, now)
	require.NoError(t, err)
	return r
}

// slip is a hand-written slip, entered by somebody at the control level.
func slip(t *testing.T, e *Evening, who singer, number uint32, now time.Time) Request {
	t.Helper()
	r, err := e.Request(who.seat, ByLevel, requested(number), DefaultRequestRules, now)
	require.NoError(t, err)
	return r
}

func numbers(e *Evening) []uint32 { return queueNumbers(e.Queue()) }

func stateOf(t *testing.T, e *Evening, id uint64, now time.Time) RequestState {
	t.Helper()
	for _, r := range e.Requests(now) {
		if r.ID == id {
			return r.State
		}
	}
	require.Failf(t, "request not listed", "request %d", id)
	return ""
}

func generic(number uint32) Song { return Song{Number: number, Title: "Generic"} }

func addGeneric(t *testing.T, e *Evening, number uint32) {
	t.Helper()
	_, err := e.Add(generic(number))
	require.NoError(t, err)
}

func restoreTurns(s Snapshot, now time.Time) (*Evening, error) { return Restore(s, Turns, now) }

// aBusyEvening has two singers, one song sung, two queued and one waiting.
func aBusyEvening(t *testing.T, now time.Time) (*Evening, singer, singer) {
	e := NewEvening(Turns)
	ana := newSinger(t, e, "Ana")
	beto := newSinger(t, e, "Beto")
	ask(t, e, ana, 11, now)
	ask(t, e, beto, 21, now)
	ask(t, e, ana, 12, now)
	ask(t, e, ana, 13, now)
	e.Pop(now)
	e.SetAccepting(false)
	return e, ana, beto
}

func TestEvening(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"arrival_order_queues_every_song_in_the_order_asked", func(t *testing.T) {
			e := NewEvening("")
			assert.Equal(t, Arrival, e.Order(), "the default")
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			for _, n := range []uint32{1, 2, 3} {
				assert.Equal(t, Queued, ask(t, e, ana, n, now).State)
			}
			ask(t, e, beto, 4, now)
			for n := uint32(5); n <= 14; n++ {
				ask(t, e, ana, n, now)
			}
			assert.Equal(t, []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}, numbers(e),
				"no turns: ten of one singer's songs stand in a row")
		}},
		{"arrival_order_takes_an_unnamed_singer_the_same_way", func(t *testing.T) {
			e := NewEvening(Arrival)
			issued, err := e.Register("device-x", nil, "token-x", SeatRules{})
			require.NoError(t, err)
			guest := singer{seat: issued.Seat.ID, token: "token-x"}
			ask(t, e, guest, 1, now)
			ask(t, e, guest, 2, now)
			assert.Equal(t, []uint32{1, 2}, numbers(e))
		}},
		{"a_change_to_arrival_order_empties_the_waiting_list_into_the_queue", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			ask(t, e, ana, 1, now)
			ask(t, e, ana, 2, now)
			ask(t, e, beto, 3, now)
			ask(t, e, ana, 4, now)
			assert.Equal(t, []uint32{1, 3}, numbers(e), "turns keep 2 and 4 waiting")
			e.SetOrder(Arrival, now)
			assert.Equal(t, []uint32{1, 3, 2, 4}, numbers(e))
		}},
		{"a_restart_applies_the_order_it_is_given", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 1, now)
			ask(t, e, ana, 2, now)
			snap := e.Snapshot(nil, now)
			turns, err := Restore(snap, Turns, now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{1}, numbers(turns))
			arrival, err := Restore(snap, Arrival, now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{1, 2}, numbers(arrival))
		}},
		{"a_first_request_goes_straight_into_the_queue", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			r := ask(t, e, ana, 1, now)
			assert.Equal(t, Queued, r.State)
			entry, ok := e.Queue().Peek()
			require.True(t, ok)
			require.NotNil(t, r.Entry)
			assert.Equal(t, entry.ID, *r.Entry)
			assert.Equal(t, seatPtr(ana.seat), entry.Seat)
			assert.Equal(t, "Ana", entry.Singer)
		}},
		{"a_second_request_waits_while_the_first_is_queued", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 1, now)
			second := ask(t, e, ana, 2, now)
			assert.Equal(t, Waiting, second.State)
			assert.Nil(t, second.Entry)
			assert.Equal(t, []uint32{1}, numbers(e))
		}},
		{"the_singers_take_turns", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			caio := newSinger(t, e, "Caio")
			for _, n := range []uint32{11, 12, 13} {
				ask(t, e, ana, n, now)
			}
			for _, n := range []uint32{21, 22, 23} {
				ask(t, e, beto, n, now)
			}
			for _, n := range []uint32{31, 32, 33} {
				ask(t, e, caio, n, now)
			}
			assert.Equal(t, []uint32{11, 21, 31}, numbers(e), "one round")
			var sung []uint32
			for {
				entry, ok := e.Pop(now)
				if !ok {
					break
				}
				sung = append(sung, entry.Number)
			}
			assert.Equal(t, []uint32{11, 21, 31, 12, 22, 32, 13, 23, 33}, sung)
		}},
		{"the_next_request_goes_in_when_the_song_starts_and_no_entry_moves_back", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			first := ask(t, e, ana, 11, now)
			next := ask(t, e, ana, 12, now)
			ask(t, e, beto, 21, now)
			assert.Equal(t, []uint32{11, 21}, numbers(e))

			started, ok := e.Pop(now)
			require.True(t, ok)
			assert.Equal(t, uint32(11), started.Number)
			assert.Equal(t, []uint32{21, 12}, numbers(e), "behind the song already there")
			assert.Equal(t, Played, stateOf(t, e, first.ID, now))
			assert.Equal(t, Queued, stateOf(t, e, next.ID, now))
		}},
		{"a_new_singer_waits_one_round_at_most", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			for _, n := range []uint32{11, 12, 13} {
				ask(t, e, ana, n, now)
			}
			for _, n := range []uint32{21, 22, 23} {
				ask(t, e, beto, n, now)
			}
			e.Pop(now)
			dani := newSinger(t, e, "Dani")
			ask(t, e, dani, 41, now)
			assert.Equal(t, []uint32{21, 12, 41}, numbers(e))
		}},
		{"a_song_with_no_singer_passes_the_rule", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 11, now)
			addGeneric(t, e, 901)
			addGeneric(t, e, 902)
			assert.Equal(t, []uint32{11, 901, 902}, numbers(e))
			assert.Len(t, e.Requests(now), 1, "and it is no request")
		}},
		{"withdrawing_a_waiting_request_needs_the_seats_token", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			ask(t, e, ana, 11, now)
			waiting := ask(t, e, ana, 12, now)

			_, err := e.Withdraw(waiting.ID, beto.token, now)
			assert.ErrorIs(t, err, ErrWrongToken)
			withdrawn, err := e.Withdraw(waiting.ID, ana.token, now)
			require.NoError(t, err)
			assert.Equal(t, Withdrawn, withdrawn.State)
			assert.Equal(t, []uint32{11}, numbers(e))

			_, err = e.Withdraw(waiting.ID, ana.token, now)
			assert.ErrorIs(t, err, ErrFinished)
			_, err = e.Withdraw(9999, ana.token, now)
			assert.ErrorIs(t, err, ErrRequestUnknown)
		}},
		{"withdrawing_a_queued_song_lets_the_next_one_in", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			wrong := ask(t, e, ana, 11, now)
			ask(t, e, beto, 21, now)
			ask(t, e, ana, 12, now)
			_, err := e.Withdraw(wrong.ID, ana.token, now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{21, 12}, numbers(e))
			assert.Equal(t, Withdrawn, stateOf(t, e, wrong.ID, now))
		}},
		{"an_entry_somebody_removes_reads_declined_and_the_rule_runs", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			first := ask(t, e, ana, 11, now)
			second := ask(t, e, ana, 12, now)
			_, ok := e.Remove(*first.Entry, now)
			require.True(t, ok)
			assert.Equal(t, Declined, stateOf(t, e, first.ID, now))
			assert.Equal(t, Queued, stateOf(t, e, second.ID, now))
			assert.Equal(t, []uint32{12}, numbers(e))
		}},
		{"clearing_the_queue_lets_each_singers_next_request_in", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			ask(t, e, ana, 11, now)
			ask(t, e, ana, 12, now)
			ask(t, e, beto, 21, now)
			assert.Len(t, e.Clear(now), 2)
			assert.Equal(t, []uint32{12}, numbers(e), "Beto has nothing else waiting")
		}},
		{"the_rule_never_moves_an_entry_somebody_moved", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			ask(t, e, ana, 11, now)
			moved := ask(t, e, beto, 21, now)
			ask(t, e, beto, 22, now)
			assert.True(t, e.MoveTo(*moved.Entry, 0))
			assert.Equal(t, []uint32{21, 11}, numbers(e))
			e.Pop(now)
			assert.Equal(t, []uint32{11, 22}, numbers(e))
		}},
		{"a_singer_may_have_only_so_many_requests_waiting", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			rules := RequestRules{MaxWaiting: 2}
			for _, n := range []uint32{11, 12, 13} {
				_, err := e.Request(ana.seat, ByToken(ana.token), requested(n), rules, now)
				require.NoError(t, err, "under the cap")
			}
			_, err := e.Request(ana.seat, ByToken(ana.token), requested(14), rules, now)
			assert.ErrorIs(t, err, ErrSingerFull)
		}},
		{"a_cap_of_no_waiting_songs_still_takes_the_song_that_goes_straight_in", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			rules := RequestRules{MaxWaiting: 0}
			first, err := e.Request(ana.seat, ByToken(ana.token), requested(11), rules, now)
			require.NoError(t, err, "nothing of hers is queued, so it goes in")
			assert.Equal(t, Queued, first.State)
			_, err = e.Request(ana.seat, ByToken(ana.token), requested(12), rules, now)
			assert.ErrorIs(t, err, ErrSingerFull, "the second would wait")

			e.Pop(now)
			next, err := e.Request(ana.seat, ByToken(ana.token), requested(12), rules, now)
			require.NoError(t, err, "the queue holds none of hers")
			assert.Equal(t, Queued, next.State)
		}},
		{"the_whole_list_is_capped", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			rules := RequestRules{MaxWaiting: ^uint32(0)}
			for n := range MaxRequests {
				_, err := e.Request(ana.seat, ByToken(ana.token), requested(uint32(n)+1), rules, now)
				require.NoError(t, err, "under the cap")
			}
			_, err := e.Request(ana.seat, ByToken(ana.token), requested(9999), rules, now)
			assert.ErrorIs(t, err, ErrRequestsFull)
		}},
		{"a_full_queue_holds_every_request_until_an_entry_leaves", func(t *testing.T) {
			e := NewEvening(Turns)
			for n := range MaxQueued {
				addGeneric(t, e, uint32(n)+1)
			}
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			first := ask(t, e, ana, 5001, now)
			second := ask(t, e, beto, 5002, now)
			assert.Equal(t, Waiting, first.State)
			assert.Equal(t, Waiting, second.State)

			e.Pop(now)
			assert.Equal(t, Queued, stateOf(t, e, first.ID, now))
			assert.Equal(t, Waiting, stateOf(t, e, second.ID, now))
		}},
		{"a_request_needs_a_seat_and_its_token_or_a_callers_level", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			_, err := e.Request(9999, ByLevel, requested(1), DefaultRequestRules, now)
			assert.ErrorIs(t, err, ErrNoSinger)
			_, err = e.Request(ana.seat, ByToken("nope"), requested(1), DefaultRequestRules, now)
			assert.ErrorIs(t, err, ErrWrongToken)
			first := slip(t, e, ana, 1, now)
			second := slip(t, e, ana, 2, now)
			assert.Equal(t, Queued, first.State)
			assert.Equal(t, Waiting, second.State)
		}},
		{"a_finished_request_leaves_the_list_after_a_fixed_time", func(t *testing.T) {
			start := now
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			played := ask(t, e, ana, 11, start)
			queued := ask(t, e, ana, 12, start)
			e.Pop(start)

			justBefore := start.Add(FinishedFor - time.Second)
			assert.Equal(t, Played, stateOf(t, e, played.ID, justBefore))

			var listed []uint64
			for _, r := range e.Requests(start.Add(FinishedFor)) {
				listed = append(listed, r.ID)
			}
			assert.Equal(t, []uint64{queued.ID}, listed, "the one still in the queue stays")
		}},
		{"a_name_is_held_while_its_seat_has_a_request", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			r := ask(t, e, ana, 11, now)
			reclaim := func() error {
				_, err := e.Register("another-device", &SeatName{Shown: "Ana", Key: "ana"}, "token-new", SeatRules{})
				return err
			}
			assert.ErrorIs(t, reclaim(), ErrNameTaken)
			_, err := e.Withdraw(r.ID, ana.token, now)
			require.NoError(t, err)
			assert.NoError(t, reclaim(), "nothing queued and nothing waiting")
		}},
		{"a_rename_reaches_the_seats_queued_songs", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 11, now)
			_, err := e.Rename(ana.seat, ana.token, SeatName{Shown: "Ana Paula", Key: "ana paula"})
			require.NoError(t, err)
			entry, _ := e.Queue().Peek()
			assert.Equal(t, "Ana Paula", entry.Singer)
		}},
		{"a_declined_request_never_plays_and_the_next_one_takes_the_turn", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			queued := ask(t, e, ana, 11, now)
			unwelcome := ask(t, e, ana, 12, now)
			third := ask(t, e, ana, 13, now)

			declined, err := e.Decline(unwelcome.ID, now)
			require.NoError(t, err)
			assert.Equal(t, Declined, declined.State)
			assert.Equal(t, []uint32{11}, numbers(e), "nothing in the queue moved")

			_, err = e.Decline(queued.ID, now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{13}, numbers(e))
			assert.Equal(t, Queued, stateOf(t, e, third.ID, now))

			_, err = e.Decline(queued.ID, now)
			assert.ErrorIs(t, err, ErrFinished)
			_, err = e.Decline(9999, now)
			assert.ErrorIs(t, err, ErrRequestUnknown)
		}},
		{"accepting_early_gives_a_second_song_and_the_rule_adds_no_third", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			first := ask(t, e, ana, 11, now)
			ask(t, e, beto, 21, now)
			second := ask(t, e, ana, 12, now)
			third := ask(t, e, ana, 13, now)

			accepted, err := e.Accept(second.ID, false, now)
			require.NoError(t, err)
			assert.Equal(t, Queued, accepted.State)
			assert.Equal(t, []uint32{11, 21, 12}, numbers(e), "at the end")

			next, err := e.Accept(third.ID, true, now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{13, 11, 21, 12}, numbers(e), "next")
			front, _ := e.Queue().Peek()
			assert.Equal(t, front.ID, *next.Entry)

			_, err = e.Accept(first.ID, false, now)
			assert.ErrorIs(t, err, ErrNotWaiting, "a queued request has nothing to accept")

			fourth := ask(t, e, ana, 14, now)
			e.Pop(now)
			assert.Equal(t, Waiting, stateOf(t, e, fourth.ID, now))
		}},
		{"a_full_queue_refuses_an_early_accept", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 5001, now)
			waiting := ask(t, e, ana, 5002, now)
			for n := range MaxQueued - 1 {
				addGeneric(t, e, uint32(n)+1)
			}
			_, err := e.Accept(waiting.ID, false, now)
			assert.ErrorIs(t, err, ErrQueueFull)
			assert.Equal(t, Waiting, stateOf(t, e, waiting.ID, now))
		}},
		{"a_closed_evening_refuses_a_phone_and_keeps_what_waits", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 11, now)
			waiting := ask(t, e, ana, 12, now)
			assert.True(t, e.Accepting())

			e.SetAccepting(false)
			_, err := e.Request(ana.seat, ByToken(ana.token), requested(13), DefaultRequestRules, now)
			assert.ErrorIs(t, err, ErrClosed)

			e.Pop(now)
			assert.Equal(t, Queued, stateOf(t, e, waiting.ID, now))
			assert.Equal(t, Waiting, slip(t, e, ana, 14, now).State, "the host's song is taken")

			e.SetAccepting(true)
			ask(t, e, ana, 15, now)
		}},
		{"removing_a_singer_takes_their_songs_and_frees_their_name", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			queued := ask(t, e, ana, 11, now)
			waiting := ask(t, e, ana, 12, now)
			ask(t, e, beto, 21, now)

			gone, err := e.RemoveSinger(ana.seat, now)
			require.NoError(t, err)
			assert.Equal(t, ana.seat, gone.ID)
			assert.Equal(t, []uint32{21}, numbers(e))
			assert.Equal(t, Declined, stateOf(t, e, queued.ID, now))
			assert.Equal(t, Declined, stateOf(t, e, waiting.ID, now))
			_, ok := e.Seats().Get(ana.seat)
			assert.False(t, ok)
			_, err = e.RemoveSinger(ana.seat, now)
			assert.ErrorIs(t, err, ErrSeatNotFound)
			newSinger(t, e, "Ana")
		}},
		{"merging_two_singers_moves_everything_to_one", func(t *testing.T) {
			e := NewEvening(Turns)
			dave := newSinger(t, e, "Dave")
			david := newSinger(t, e, "David")
			ask(t, e, dave, 11, now)
			ask(t, e, david, 21, now)
			waiting := ask(t, e, dave, 12, now)

			kept, err := e.Merge(dave.seat, david.seat)
			require.NoError(t, err)
			assert.Equal(t, david.seat, kept.ID)
			_, ok := e.Seats().Get(dave.seat)
			assert.False(t, ok)
			for _, entry := range e.Queue().Entries() {
				assert.Equal(t, seatPtr(david.seat), entry.Seat)
				assert.Equal(t, "David", entry.Singer)
			}
			assert.Equal(t, 2, e.Queue().Len())

			e.Pop(now)
			assert.Equal(t, Waiting, stateOf(t, e, waiting.ID, now), "one of his is still in the queue")
			e.Pop(now)
			assert.Equal(t, Queued, stateOf(t, e, waiting.ID, now))
			assert.Equal(t, []Sung{{11, seatPtr(david.seat)}, {21, seatPtr(david.seat)}}, e.Sung())

			_, err = e.Merge(david.seat, david.seat)
			assert.ErrorIs(t, err, ErrSeatNotFound)
			_, err = e.Merge(9999, david.seat)
			assert.ErrorIs(t, err, ErrSeatNotFound)
		}},
		{"the_record_holds_every_song_that_started", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 11, now)
			addGeneric(t, e, 901)
			assert.Empty(t, e.Sung())
			e.Pop(now)
			e.Pop(now)
			assert.Equal(t, []Sung{{11, seatPtr(ana.seat)}, {901, nil}}, e.Sung())
		}},
		{"a_new_evening_clears_everything_and_ids_go_on_counting", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			before := ask(t, e, ana, 11, now)
			ask(t, e, ana, 12, now)
			e.Pop(now)
			e.SetAccepting(false)

			e.Reset()

			assert.Zero(t, e.Queue().Len())
			assert.Zero(t, e.Seats().Len())
			assert.Empty(t, e.Requests(now))
			assert.Empty(t, e.Sung())
			assert.True(t, e.Accepting())

			again := newSinger(t, e, "Ana")
			assert.Greater(t, again.seat, ana.seat, "a kept seat id names nobody now")
			after := ask(t, e, again, 11, now)
			assert.Greater(t, after.ID, before.ID)
		}},
		{"an_operators_singer_takes_turns_with_no_phone", func(t *testing.T) {
			e := NewEvening(Turns)
			hosted, err := e.CreateSinger(&SeatName{Shown: "Caio", Key: "caio"})
			require.NoError(t, err)
			caio := singer{seat: hosted.ID}
			assert.Equal(t, Queued, slip(t, e, caio, 31, now).State)
			assert.Equal(t, Waiting, slip(t, e, caio, 32, now).State)
			_, err = e.Request(caio.seat, ByToken(""), requested(33), DefaultRequestRules, now)
			assert.ErrorIs(t, err, ErrWrongToken)

			phoneTakes := func() (Issued, error) {
				return e.Register("phone-c", &SeatName{Shown: "Caio", Key: "caio"}, "token-c", SeatRules{})
			}
			_, err = phoneTakes()
			assert.ErrorIs(t, err, ErrNameTaken)
			_, err = e.OpenSeat(caio.seat)
			require.NoError(t, err)
			taken, err := phoneTakes()
			require.NoError(t, err, "an opened seat")
			assert.Equal(t, caio.seat, taken.Seat.ID, "with his songs")
		}},
		{"a_restored_evening_holds_what_the_snapshot_held", func(t *testing.T) {
			e, ana, beto := aBusyEvening(t, now)
			snap := e.Snapshot(nil, now)
			restored, err := restoreTurns(snap, now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{21, 12}, numbers(restored))
			assert.Equal(t, e.Sung(), restored.Sung())
			assert.False(t, restored.Accepting())
			assert.True(t, restored.Seats().Holds(ana.seat, ana.token))
			assert.True(t, restored.Seats().Holds(beto.seat, beto.token))
			assert.Equal(t, e.Requests(now), restored.Requests(now))
			assert.Equal(t, snap, restored.Snapshot(nil, now), "nothing changed on the way through")
		}},
		{"a_snapshot_of_an_unchanged_evening_equals_the_one_before", func(t *testing.T) {
			e, _, _ := aBusyEvening(t, now)
			assert.Equal(t, e.Snapshot(nil, now), e.Snapshot(nil, now.Add(time.Minute)))
		}},
		{"a_snapshot_survives_its_written_form", func(t *testing.T) {
			e, _, _ := aBusyEvening(t, now)
			playing, ok := e.Pop(now)
			require.True(t, ok)
			snap := e.Snapshot(&playing, now)
			written, err := json.Marshal(snap)
			require.NoError(t, err)
			var read Snapshot
			require.NoError(t, json.Unmarshal(written, &read))
			assert.Equal(t, snap, read)
		}},
		{"a_restored_evening_issues_no_id_twice", func(t *testing.T) {
			e, ana, beto := aBusyEvening(t, now)
			before := e.Snapshot(nil, now)
			restored, err := restoreTurns(before, now)
			require.NoError(t, err)
			restored.SetAccepting(true)

			caio := newSinger(t, restored, "Caio")
			r := ask(t, restored, caio, 31, now)
			assert.Greater(t, caio.seat, max(ana.seat, beto.seat))
			for _, old := range before.Requests {
				assert.Less(t, old.ID, r.ID)
			}
			require.NotNil(t, r.Entry, "it went straight in")
			for _, old := range before.Queue {
				assert.Less(t, old.ID, *r.Entry)
			}
		}},
		{"the_song_that_was_playing_goes_back_to_the_front", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			beto := newSinger(t, e, "Beto")
			first := ask(t, e, ana, 11, now)
			ask(t, e, beto, 21, now)
			playing, ok := e.Pop(now)
			require.True(t, ok)
			assert.Len(t, e.Sung(), 1)

			restored, err := restoreTurns(e.Snapshot(&playing, now), now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{11, 21}, numbers(restored))
			front, _ := restored.Queue().Peek()
			assert.Equal(t, playing, front, "with its id and its seat")
			assert.Empty(t, restored.Sung(), "it has not been sung")
			assert.Equal(t, Queued, stateOf(t, restored, first.ID, now))
		}},
		{"a_record_of_another_song_stays_when_the_playing_song_returns", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			ask(t, e, ana, 11, now)
			playing, ok := e.Pop(now)
			require.True(t, ok)
			playing.Seat = nil
			restored, err := restoreTurns(e.Snapshot(&playing, now), now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{11}, numbers(restored))
			assert.Len(t, restored.Sung(), 1)
		}},
		{"a_snapshot_with_one_id_on_two_records_is_refused", func(t *testing.T) {
			e, _, _ := aBusyEvening(t, now)
			tests := []struct {
				what   string
				break_ func(*Snapshot)
			}{
				{"queue entries", func(s *Snapshot) { s.Queue[1].ID = s.Queue[0].ID }},
				{"seats", func(s *Snapshot) { s.Seats[1].Seat.ID = s.Seats[0].Seat.ID }},
				{"requests", func(s *Snapshot) { s.Requests[1].ID = s.Requests[0].ID }},
			}
			for _, tt := range tests {
				snap := e.Snapshot(nil, now)
				tt.break_(&snap)
				_, err := restoreTurns(snap, now)
				assert.Equal(t, ErrDuplicateID{What: tt.what}, err)
			}
		}},
		{"a_counter_behind_its_ids_is_raised", func(t *testing.T) {
			e, _, _ := aBusyEvening(t, now)
			snap := e.Snapshot(nil, now)
			snap.QueueNext, snap.SeatsNext, snap.GuestsNext, snap.RequestsNext = 0, 0, 0, 0
			restored, err := restoreTurns(snap, now)
			require.NoError(t, err)
			raised := restored.Snapshot(nil, now)
			for _, q := range snap.Queue {
				assert.Greater(t, raised.QueueNext, q.ID)
			}
			for _, s := range snap.Seats {
				assert.Greater(t, raised.SeatsNext, s.Seat.ID)
			}
			for _, r := range snap.Requests {
				assert.Greater(t, raised.RequestsNext, r.ID)
			}
		}},
		{"a_queued_request_with_no_entry_waits_again", func(t *testing.T) {
			e := NewEvening(Turns)
			ana := newSinger(t, e, "Ana")
			r := ask(t, e, ana, 11, now)
			snap := e.Snapshot(nil, now)
			snap.Queue = nil
			restored, err := restoreTurns(snap, now)
			require.NoError(t, err)
			assert.Equal(t, []uint32{11}, numbers(restored), "the rule accepted it again")
			var again Request
			for _, x := range restored.Requests(now) {
				if x.ID == r.ID {
					again = x
				}
			}
			assert.Equal(t, Queued, again.State)
			assert.NotEqual(t, *r.Entry, *again.Entry, "under a new entry")
		}},
		{"a_restored_finished_request_stays_for_the_fixed_time_again", func(t *testing.T) {
			start := now
			e, _, _ := aBusyEvening(t, start)
			late := start.Add(FinishedFor - time.Second)
			snap := e.Snapshot(nil, late)
			assert.Len(t, snap.Requests, 4, "the played one is still listed")

			restored, err := restoreTurns(snap, late)
			require.NoError(t, err)
			assert.Len(t, restored.Requests(late.Add(FinishedFor-time.Second)), 4)
			assert.Len(t, restored.Requests(late.Add(FinishedFor)), 3)
			assert.Len(t, e.Snapshot(nil, start.Add(FinishedFor)).Requests, 3,
				"and a snapshot leaves out one whose time has passed")
		}},
		{"a_snapshot_past_a_cap_is_cut_to_it", func(t *testing.T) {
			e := NewEvening(Turns)
			for n := range MaxQueued {
				addGeneric(t, e, uint32(n)+1)
			}
			playing, ok := e.Pop(now)
			require.True(t, ok)
			addGeneric(t, e, 9000)
			restored, err := restoreTurns(e.Snapshot(&playing, now), now)
			require.NoError(t, err)
			assert.Equal(t, MaxQueued, restored.Queue().Len())
			assert.Equal(t, uint32(1), numbers(restored)[0], "the playing song is first")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// Start is not in KaraokeMachine: it starts an entry that is not at the front.
func TestEveningStart(t *testing.T) {
	now := time.Now()
	e := NewEvening(Turns)
	ana := newSinger(t, e, "Ana")
	beto := newSinger(t, e, "Beto")
	ask(t, e, ana, 11, now)
	second := ask(t, e, beto, 21, now)
	next := ask(t, e, beto, 22, now)

	started, ok := e.Start(*second.Entry, now)
	require.True(t, ok)
	assert.Equal(t, uint32(21), started.Number)
	assert.Equal(t, []uint32{11, 22}, numbers(e), "his next song goes in behind")
	assert.Equal(t, Played, stateOf(t, e, second.ID, now))
	assert.Equal(t, Queued, stateOf(t, e, next.ID, now))
	assert.Equal(t, []Sung{{21, seatPtr(beto.seat)}}, e.Sung())

	_, ok = e.Start(9999, now)
	assert.False(t, ok)
}

func TestStore(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	dir := t.TempDir()
	path := filepath.Join(dir, "evening.json")

	_, ok := Load(path, logger)
	assert.False(t, ok, "no file")

	e, _, _ := aBusyEvening(t, time.Now())
	kept := Kept{Word: "maple", Evening: e.Snapshot(nil, time.Now())}
	require.NoError(t, Write(path, kept))
	read, ok := Load(path, logger)
	require.True(t, ok)
	assert.Equal(t, kept, read)

	require.NoError(t, os.WriteFile(path, []byte(`{"version": 99}`), 0o600))
	_, ok = Load(path, logger)
	assert.False(t, ok, "a newer version")
	assert.FileExists(t, path+".bad")
	assert.NoFileExists(t, path)
}
