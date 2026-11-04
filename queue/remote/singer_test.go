package remote_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/kmapitest"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
	"github.com/rrgmc/ck-karaoke/queue/remote"
	"github.com/rrgmc/ck-karaoke/webui"
)

// startSinger runs a backend that keeps its singer in path.
func startSinger(t *testing.T, url, path string) *remote.Backend {
	t.Helper()
	b := remote.New(kmclient.New(url, nil), slog.New(slog.DiscardHandler))
	b.UseSingerFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, b.Online, wait, tick)
	return b
}

func TestAPhoneRequestsAsItsOwnSinger(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	path := filepath.Join(t.TempDir(), "singer.json")
	b := startSinger(t, srv.URL, path)
	ctx := context.Background()

	assert.Equal(t, webui.Capabilities{Requests: true, EntryIDs: true, NoMixer: true, NoCutAhead: true},
		b.Capabilities())

	me := b.Me(ctx)
	require.NoError(t, me.Err)
	assert.Empty(t, me.Name, "no singer before the first request")
	assert.Empty(t, me.Requests)
	assert.True(t, me.Accepting)

	require.NoError(t, b.SongAction(ctx, "request", "3656"))
	require.NoError(t, b.SetName(ctx, "Ana"))
	require.NoError(t, b.SongAction(ctx, "request", "6541"))

	me = b.Me(ctx)
	require.NoError(t, me.Err)
	assert.Equal(t, "Ana", me.Name)
	require.Len(t, me.Requests, 2)
	assert.Equal(t, "3656", me.Requests[0].SongID)
	assert.NotEmpty(t, me.Requests[0].State)

	singers, err := b.Client().Singers(ctx)
	require.NoError(t, err)
	assert.Len(t, singers.Singers, 1, "both songs went to one singer")

	require.NoError(t, b.Withdraw(ctx, me.Requests[1].ID))
	me = b.Me(ctx)
	assert.Equal(t, "withdrawn", me.Requests[1].State)

	// A restart keeps the singer.
	again := startSinger(t, srv.URL, path)
	me = again.Me(ctx)
	require.NoError(t, me.Err)
	assert.Equal(t, "Ana", me.Name)
	assert.Len(t, me.Requests, 2)
}

func TestANewEveningStartsANewSinger(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	b := startSinger(t, srv.URL, filepath.Join(t.TempDir(), "singer.json"))
	ctx := context.Background()
	require.NoError(t, b.SetName(ctx, "Ana"))

	srv.API.ResetEvening()

	me := b.Me(ctx)
	require.NoError(t, me.Err)
	assert.Empty(t, me.Name, "last evening's seat is gone")
	require.NoError(t, b.SongAction(ctx, "request", "1001"))
	me = b.Me(ctx)
	assert.Len(t, me.Requests, 1)
}

func TestClosedRequestsAreSaidSo(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	b := startSinger(t, srv.URL, filepath.Join(t.TempDir(), "singer.json"))
	ctx := context.Background()
	host := kmclient.New(srv.URL, nil)
	_, err := host.Login(ctx, kmapitest.HostCode)
	require.NoError(t, err)
	_, err = host.SetAccepting(ctx, false)
	require.NoError(t, err)

	assert.ErrorIs(t, b.SongAction(ctx, "request", "1001"), webui.ErrRequestsClosed)
	assert.False(t, b.Me(ctx).Accepting)
}

func TestEntriesMoveAndGoByTheirIDs(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Control, "")
	b, _ := start(t, srv.URL)
	ctx := context.Background()
	require.Eventually(t, b.Online, wait, tick)
	for _, song := range []string{"1001", "3656", "6541"} {
		require.NoError(t, b.SongAction(ctx, "reserve", song))
	}
	require.NoError(t, b.RefreshQueue(ctx))
	ids := func() []string {
		var out []string
		for _, e := range b.Queue().Entries {
			out = append(out, e.SongID)
		}
		return out
	}
	q := b.Queue()
	require.Len(t, q.Entries, 3)
	assert.NotEmpty(t, q.Entries[0].EntryID)

	require.NoError(t, b.EntryAction(ctx, "up", q.Entries[2].EntryID))
	assert.Equal(t, []string{"1001", "6541", "3656"}, ids())
	require.NoError(t, b.EntryAction(ctx, "down", q.Entries[0].EntryID))
	assert.Equal(t, []string{"6541", "1001", "3656"}, ids())
	require.NoError(t, b.EntryAction(ctx, "remove", q.Entries[0].EntryID))
	assert.Equal(t, []string{"6541", "3656"}, ids())

	assert.ErrorIs(t, b.EntryAction(ctx, "sideways", q.Entries[1].EntryID), webui.ErrUnsupported)
	assert.Error(t, b.EntryAction(ctx, "up", "999"))
}
