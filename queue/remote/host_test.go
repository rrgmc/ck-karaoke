package remote_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/kmapitest"
	"github.com/rrgmc/ck-karaoke/webui"
)

func TestOnlyAHostCodeOpensTheHostPage(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	b := startWithTokens(t, srv.URL, filepath.Join(t.TempDir(), "access.json"))
	ctx := context.Background()

	assert.False(t, b.HostAllowed())
	require.NoError(t, b.EnterCode(ctx, kmapitest.ControlCode))
	assert.False(t, b.HostAllowed(), "control is not host")
	require.NoError(t, b.EnterCode(ctx, kmapitest.HostCode))
	assert.True(t, b.HostAllowed())
	require.NoError(t, b.ForgetCode())
	assert.False(t, b.HostAllowed())
}

func TestTheHostRunsTheEvening(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	phone := startSinger(t, srv.URL, filepath.Join(t.TempDir(), "singer.json"))
	host := startWithTokens(t, srv.URL, filepath.Join(t.TempDir(), "access.json"))
	ctx := context.Background()
	require.NoError(t, host.EnterCode(ctx, kmapitest.HostCode))

	require.NoError(t, phone.SetName(ctx, "Ana"))
	for _, song := range []string{"1001", "3656", "6541"} {
		require.NoError(t, phone.SongAction(ctx, "request", song))
	}

	v := host.HostView(ctx)
	require.NoError(t, v.Err)
	assert.True(t, v.Accepting)
	require.Len(t, v.Singers, 1)
	assert.Equal(t, webui.HostSinger{ID: v.Singers[0].ID, Name: "Ana", Phone: true}, v.Singers[0])
	require.Len(t, v.Waiting, 2, "one song per singer goes to the queue; the rest wait")
	assert.Equal(t, "Ana", v.Waiting[0].Singer)
	assert.Equal(t, "3656", v.Waiting[0].SongID)

	require.NoError(t, host.HostAction(ctx, "accept", v.Waiting[0].ID, ""))
	require.NoError(t, host.HostAction(ctx, "decline", v.Waiting[1].ID, ""))
	assert.Empty(t, host.HostView(ctx).Waiting)
	states := map[string]string{}
	for _, r := range phone.Me(ctx).Requests {
		states[r.SongID] = r.State
	}
	assert.Equal(t, map[string]string{"1001": "queued", "3656": "queued", "6541": "declined"}, states)

	require.NoError(t, host.HostAction(ctx, "close", "", ""))
	assert.False(t, host.HostView(ctx).Accepting)
	assert.ErrorIs(t, phone.SongAction(ctx, "request", "1001"), webui.ErrRequestsClosed)
	require.NoError(t, host.HostAction(ctx, "open", "", ""))

	require.NoError(t, host.HostAction(ctx, "add-singer", "", "Bia"))
	v = host.HostView(ctx)
	require.Len(t, v.Singers, 2)
	bia := v.Singers[1]
	assert.Equal(t, "Bia", bia.Name)
	assert.False(t, bia.Phone)
	require.NoError(t, host.HostAction(ctx, "remove-singer", bia.ID, ""))
	assert.Len(t, host.HostView(ctx).Singers, 1)

	require.NoError(t, host.HostAction(ctx, "reset", "", ""))
	v = host.HostView(ctx)
	assert.Empty(t, v.Singers)
	assert.Empty(t, v.Waiting)

	assert.ErrorIs(t, host.HostAction(ctx, "dance", "", ""), webui.ErrUnsupported)
	assert.Error(t, host.HostAction(ctx, "accept", "x", ""))
}

func TestHostActionsNeedTheHostCode(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	b, _ := start(t, srv.URL)
	require.Eventually(t, b.Online, wait, tick)
	assert.ErrorIs(t, b.HostAction(context.Background(), "reset", "", ""), webui.ErrNeedsCode)
}
