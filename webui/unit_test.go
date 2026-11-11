package webui

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/protocol"
)

func TestUnitAckTreatsAMissingEchoAsNotAcknowledged(t *testing.T) {
	assert.NoError(t, unitAck(nil))
	assert.ErrorIs(t, unitAck(fmt.Errorf("pause: %w", context.DeadlineExceeded)), ErrNotAcknowledged)
	assert.ErrorIs(t, unitAck(errors.New("timed out waiting for echo")), ErrNotAcknowledged)

	offline := unitAck(ErrOffline)
	assert.ErrorIs(t, offline, ErrOffline)
	assert.NotErrorIs(t, offline, ErrNotAcknowledged)
}

func TestUnitStatusCarriesEveryField(t *testing.T) {
	assert.Nil(t, unitStatus(nil))

	st := unitStatus(&protocol.Status{
		SongPosition: 75 * time.Second, CurrentSong: "Song", CurrentSongID: "1234",
		NextSong: "Other", Key: -2, Tempo: 1, MusicVolume: 40, MicVolume: 30, Echo: 20,
		Lyrics: []string{"line"},
	})
	require.NotNil(t, st)
	assert.Equal(t, Status{
		Title: "Song", SongID: "1234", Next: "Other", Position: 75 * time.Second,
		Key: -2, Tempo: 1, Volume: 40, Mic: 30, Echo: 20, Lyrics: []string{"line"},
	}, *st)
}

func TestUnitQueueNamesWhoQueuedEachSong(t *testing.T) {
	assert.Nil(t, unitQueue(nil))

	q := unitQueue(&protocol.Queue{Total: 7, Start: 5, Entries: []protocol.QueueEntry{
		{SongID: "1001", Fields: []string{"0", "3.Pixel 8", "0"}},
		{SongID: "1002"},
	}})
	require.NotNil(t, q)
	assert.Equal(t, Queue{Total: 7, Start: 5, Entries: []QueueEntry{
		{EntryID: "1001", SongID: "1001", By: "Pixel 8"},
		{EntryID: "1002", SongID: "1002"},
	}}, *q)
}
