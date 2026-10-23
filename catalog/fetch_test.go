package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDownloader plays frames to the sink the way client.DownloadDatabase does,
// and records where the sink asked it to stop.
type fakeDownloader struct {
	frames  [][]byte
	stopped int // frames delivered when the sink returned an error; 0 if never
}

func (f *fakeDownloader) DownloadDatabase(_ context.Context, sink func([]byte) error,
	_ client.DownloadOptions) (int, error) {
	for i, fr := range f.frames {
		if err := sink(fr); err != nil {
			f.stopped = i + 1
			if errors.Is(err, client.ErrDownloadComplete) {
				return i + 1, nil
			}
			return i + 1, err
		}
	}
	return len(f.frames), nil
}

// The device sends no terminator, so Fetch must end the transfer itself once
// the declared size has arrived rather than wait for the stream to go quiet.
func TestFetchEndsTheTransferOnTheLastChunk(t *testing.T) {
	frames := chunkFrames(readFixture(t), 1500)
	require.Greater(t, len(frames), 2, "the fixture should span several chunks")
	dev := &fakeDownloader{frames: frames}

	path := filepath.Join(t.TempDir(), "song.db")
	require.NoError(t, Fetch(context.Background(), dev, path, nil))
	assert.Equal(t, len(frames), dev.stopped)
	assert.NoError(t, Verify(path))
}
