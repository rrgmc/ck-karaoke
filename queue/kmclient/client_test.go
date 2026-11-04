package kmclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
)

// The client runs against a real kmapi.Server, so a change on either side of
// the contract fails here.

type songs []catalog.Song

func (c songs) Song(_ context.Context, id int) (catalog.Song, error) {
	for _, s := range c {
		if s.ID == id {
			return s, nil
		}
	}
	return catalog.Song{}, errors.New("no such song")
}

func (c songs) SongsAfter(_ context.Context, after, limit int) ([]catalog.Song, error) {
	var out []catalog.Song
	for _, s := range c {
		if s.ID > after && len(out) < limit {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c songs) Info() catalog.Info { return catalog.Info{DBVersion: 7, Songs: len(c)} }

var testSongs = songs{
	{ID: 1001, Title: "PRESENTE DE DEUS", Artist: "FERNANDINHO"},
	{ID: 3656, Title: "MENINA", Artist: "LUAN SANTANA"},
	{ID: 6541, Title: "4 MINUTES", Artist: "MADONNA"},
}

type player struct {
	mu    sync.Mutex
	state feeder.State
	calls []string
}

func (p *player) State() feeder.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *player) call(c string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, c)
	return nil
}

func (p *player) Play(context.Context) error               { return p.call("play") }
func (p *player) Pause(context.Context) error              { return p.call("pause") }
func (p *player) Skip(context.Context) error               { return p.call("skip") }
func (p *player) Restart(context.Context) error            { return p.call("restart") }
func (p *player) Stop(context.Context) error               { return p.call("stop") }
func (p *player) SetTranspose(context.Context, int) error  { return p.call("transpose") }
func (p *player) SetTempo(context.Context, float64) error  { return p.call("tempo") }
func (p *player) SetVolume(context.Context, float64) error { return p.call("volume") }
func (p *player) setConnected(c bool)                      { p.mu.Lock(); p.state.Connected = c; p.mu.Unlock() }
func (p *player) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

type rig struct {
	srv    *kmapi.Server
	player *player
	url    string
}

const (
	queueCode = "1111"
	hostCode  = "3333"
)

// newRig serves a queue manager's API with the given room level and song
// database file ("" for none), and runs its event loop for the test.
func newRig(t *testing.T, room kmapi.Level, catalogFile string) *rig {
	t.Helper()
	lookup := func(n uint32) (fair.Song, bool) {
		s, err := testSongs.Song(context.Background(), int(n))
		return fair.Song{Number: n, Title: s.Title, Artist: s.Artist}, err == nil
	}
	p := &player{state: feeder.State{Connected: true, Transport: feeder.Idle, Volume: 50}}
	srv := kmapi.New(kmapi.Options{
		Room:        feeder.NewRoom(fair.NewEvening(fair.Turns), lookup),
		Player:      p,
		Catalog:     testSongs,
		CatalogFile: catalogFile,
		Unit:        func() (string, string) { return "MK8513-BR-B", "d91c983b" },
		ID:          "00112233aabbccdd",
		Secret:      []byte("secret"),
		Version:     "1.3.0",
		Config: kmapi.Config{
			Name: "Living room", Room: room,
			Codes: kmapi.Codes{Queue: queueCode, Control: "2222", Host: hostCode},
		},
		Logger: slog.New(slog.DiscardHandler),
		Addrs:  func() []net.IP { return []net.IP{net.IPv4(127, 0, 0, 1)} },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); srv.Run(ctx) }()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		cancel()
		<-done
		hs.Close()
	})
	return &rig{srv: srv, player: p, url: hs.URL}
}

func (r *rig) client() *kmclient.Client { return kmclient.New(r.url, nil) }

func TestDiscoverAndTheCKCatalogue(t *testing.T) {
	content := []byte("SQLite format 3\x00 the song database")
	path := filepath.Join(t.TempDir(), "song.db")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	r := newRig(t, kmapi.View, path)
	c := r.client()
	ctx := context.Background()

	d, err := c.Discover(ctx)
	require.NoError(t, err)
	assert.Equal(t, "00112233aabbccdd", d.ID)
	assert.Equal(t, "Living room", d.Name)

	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	cat, err := c.CKCatalog(ctx)
	require.NoError(t, err)
	assert.Equal(t, wire.CKCatalog{
		Model: "MK8513-BR-B", DatabaseID: "d91c983b", DBVersion: 7, Songs: len(testSongs),
		Size: int64(len(content)), SHA256: want,
	}, cat)

	var b bytes.Buffer
	changed, err := c.DownloadSongDB(ctx, "", &b)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, content, b.Bytes())

	b.Reset()
	changed, err = c.DownloadSongDB(ctx, want, &b)
	require.NoError(t, err)
	assert.False(t, changed, "a copy with the same hash downloads nothing")
	assert.Zero(t, b.Len())
}

func TestAServerWithoutTheCKRoutesIsNotCK(t *testing.T) {
	c := newRig(t, kmapi.View, "").client()

	_, err := c.CKCatalog(context.Background())
	assert.ErrorIs(t, err, kmclient.ErrNotCK)
	_, err = c.DownloadSongDB(context.Background(), "", &bytes.Buffer{})
	assert.ErrorIs(t, err, kmclient.ErrNotCK)
}

func TestACodeOpensTheQueue(t *testing.T) {
	r := newRig(t, kmapi.Request, "")
	c := r.client()
	ctx := context.Background()

	a, err := c.Access(ctx)
	require.NoError(t, err)
	assert.Equal(t, kmapi.Request, a.Access)
	assert.True(t, a.QueueCode)

	_, err = c.AddToQueue(ctx, 1001)
	assert.ErrorIs(t, err, kmclient.ErrUnauthorized, "the room level cannot add a song with no singer")

	_, err = c.Login(ctx, "9999")
	assert.ErrorIs(t, err, kmclient.ErrUnauthorized, "a code that opens nothing")
	assert.Empty(t, c.Token())

	g, err := c.Login(ctx, queueCode)
	require.NoError(t, err)
	assert.Equal(t, kmapi.Queue, g.Access)
	assert.Equal(t, g.Token, c.Token())

	first, err := c.AddToQueue(ctx, 1001)
	require.NoError(t, err)
	assert.Equal(t, "PRESENTE DE DEUS", first.Title)
	second, err := c.AddToQueue(ctx, 6541)
	require.NoError(t, err)

	q, err := c.Queue(ctx)
	require.NoError(t, err)
	require.Len(t, q.Entries, 2)
	assert.Equal(t, wire.Number(1001), q.Entries[0].Number)

	_, err = c.MoveEntry(ctx, second.EntryID, 0)
	assert.ErrorIs(t, err, kmclient.ErrUnauthorized, "moving needs control")

	c.SetToken("")
	_, err = c.Login(ctx, hostCode)
	require.NoError(t, err)
	q, err = c.MoveEntry(ctx, second.EntryID, 0)
	require.NoError(t, err)
	assert.Equal(t, wire.Number(6541), q.Entries[0].Number)

	q, err = c.RemoveEntry(ctx, second.EntryID)
	require.NoError(t, err)
	require.Len(t, q.Entries, 1)
	assert.Equal(t, first.EntryID, q.Entries[0].ID)

	q, err = c.ClearQueue(ctx)
	require.NoError(t, err)
	assert.Empty(t, q.Entries)
}

func TestASingerRequestsAndTheHostDecides(t *testing.T) {
	r := newRig(t, kmapi.Request, "")
	phone, host := r.client(), r.client()
	ctx := context.Background()
	_, err := host.Login(ctx, hostCode)
	require.NoError(t, err)

	seat, err := phone.RegisterSinger(ctx, "phone-1", "")
	require.NoError(t, err)
	assert.Nil(t, seat.Singer.Name)
	assert.NotEmpty(t, seat.Token)

	named, err := phone.RenameSinger(ctx, seat.Singer.ID, seat.Token, "Ana")
	require.NoError(t, err)
	require.NotNil(t, named.Name)
	assert.Equal(t, "Ana", *named.Name)

	_, err = phone.Request(ctx, seat.Singer.ID, "wrong token", 3656)
	assert.Error(t, err)

	asked, err := phone.Request(ctx, seat.Singer.ID, seat.Token, 3656)
	require.NoError(t, err)
	assert.Equal(t, "MENINA", asked.Title)

	again, err := phone.Request(ctx, seat.Singer.ID, seat.Token, 6541)
	require.NoError(t, err)
	withdrawn, err := phone.Withdraw(ctx, again.ID, seat.Token)
	require.NoError(t, err)
	assert.NotEqual(t, again.State, withdrawn.State)

	_, err = phone.Accept(ctx, asked.ID, false)
	assert.ErrorIs(t, err, kmclient.ErrUnauthorized, "accepting needs host")

	rs, err := host.Requests(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, rs.Requests)

	guest, err := host.CreateSinger(ctx, "Bia")
	require.NoError(t, err)
	forGuest, err := host.RequestFor(ctx, guest.ID, 1001)
	require.NoError(t, err)
	_, err = host.Decline(ctx, forGuest.ID)
	require.NoError(t, err)

	rs, err = host.SetAccepting(ctx, false)
	require.NoError(t, err)
	assert.False(t, rs.Accepting)
	_, err = phone.Request(ctx, seat.Singer.ID, seat.Token, 1001)
	var e *kmclient.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "requests_closed", e.Code)

	singers, err := host.Singers(ctx)
	require.NoError(t, err)
	assert.Len(t, singers.Singers, 2)
	_, err = host.RemoveSinger(ctx, guest.ID)
	require.NoError(t, err)

	_, err = host.ResetEvening(ctx)
	require.NoError(t, err)
	singers, err = host.Singers(ctx)
	require.NoError(t, err)
	assert.Empty(t, singers.Singers)
}

func TestTransportAndAnAbsentPlayer(t *testing.T) {
	r := newRig(t, kmapi.Control, "")
	c := r.client()
	ctx := context.Background()

	_, err := c.Transport(ctx, "stop")
	require.NoError(t, err)
	assert.Equal(t, []string{"stop"}, r.player.Calls())

	r.player.setConnected(false)
	st, err := c.State(ctx)
	require.NoError(t, err)
	assert.False(t, st.PlayerOnline)

	_, err = c.Transport(ctx, "stop")
	var e *kmclient.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, http.StatusConflict, e.Status)
	assert.Equal(t, "player_offline", e.Code)
}

func TestFollowDeliversTheStateAndTheChanges(t *testing.T) {
	r := newRig(t, kmapi.Queue, "")
	c := r.client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan kmclient.Event, 64)
	done := make(chan error, 1)
	go func() { done <- c.Follow(ctx, func(e kmclient.Event) { events <- e }) }()

	first := next(t, events, func(e kmclient.Event) bool { return true })
	assert.Equal(t, "state", first.Kind)
	require.NotNil(t, first.State)

	_, err := c.AddToQueue(context.Background(), 1001)
	require.NoError(t, err)
	changed := next(t, events, func(e kmclient.Event) bool { return e.Kind == "queue_changed" })
	require.NotNil(t, changed.Queue)
	require.Len(t, changed.Queue.Entries, 1)
	assert.Equal(t, wire.Number(1001), changed.Queue.Entries[0].Number)

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Follow did not return after its context ended")
	}
}

func TestFollowFailsWithoutAServer(t *testing.T) {
	c := kmclient.New("http://127.0.0.1:1", nil)
	err := c.Follow(context.Background(), func(kmclient.Event) {})
	assert.Error(t, err)
}

// next waits for the first event that matches.
func next(t *testing.T, events <-chan kmclient.Event, match func(kmclient.Event) bool) kmclient.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if match(e) {
				return e
			}
		case <-timeout:
			t.Fatal("no matching event")
		}
	}
}
