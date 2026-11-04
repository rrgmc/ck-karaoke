package remote_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/kmapitest"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
	"github.com/rrgmc/ck-karaoke/queue/remote"
)

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"192.168.1.10":               "http://192.168.1.10:8177",
		"192.168.1.10:9000":          "http://192.168.1.10:9000",
		" http://host:8177/ ":        "http://host:8177",
		"http://host/api/v1":         "http://host:8177",
		"[fe80::1]":                  "http://[fe80::1]:8177",
		"https://queue.example:8443": "https://queue.example:8443",
	} {
		assert.Equal(t, want, remote.BaseURL(in), in)
	}
}

// sqliteFile writes a small SQLite database that passes catalog.Verify.
func sqliteFile(t *testing.T, path string, rows int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`create table TableSongSinger(n integer)`)
	require.NoError(t, err)
	for i := range rows {
		_, err = db.Exec(`insert into TableSongSinger values (?)`, i)
		require.NoError(t, err)
	}
}

func TestCheckTellsACKQueueManagerApart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.db")
	sqliteFile(t, path, 1)
	ck := kmapitest.New(t, kmapi.View, path)
	plain := kmapitest.New(t, kmapi.View, "")

	f, err := remote.Check(context.Background(), ck.URL)
	require.NoError(t, err)
	assert.Equal(t, "Living room", f.Name)
	assert.Equal(t, ck.URL, f.URL)
	assert.NotEmpty(t, f.Catalog.SHA256)

	_, err = remote.Check(context.Background(), plain.URL)
	assert.ErrorIs(t, err, kmclient.ErrNotCK)
}

func TestSyncCatalogueDownloadsOnlyWhatChanged(t *testing.T) {
	served := filepath.Join(t.TempDir(), "song.db")
	sqliteFile(t, served, 3)
	srv := kmapitest.New(t, kmapi.View, served)
	c := kmclient.New(srv.URL, nil)
	local := filepath.Join(t.TempDir(), "song.db")
	ctx := context.Background()

	downloaded, err := remote.SyncCatalogue(ctx, c, local)
	require.NoError(t, err)
	assert.True(t, downloaded, "no local copy")
	want, err := os.ReadFile(served)
	require.NoError(t, err)
	got, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	downloaded, err = remote.SyncCatalogue(ctx, c, local)
	require.NoError(t, err)
	assert.False(t, downloaded, "the same copy")

	require.NoError(t, os.WriteFile(local, []byte("stale"), 0o600))
	downloaded, err = remote.SyncCatalogue(ctx, c, local)
	require.NoError(t, err)
	assert.True(t, downloaded, "a different copy")

	entries, err := os.ReadDir(filepath.Dir(local))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file is left behind")
}

func TestSyncCatalogueKeepsTheOldFileWhenTheNewOneIsDamaged(t *testing.T) {
	served := filepath.Join(t.TempDir(), "song.db")
	require.NoError(t, os.WriteFile(served, []byte("not a database"), 0o600))
	srv := kmapitest.New(t, kmapi.View, served)
	local := filepath.Join(t.TempDir(), "song.db")
	sqliteFile(t, local, 1)
	before, err := os.ReadFile(local)
	require.NoError(t, err)

	_, err = remote.SyncCatalogue(context.Background(), kmclient.New(srv.URL, nil), local)
	assert.Error(t, err)
	after, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
