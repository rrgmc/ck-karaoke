package remote_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
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

// startWithTokens runs a backend that keeps its tokens in path.
func startWithTokens(t *testing.T, url, path string) *remote.Backend {
	t.Helper()
	b := remote.New(kmclient.New(url, nil), slog.New(slog.DiscardHandler))
	b.UseTokenFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, b.Online, wait, tick)
	return b
}

func TestACodeIsEnteredKeptAndForgotten(t *testing.T) {
	srv := kmapitest.New(t, kmapi.Request, "")
	path := filepath.Join(t.TempDir(), "access.json")
	b := startWithTokens(t, srv.URL, path)
	ctx := context.Background()

	info := b.SetupInfo(ctx)
	assert.Equal(t, webui.SetupInfo{
		Name: "Living room", Addr: srv.URL, Level: "request", Room: "request",
	}, info)

	assert.ErrorIs(t, b.EnterCode(ctx, "9999"), webui.ErrWrongCode)
	assert.False(t, b.SetupInfo(ctx).HasCode)

	require.NoError(t, b.EnterCode(ctx, kmapitest.QueueCode))
	info = b.SetupInfo(ctx)
	assert.Equal(t, "queue", info.Level)
	assert.True(t, info.HasCode)
	require.NoError(t, b.SongAction(ctx, "reserve", "1001"), "the code opens the queue")

	var kept map[string]string
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &kept))
	assert.NotEmpty(t, kept["00112233aabbccdd"], "kept under the queue manager's id")

	// A new run with the same file starts with the code.
	again := startWithTokens(t, srv.URL, path)
	require.Eventually(t, func() bool { return again.SetupInfo(ctx).Level == "queue" }, wait, tick)

	require.NoError(t, again.ForgetCode())
	info = again.SetupInfo(ctx)
	assert.Equal(t, "request", info.Level)
	assert.False(t, info.HasCode)
	raw, err = os.ReadFile(path)
	require.NoError(t, err)
	kept = nil
	require.NoError(t, json.Unmarshal(raw, &kept))
	assert.Empty(t, kept)
}

func TestSetupInfoWithoutAQueueManager(t *testing.T) {
	b, _ := start(t, "http://127.0.0.1:1")

	info := b.SetupInfo(context.Background())
	assert.Equal(t, "http://127.0.0.1:1", info.Addr)
	assert.ErrorIs(t, info.Err, webui.ErrQueueManager)
	assert.Empty(t, info.Level)
	assert.ErrorIs(t, b.EnterCode(context.Background(), "1111"), webui.ErrQueueManager)
}
