package server

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/kmapitest"
)

// songDB writes a song database with the unit's schema and the songs
// kmapitest's queue manager knows.
func songDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "song.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	_, err = db.Exec(`
		create table TableDBInfo (ProjectName text, ProductID int, DBVersion int);
		insert into TableDBInfo values ('MK8513-BR-B', 1, 7);
		create table TableLan (LanType int, LanName_EG text);
		insert into TableLan values (1, 'Portuguese');
		create table TableSinger (SingerIndex int, SingerName text, FirstWord text, AlphaIndex int, LanType int);
		insert into TableSinger values (1, 'FERNANDINHO', 'FERNANDINHO', 6, 1);
		create table TableSong (SongIndex int, SongName text, SingerName text, LanType int, FirstWord text, AlphaIndex int);
		insert into TableSong values (1001, 'PRESENTE DE DEUS', 'FERNANDINHO', 1, 'PRESENTE DE DEUS', 16);
		create table TableSongSinger (SongIndex int, SingerIndex int);
		insert into TableSongSinger values (1001, 1);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

func TestRunQueueDownloadsTheCatalogueAndServes(t *testing.T) {
	served := songDB(t)
	qm := kmapitest.New(t, kmapi.Queue, served)
	dir := t.TempDir()
	cfg := Config{
		Backend:      BackendQueue,
		Addr:         qm.URL,
		Listen:       "127.0.0.1:0",
		SongDB:       filepath.Join(dir, "song.db"),
		FavDB:        filepath.Join(dir, "favorites.db"),
		LastAddrFile: filepath.Join(dir, "queue-manager.addr"),
		Logger:       slog.New(slog.DiscardHandler),
	}
	ready := make(chan string, 1)
	cfg.Ready = func(a net.Addr, device string) {
		assert.Equal(t, qm.URL, device)
		ready <- a.String()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}

	want, err := os.ReadFile(served)
	require.NoError(t, err)
	got, err := os.ReadFile(cfg.SongDB)
	require.NoError(t, err)
	assert.Equal(t, want, got, "the queue manager's song database")
	assert.Equal(t, strings.TrimPrefix(qm.URL, "http://"), readAddr(cfg.LastAddrFile))

	resp, err := http.Post("http://"+addr+"/song/1001/reserve", "", nil)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Empty(t, qm.Player.Calls(), "queueing goes to the queue manager, not to the unit")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

func TestRunQueueNeedsACatalogue(t *testing.T) {
	dir := t.TempDir()
	err := Run(context.Background(), Config{
		Backend: BackendQueue,
		Addr:    "http://127.0.0.1:1",
		Listen:  "127.0.0.1:0",
		SongDB:  filepath.Join(dir, "song.db"),
		FavDB:   filepath.Join(dir, "favorites.db"),
		Logger:  slog.New(slog.DiscardHandler),
	})
	assert.Error(t, err)
}
