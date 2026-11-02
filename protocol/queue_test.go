package protocol

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func page(total, start, end int) *Queue {
	q := &Queue{Total: total, Start: start, End: end}
	for i := start; i < end; i++ {
		q.Entries = append(q.Entries, QueueEntry{SongID: strconv.Itoa(1000 + i)})
	}
	return q
}

func ids(q *Queue) []string {
	out := make([]string, len(q.Entries))
	for i, e := range q.Entries {
		out[i] = e.SongID
	}
	return out
}

func TestQueueAssemblerSinglePage(t *testing.T) {
	var a QueueAssembler
	q := a.Add(page(3, 0, 3))
	require.NotNil(t, q)
	assert.Equal(t, []string{"1000", "1001", "1002"}, ids(q))
	assert.Equal(t, 0, q.Start)
	assert.Equal(t, 3, q.End)
}

func TestQueueAssemblerEmpty(t *testing.T) {
	var a QueueAssembler
	q := a.Add(page(0, 0, 0))
	require.NotNil(t, q)
	assert.Empty(t, q.Entries)
}

func TestQueueAssemblerJoinsPages(t *testing.T) {
	var a QueueAssembler
	assert.Nil(t, a.Add(page(23, 0, 10)))
	assert.Nil(t, a.Add(page(23, 10, 20)))
	q := a.Add(page(23, 20, 23))
	require.NotNil(t, q)
	require.Len(t, q.Entries, 23)
	assert.Equal(t, "1000", q.Entries[0].SongID)
	assert.Equal(t, "1022", q.Entries[22].SongID)
	assert.Equal(t, 0, q.Start)
	assert.Equal(t, 23, q.End)
	assert.Equal(t, 23, q.Total)

	// The next list starts afresh.
	q = a.Add(page(2, 0, 2))
	require.NotNil(t, q)
	assert.Equal(t, []string{"1000", "1001"}, ids(q))
}

func TestQueueAssemblerDropsOrphanPage(t *testing.T) {
	var a QueueAssembler
	assert.Nil(t, a.Add(page(11, 10, 11)), "a later page without its first is not a list")
}

func TestQueueAssemblerRestartsOnNewList(t *testing.T) {
	var a QueueAssembler
	assert.Nil(t, a.Add(page(11, 0, 10)))
	// The list changed before its last page arrived; a new one begins.
	assert.Nil(t, a.Add(page(12, 0, 10)))
	assert.Nil(t, a.Add(page(11, 10, 11)), "page from the abandoned list")
}

func TestQueueAssemblerRestartAfterMismatch(t *testing.T) {
	var a QueueAssembler
	assert.Nil(t, a.Add(page(12, 0, 10)))
	assert.Nil(t, a.Add(page(12, 11, 12)), "gap in the pages")
	assert.Nil(t, a.Add(page(12, 10, 12)), "assembly was dropped")
	assert.Nil(t, a.Add(page(12, 0, 10)))
	require.NotNil(t, a.Add(page(12, 10, 12)))
}
