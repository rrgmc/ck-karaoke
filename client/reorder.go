package client

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/rrgmc/ck-karaoke/protocol"
)

// The unit cannot reorder its queue: UpOrder and DownOrder are acknowledged
// but change nothing. It can remove a song (DelOrder), add one at the end
// (AddOrder) and add one at the front (FirstOrder), and those three reach any
// order. Reorder removes the fewest entries it can and adds them back where
// they belong.

// ErrNotPlaying reports that the queue cannot be reordered while nothing
// plays: AddOrder on an idle or stopped unit starts the song at once instead of
// queueing it.
var ErrNotPlaying = errors.New("the queue can be reordered only while a song plays")

// ReorderPlan is how one order of the queue becomes another.
type ReorderPlan struct {
	// Remove lists the songs to take out, in queue order.
	Remove []string
	// Front lists the songs to add at the front, in the order to send them:
	// each one goes in front of the one sent before it.
	Front []string
	// Back lists the songs to add at the end, in the order to send them.
	Back []string
}

// Empty reports whether the plan changes nothing.
func (p ReorderPlan) Empty() bool { return len(p.Remove) == 0 }

// PlanReorder works out how to turn the queue have into want, both lists of
// song ids. want must hold the same songs as have, each once.
//
// Entries that stay put keep their order, so they must form one run of want
// that appears in have in the same order. PlanReorder picks the longest such
// run; everything else is removed, then the songs before the run are added at
// the front and the songs after it at the end.
func PlanReorder(have, want []string) (ReorderPlan, error) {
	if err := samePermutation(have, want); err != nil {
		return ReorderPlan{}, err
	}
	pos := make(map[string]int, len(have))
	for i, id := range have {
		pos[id] = i
	}
	// The longest run want[bi:bj] whose positions in have increase.
	bi, bj := 0, 0
	for i := 0; i < len(want); {
		j := i + 1
		for j < len(want) && pos[want[j]] > pos[want[j-1]] {
			j++
		}
		if j-i > bj-bi {
			bi, bj = i, j
		}
		i = j
	}
	var p ReorderPlan
	keep := make(map[string]bool, bj-bi)
	for _, id := range want[bi:bj] {
		keep[id] = true
	}
	for _, id := range have {
		if !keep[id] {
			p.Remove = append(p.Remove, id)
		}
	}
	for i := bi - 1; i >= 0; i-- {
		p.Front = append(p.Front, want[i])
	}
	p.Back = append(p.Back, want[bj:]...)
	return p, nil
}

func samePermutation(have, want []string) error {
	if len(have) != len(want) {
		return fmt.Errorf("the new order has %d songs, the queue %d", len(want), len(have))
	}
	seen := make(map[string]bool, len(have))
	for _, id := range have {
		if seen[id] {
			return fmt.Errorf("song %s is queued twice", id)
		}
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			return fmt.Errorf("song %s is not in the queue, or is listed twice", id)
		}
		delete(seen, id)
	}
	return nil
}

// MoveInQueue moves a queued song by offset places: -1 is one place earlier,
// 1 one place later. The move stops at either end of the queue.
func (c *Client) MoveInQueue(ctx context.Context, songID string, offset int) error {
	have, err := c.queueIDs(ctx)
	if err != nil {
		return err
	}
	from := slices.Index(have, songID)
	if from < 0 {
		return fmt.Errorf("song %s is not in the queue", songID)
	}
	to := min(max(from+offset, 0), len(have)-1)
	want := slices.Delete(slices.Clone(have), from, from+1)
	want = slices.Insert(want, to, songID)
	return c.reorder(ctx, have, want)
}

// Reorder puts the queue in the order of want, a list of the queued song ids.
//
// The unit has no atomic reorder, so other clients see the songs leave and
// come back, and the songs that come back show this session's name. If the
// current song ends partway through, the unit plays whatever is first in its
// queue at that moment.
func (c *Client) Reorder(ctx context.Context, want []string) error {
	have, err := c.queueIDs(ctx)
	if err != nil {
		return err
	}
	return c.reorder(ctx, have, want)
}

func (c *Client) reorder(ctx context.Context, have, want []string) error {
	p, err := PlanReorder(have, want)
	if err != nil || p.Empty() {
		return err
	}
	if st := c.Status(); st == nil || st.CurrentSong == "" {
		return ErrNotPlaying
	}
	for _, id := range p.Remove {
		if err := c.RemoveFromQueue(ctx, id); err != nil {
			return fmt.Errorf("removing song %s: %w", id, err)
		}
	}
	// The unit refuses to add a song that is still queued, so wait until the
	// removals show.
	if err := c.waitQueue(ctx, func(q *protocol.Queue) bool {
		return !slices.ContainsFunc(q.Entries, func(e protocol.QueueEntry) bool {
			return slices.Contains(p.Remove, e.SongID)
		})
	}); err != nil {
		return fmt.Errorf("waiting for the removals: %w", err)
	}
	for _, id := range p.Front {
		if err := c.ReserveFirst(ctx, id); err != nil {
			return fmt.Errorf("adding song %s at the front: %w", id, err)
		}
	}
	for _, id := range p.Back {
		if err := c.Reserve(ctx, id); err != nil {
			return fmt.Errorf("adding song %s at the end: %w", id, err)
		}
	}
	return nil
}

// queueIDs returns the song ids of the whole queue, asking the unit for it.
func (c *Client) queueIDs(ctx context.Context) ([]string, error) {
	const window = 50
	q, err := c.Queue(ctx, 0, window)
	if err == nil && q.Total > len(q.Entries) {
		q, err = c.Queue(ctx, 0, q.Total)
	}
	if err != nil {
		return nil, err
	}
	if q.Total > len(q.Entries) {
		return nil, fmt.Errorf("the unit sent %d of %d queued songs", len(q.Entries), q.Total)
	}
	ids := make([]string, len(q.Entries))
	for i, e := range q.Entries {
		ids[i] = e.SongID
	}
	return ids, nil
}

// waitQueue returns once the unit reports a queue for which done is true.
func (c *Client) waitQueue(ctx context.Context, done func(*protocol.Queue) bool) error {
	if q := c.LastQueue(); q != nil && done(q) {
		return nil
	}
	for {
		q, err := c.Queue(ctx, 0, 50)
		if err != nil {
			return err
		}
		if done(q) {
			return nil
		}
	}
}
