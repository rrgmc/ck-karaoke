package server

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/kmapitest"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
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

// startQueue runs a queue build until the test ends and returns the address
// it serves on and the queue manager address it reported.
func startQueue(t *testing.T, cfg Config) (listen, device string, stop func()) {
	t.Helper()
	cfg.Backend = BackendQueue
	cfg.Listen = "127.0.0.1:0"
	cfg.Logger = slog.New(slog.DiscardHandler)
	type ready struct{ listen, device string }
	readyC := make(chan ready, 1)
	cfg.Ready = func(a net.Addr, device string) { readyC <- ready{a.String(), device} }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				assert.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Error("Run did not return after its context ended")
			}
		})
	}
	t.Cleanup(stop)
	select {
	case r := <-readyC:
		return r.listen, r.device, stop
	case err := <-done:
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}
	return "", "", stop
}

// waitForSongs waits until the server at addr browses a song list, which the
// queue build downloads after it starts serving.
func waitForSongs(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + strings.TrimPrefix(addr, "http://") + "/")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		page, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(page), "PRESENTE DE DEUS")
	}, 10*time.Second, 20*time.Millisecond, "the song list never loaded")
}

// The song list arrives after the server is serving, once the queue manager
// is reached.
func TestRunQueueDownloadsTheCatalogueAndServes(t *testing.T) {
	served := songDB(t)
	qm := kmapitest.New(t, kmapi.Queue, served)
	dir := t.TempDir()
	songs := filepath.Join(dir, "song.db")
	addr, device, _ := startQueue(t, Config{
		Addr:   qm.URL,
		SongDB: songs,
		FavDB:  filepath.Join(dir, "favorites.db"),
	})
	assert.Equal(t, qm.URL, device)

	want, err := os.ReadFile(served)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := os.ReadFile(songs)
		return err == nil && bytes.Equal(want, got)
	}, 10*time.Second, 20*time.Millisecond, "the queue manager's song database")

	waitForSongs(t, addr)

	resp, err := http.Post("http://"+addr+"/song/1001/reserve", "", nil)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Empty(t, qm.Player.Calls(), "queueing goes to the queue manager, not to the unit")
}

// With no queue manager and no song list, the build still serves, and the
// Songs tab offers the import.
func TestRunQueueServesWithoutACatalogue(t *testing.T) {
	dir := t.TempDir()
	addr, _, _ := startQueue(t, Config{
		Addr:   "http://127.0.0.1:1",
		SongDB: filepath.Join(dir, "song.db"),
		FavDB:  filepath.Join(dir, "favorites.db"),
	})

	resp, err := http.Get("http://" + addr + "/")
	require.NoError(t, err)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(page), `href="/songlist/import"`)
	assert.Contains(t, string(page), "The song list comes from the queue manager.")
}

// A remembered address is kept while the queue manager answers there, which
// needs no search at all.
func TestRunQueueUsesTheRememberedAddress(t *testing.T) {
	qm := kmapitest.New(t, kmapi.Queue, songDB(t))
	dir := t.TempDir()
	lastAddrFile := filepath.Join(dir, "queue-manager.addr")
	host := strings.TrimPrefix(qm.URL, "http://")
	require.NoError(t, os.WriteFile(lastAddrFile, []byte(host+"\n"), 0o644))

	_, device, _ := startQueue(t, Config{
		SongDB:       filepath.Join(dir, "song.db"),
		FavDB:        filepath.Join(dir, "favorites.db"),
		LastAddrFile: lastAddrFile,
	})
	assert.Equal(t, qm.URL, device)
	require.Eventually(t, func() bool { return exists(filepath.Join(dir, "song.db")) },
		10*time.Second, 20*time.Millisecond, "the song list came from the remembered address")
	assert.Equal(t, host, readAddr(lastAddrFile))
}

// A song list that differs from the queue manager's is downloaded beside it,
// and installed by the next start.
func TestRunQueueStagesANewerCatalogue(t *testing.T) {
	served := songDB(t)
	qm := kmapitest.New(t, kmapi.Queue, served)
	dir := t.TempDir()
	songs := filepath.Join(dir, "song.db")
	old := songDB(t)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(old))
	require.NoError(t, err)
	_, err = db.Exec(`insert into TableSong values (1002, 'OUTRA', 'FERNANDINHO', 1, 'OUTRA', 15)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	content, err := os.ReadFile(old)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(songs, content, 0o644))

	cfg := Config{Addr: qm.URL, SongDB: songs, FavDB: filepath.Join(dir, "favorites.db")}
	_, _, stop := startQueue(t, cfg)
	require.Eventually(t, func() bool { return exists(songs + stagedSuffix) },
		10*time.Second, 20*time.Millisecond, "the newer list waits beside the one in use")
	got, err := os.ReadFile(songs)
	require.NoError(t, err)
	assert.Equal(t, content, got, "the list in use is left alone while it is open")

	stop()
	promoteSongs(songs, quietLogger())
	want, err := os.ReadFile(served)
	require.NoError(t, err)
	got, err = os.ReadFile(songs)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.False(t, exists(songs+stagedSuffix))
}

func TestPromoteSongsDropsADamagedDownload(t *testing.T) {
	dir := t.TempDir()
	songs := filepath.Join(dir, "song.db")
	require.NoError(t, os.WriteFile(songs, []byte("in use"), 0o644))
	require.NoError(t, os.WriteFile(songs+stagedSuffix, []byte("not really sqlite"), 0o644))

	promoteSongs(songs, quietLogger())
	got, err := os.ReadFile(songs)
	require.NoError(t, err)
	assert.Equal(t, "in use", string(got))
	assert.False(t, exists(songs+stagedSuffix))
}

func TestTheQueueBuildHasASetupPage(t *testing.T) {
	qm := kmapitest.New(t, kmapi.Request, songDB(t))
	dir := t.TempDir()
	ready := make(chan string, 1)
	cfg := Config{
		Backend: BackendQueue,
		Addr:    qm.URL,
		Listen:  "127.0.0.1:0",
		SongDB:  filepath.Join(dir, "song.db"),
		FavDB:   filepath.Join(dir, "favorites.db"),
		Logger:  slog.New(slog.DiscardHandler),
		Ready:   func(a net.Addr, _ string) { ready <- a.String() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	t.Cleanup(func() { cancel(); <-done })
	var base string
	select {
	case a := <-ready:
		base = "http://" + a
	case err := <-done:
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}

	resp, err := http.Get(base + "/setup")
	require.NoError(t, err)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(page), `href="/setup"`, "the Setup tab")
	assert.Contains(t, string(page), "· CK Queue Remote</title>")
	assert.Contains(t, string(page), "Living room")
	assert.Contains(t, string(page), "request songs")

	resp, err = http.PostForm(base+"/setup/code", url.Values{"code": {kmapitest.HostCode}})
	require.NoError(t, err)
	fragment, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(fragment), "run the evening", "the host code's level")
	assert.Contains(t, string(fragment), "Forget the code")

	saved, err := os.ReadFile(filepath.Join(dir, "access.json"))
	require.NoError(t, err)
	assert.Contains(t, string(saved), "00112233aabbccdd", "the code is kept beside the song database")

	resp, err = http.Get(base + "/host")
	require.NoError(t, err)
	host, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the host code opens the Host page")
	assert.Contains(t, string(host), `href="/host"`, "the Host tab")
	assert.Contains(t, string(host), "New evening")

	resp, err = http.PostForm(base+"/host/add-singer", url.Values{"value": {"Bia"}})
	require.NoError(t, err)
	fragment, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(fragment), "Bia")

	resp, err = http.PostForm(base+"/setup/forget", nil)
	require.NoError(t, err)
	resp.Body.Close()
	resp, err = http.Get(base + "/host")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "no Host page without the code")
}

func TestTheQueueBuildRequestsForThisPhone(t *testing.T) {
	qm := kmapitest.New(t, kmapi.Request, songDB(t))
	dir := t.TempDir()
	ready := make(chan string, 1)
	cfg := Config{
		Backend: BackendQueue,
		Addr:    qm.URL,
		Listen:  "127.0.0.1:0",
		SongDB:  filepath.Join(dir, "song.db"),
		FavDB:   filepath.Join(dir, "favorites.db"),
		Logger:  slog.New(slog.DiscardHandler),
		Ready:   func(a net.Addr, _ string) { ready <- a.String() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	t.Cleanup(func() { cancel(); <-done })
	var base string
	select {
	case a := <-ready:
		base = "http://" + a
	case err := <-done:
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}
	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(base + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		return string(b)
	}
	post := func(path string, form url.Values) string {
		t.Helper()
		resp, err := http.PostForm(base+path, form)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, path+": "+string(b))
		return string(b)
	}

	waitForSongs(t, base)
	songs := get("/")
	assert.Contains(t, songs, `hx-post="/song/1001/request"`, "+ requests the song")
	assert.NotContains(t, songs, "/song/1001/reserve")
	assert.Contains(t, songs, `href="/mine"`, "the My songs tab")

	now := get("/now")
	assert.NotContains(t, now, "/control/key", "no mixer")
	assert.NotContains(t, now, "/control/next")
	assert.Contains(t, now, "/control/skip")

	post("/song/1001/request", nil)
	post("/mine/name", url.Values{"name": {"Ana"}})
	mine := get("/mine")
	assert.Contains(t, mine, `value="Ana"`)
	assert.Contains(t, mine, "PRESENTE DE DEUS", "the request, named from the catalogue")
	assert.Contains(t, mine, "/withdraw")

	singers, err := kmclient.New(qm.URL, nil).Singers(context.Background())
	require.NoError(t, err)
	require.Len(t, singers.Singers, 1)
	require.NotNil(t, singers.Singers[0].Name)
	assert.Equal(t, "Ana", *singers.Singers[0].Name)
}
