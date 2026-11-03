package fair

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this package are KaraokeMachine's km-queue tests, ported. Each
// case keeps the Rust test's name, so the two suites can be compared.

func song(number uint32) Song {
	return Song{Number: number, Title: fmt.Sprintf("Song %d", number), Artist: "Someone", DurationMS: 180_000}
}

func add(t *testing.T, q *Queue, s Song) uint64 {
	t.Helper()
	id, err := q.Add(s)
	require.NoError(t, err)
	return id
}

func queueNumbers(q *Queue) []uint32 {
	var out []uint32
	for _, e := range q.Entries() {
		out = append(out, e.Number)
	}
	return out
}

func seatPtr(id uint64) *uint64 { return &id }

func TestQueue(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"songs_play_in_the_order_they_were_added", func(t *testing.T) {
			q := &Queue{}
			for _, n := range []uint32{1, 2, 3} {
				add(t, q, song(n))
			}
			assert.Equal(t, 3, q.Len())
			for _, want := range []uint32{1, 2, 3} {
				e, ok := q.Pop()
				require.True(t, ok)
				assert.Equal(t, want, e.Number)
			}
			_, ok := q.Pop()
			assert.False(t, ok)
			assert.Zero(t, q.Len())
		}},
		{"ids_are_never_reused", func(t *testing.T) {
			q := &Queue{}
			first := add(t, q, song(1))
			q.Remove(first)
			second := add(t, q, song(2))
			assert.NotEqual(t, first, second)
			_, ok := q.Remove(first)
			assert.False(t, ok, "the old id must not match")
			assert.Equal(t, 1, q.Len())
		}},
		{"removing_by_id_takes_the_right_entry_even_after_earlier_removals", func(t *testing.T) {
			q := &Queue{}
			a := add(t, q, song(10))
			b := add(t, q, song(20))
			c := add(t, q, song(30))
			q.Remove(a)
			e, ok := q.Remove(b)
			require.True(t, ok)
			assert.Equal(t, uint32(20), e.Number)
			front, _ := q.Peek()
			assert.Equal(t, c, front.ID)
		}},
		{"removing_an_unknown_id_does_nothing", func(t *testing.T) {
			q := &Queue{}
			add(t, q, song(1))
			_, ok := q.Remove(9999)
			assert.False(t, ok)
			assert.Equal(t, 1, q.Len())
		}},
		{"an_entry_can_be_moved_to_the_front_to_play_next", func(t *testing.T) {
			q := &Queue{}
			add(t, q, song(1))
			add(t, q, song(2))
			third := add(t, q, song(3))
			assert.True(t, q.MoveTo(third, 0))
			front, _ := q.Peek()
			assert.Equal(t, uint32(3), front.Number)
			assert.Equal(t, 3, q.Len())
		}},
		{"moving_past_the_end_lands_at_the_end_rather_than_panicking", func(t *testing.T) {
			q := &Queue{}
			first := add(t, q, song(1))
			add(t, q, song(2))
			add(t, q, song(3))
			assert.True(t, q.MoveTo(first, 999))
			assert.Equal(t, []uint32{2, 3, 1}, queueNumbers(q))
		}},
		{"moving_to_its_own_position_changes_nothing", func(t *testing.T) {
			q := &Queue{}
			add(t, q, song(1))
			second := add(t, q, song(2))
			add(t, q, song(3))
			assert.True(t, q.MoveTo(second, 1))
			assert.Equal(t, []uint32{1, 2, 3}, queueNumbers(q))
		}},
		{"moving_an_unknown_id_reports_failure", func(t *testing.T) {
			q := &Queue{}
			add(t, q, song(1))
			assert.False(t, q.MoveTo(1234, 0))
		}},
		{"the_queue_is_capped", func(t *testing.T) {
			q := &Queue{}
			for n := range MaxQueued {
				add(t, q, song(uint32(n)))
			}
			_, err := q.Add(song(9999))
			assert.ErrorIs(t, err, ErrQueueFull)
			assert.Equal(t, MaxQueued, q.Len())
		}},
		{"room_frees_up_after_a_song_plays", func(t *testing.T) {
			q := &Queue{}
			for n := range MaxQueued {
				add(t, q, song(uint32(n)))
			}
			q.Pop()
			_, err := q.Add(song(9999))
			assert.NoError(t, err)
		}},
		{"the_same_song_may_be_queued_twice", func(t *testing.T) {
			q := &Queue{}
			add(t, q, song(42))
			add(t, q, song(42))
			assert.Equal(t, 2, q.Len())
			assert.True(t, q.ContainsNumber(42))
			assert.False(t, q.ContainsNumber(43))
		}},
		{"positions_are_reported_for_a_remote_to_show", func(t *testing.T) {
			q := &Queue{}
			a := add(t, q, song(1))
			b := add(t, q, song(2))
			assert.Equal(t, 0, q.PositionOf(a))
			assert.Equal(t, 1, q.PositionOf(b))
			q.Pop()
			assert.Equal(t, 0, q.PositionOf(b), "positions shift as songs play")
			assert.Equal(t, -1, q.PositionOf(a))
		}},
		{"clearing_returns_what_was_removed", func(t *testing.T) {
			q := &Queue{}
			add(t, q, song(1))
			add(t, q, song(2))
			assert.Len(t, q.Clear(), 2)
			assert.Zero(t, q.Len())
		}},
		{"labels_include_the_details_that_are_present", func(t *testing.T) {
			e := Entry{Number: 10234, Title: "Exagerado", Artist: "Cazuza", Singer: "Ana"}
			assert.Equal(t, "10234  Exagerado — Cazuza (Ana)", e.Label())
			assert.Equal(t, "1  Just A Title", Entry{Number: 1, Title: "Just A Title"}.Label())
		}},
		{"a_seat_holds_a_song_while_one_of_its_entries_waits", func(t *testing.T) {
			q := &Queue{}
			add(t, q, song(1))
			s := song(2)
			s.Seat = seatPtr(7)
			id := add(t, q, s)
			assert.True(t, q.HoldsSeat(7))
			assert.False(t, q.HoldsSeat(8), "a song with no singer belongs to no seat")
			q.Remove(id)
			assert.False(t, q.HoldsSeat(7))
		}},
		{"an_empty_queue_reports_nothing_up_next", func(t *testing.T) {
			q := &Queue{}
			_, ok := q.Peek()
			assert.False(t, ok)
			assert.Empty(t, q.Entries())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
