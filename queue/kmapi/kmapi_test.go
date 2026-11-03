package kmapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// fakeCatalog is a catalogue of a few songs.
type fakeCatalog struct{ songs []catalog.Song }

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{songs: []catalog.Song{
		{ID: 1001, Title: "PRESENTE DE DEUS", Artist: "FERNANDINHO", Language: "Portuguese"},
		{ID: 1229, Title: "CONQUISTA", Artist: "", Language: "Brazil"},
		{ID: 3656, Title: "MENINA", Artist: "LUAN SANTANA", Language: "Portuguese"},
		{ID: 6541, Title: "4 MINUTES", Artist: "MADONNA", Language: "English"},
		{ID: 19535, Title: "FLY ME TO THE MOON", Artist: "FRANK SINATRA", Language: "Dance"},
	}}
}

func (c *fakeCatalog) Song(_ context.Context, id int) (catalog.Song, error) {
	for _, s := range c.songs {
		if s.ID == id {
			return s, nil
		}
	}
	return catalog.Song{}, errors.New("no such song")
}

func (c *fakeCatalog) SongsAfter(_ context.Context, after, limit int) ([]catalog.Song, error) {
	var out []catalog.Song
	for _, s := range c.songs {
		if s.ID > after && len(out) < limit {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *fakeCatalog) Info() catalog.Info {
	return catalog.Info{ProjectName: "CooKara", DBVersion: 2, Songs: len(c.songs)}
}

// fakePlayer records the commands it is given.
type fakePlayer struct {
	mu    sync.Mutex
	state feeder.State
	calls []string
}

func (p *fakePlayer) State() feeder.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *fakePlayer) call(c string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, c)
	return nil
}

func (p *fakePlayer) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *fakePlayer) Play(context.Context) error    { return p.call("play") }
func (p *fakePlayer) Pause(context.Context) error   { return p.call("pause") }
func (p *fakePlayer) Skip(context.Context) error    { return p.call("skip") }
func (p *fakePlayer) Restart(context.Context) error { return p.call("restart") }
func (p *fakePlayer) Stop(context.Context) error    { return p.call("stop") }
func (p *fakePlayer) SetTranspose(_ context.Context, n int) error {
	return p.call(fmt.Sprintf("transpose %d", n))
}
func (p *fakePlayer) SetTempo(_ context.Context, r float64) error {
	return p.call(fmt.Sprintf("tempo %.2f", r))
}
func (p *fakePlayer) SetVolume(_ context.Context, v float64) error {
	return p.call(fmt.Sprintf("volume %.2f", v))
}

type rig struct {
	t      *testing.T
	srv    *Server
	room   *feeder.Room
	player *fakePlayer
	http   *httptest.Server
	now    time.Time
}

// deterministic makes the random values predictable: a counter in hex.
func deterministic(t *testing.T) {
	n := 0
	old := randomHex
	randomHex = func(bytes int) string {
		n++
		return fmt.Sprintf("%0*x", bytes*2, n)
	}
	t.Cleanup(func() { randomHex = old })
}

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	deterministic(t)
	now := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	durations, err := feeder.OpenDurations("")
	require.NoError(t, err)
	_, _ = durations.Learn(1229, 200*time.Second)
	_, _ = durations.Learn(6541, 190*time.Second)
	cat := newFakeCatalog()
	lookup := func(n uint32) (fair.Song, bool) {
		s, err := cat.Song(context.Background(), int(n))
		return fair.Song{Number: n, Title: s.Title, Artist: s.Artist}, err == nil
	}
	room := feeder.NewRoom(fair.NewEvening(fair.Turns), lookup)
	player := &fakePlayer{state: feeder.State{Connected: true, Transport: feeder.Idle, TempoSteps: 0, Volume: 50}}
	srv := New(Options{
		Room: room, Player: player, Catalog: cat, Durations: durations,
		ID: "00112233aabbccdd", Port: DefaultPort, Secret: []byte("secret"), Evening: "0123456789abcdef",
		Version: "1.3.0", Config: cfg, Logger: slog.New(slog.DiscardHandler),
		Addrs: func() []net.IP { return []net.IP{net.IPv4(192, 168, 1, 42).To4()} },
		Now:   func() time.Time { return now },
	})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return &rig{t: t, srv: srv, room: room, player: player, http: hs, now: now}
}

// do sends a request and returns the status and the body.
func (r *rig) do(method, path, token string, body any) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(r.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.http.URL+"/api/v1"+path, rd)
	require.NoError(r.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(r.t, err)
	return resp.StatusCode, b
}

// ok sends a request that must succeed with status and decodes the answer.
func (r *rig) ok(status int, method, path, token string, body any, out any) []byte {
	r.t.Helper()
	got, b := r.do(method, path, token, body)
	require.Equal(r.t, status, got, "%s %s: %s", method, path, b)
	if out != nil {
		require.NoError(r.t, json.Unmarshal(b, out))
	}
	return b
}

func (r *rig) register(name string) singerIssuedDTO {
	var out singerIssuedDTO
	r.ok(201, "POST", "/singers", "", map[string]any{"device": "device-" + name, "name": name}, &out)
	return out
}

func (r *rig) ask(who singerIssuedDTO, number string) requestDTO {
	var out requestDTO
	r.ok(201, "POST", "/requests", "", map[string]any{"singer": who.Singer.ID, "token": who.Token, "number": number}, &out)
	return out
}

// golden compares b with testdata/golden/name, or rewrites it with -update.
func golden(t *testing.T, name string, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, b, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test -update to write it")
	assert.Equal(t, string(want), string(b), name)
}

// An evening with two singers in turns, Ana's first song playing, Beto's and
// a song with no singer queued, and Ana's second waiting.
func busyEvening(t *testing.T) (*rig, singerIssuedDTO, singerIssuedDTO) {
	r := newRig(t, Config{Name: "Living Room", Room: Request, Codes: Codes{Queue: "1111", Control: "2222", Host: "3333"}})
	ana := r.register("Ana")
	beto := r.register("Beto")
	r.ask(ana, "1001")
	r.ask(beto, "1229")
	r.ask(ana, "3656")
	r.room.Do(func(e *fair.Evening, now time.Time) {
		_, err := e.Add(fair.Song{Number: 6541, Title: "4 MINUTES", Artist: "MADONNA", DurationMS: 190_000})
		require.NoError(t, err)
	})
	var started fair.Entry
	r.room.Do(func(e *fair.Evening, now time.Time) { started, _ = e.Pop(now) })
	r.player.mu.Lock()
	r.player.state = feeder.State{
		Connected: true, Transport: feeder.Playing, Position: 42 * time.Second, Key: 1, TempoSteps: -1, Volume: 58,
		Song: &feeder.Song{Number: started.Number, Title: started.Title, Entry: &started},
	}
	r.player.mu.Unlock()
	return r, ana, beto
}

func TestGoldenResponses(t *testing.T) {
	r, ana, _ := busyEvening(t)
	host := r.login("3333")

	golden(t, "discover.json", r.ok(200, "GET", "/discover", "", nil, nil))
	golden(t, "access.json", r.ok(200, "GET", "/access", "", nil, nil))
	golden(t, "state_playing.json", r.ok(200, "GET", "/state", "", nil, nil))
	golden(t, "queue.json", r.ok(200, "GET", "/queue", "", nil, nil))
	golden(t, "singers.json", r.ok(200, "GET", "/singers", "", nil, nil))
	golden(t, "requests.json", r.ok(200, "GET", "/requests", "", nil, nil))
	golden(t, "packages.json", r.ok(200, "GET", "/packages", "", nil, nil))
	golden(t, "export.ndjson", r.ok(200, "GET", "/songs/export?limit=3", "", nil, nil))
	golden(t, "singer_issued.json", r.ok(201, "POST", "/singers", "", map[string]any{"device": "phone-c", "name": " Caio "}, nil))
	golden(t, "singer.json", r.ok(201, "POST", "/singers/hosted", host, map[string]any{"name": nil}, nil))
	golden(t, "request.json", r.ok(201, "POST", "/requests", "", map[string]any{"singer": ana.Singer.ID, "token": ana.Token, "number": "6541"}, nil))
	golden(t, "added_to_queue.json", r.ok(201, "POST", "/queue", r.login("1111"), map[string]any{"number": "19535"}, nil))
	golden(t, "settings.json", r.ok(200, "PUT", "/settings", r.login("1111"),
		map[string]any{"transpose": 2, "tempo_ratio": nil, "melody_enabled": nil, "music_volume": nil, "lyric_offset_ms": nil}, nil))
	golden(t, "login.json", r.ok(200, "POST", "/login", "", map[string]any{"password": "2222"}, nil))

	status, b := r.do("POST", "/requests", "", map[string]any{"singer": ana.Singer.ID, "token": "wrong", "number": "1001"})
	assert.Equal(t, 403, status)
	golden(t, "error_forbidden.json", b)
	status, b = r.do("POST", "/transport/skip", "", nil)
	assert.Equal(t, 401, status)
	golden(t, "error_unauthorized.json", b)

	r.player.mu.Lock()
	r.player.state = feeder.State{Connected: true, Transport: feeder.Idle, Volume: 100}
	r.player.mu.Unlock()
	golden(t, "state_idle.json", r.ok(200, "GET", "/state", "", nil, nil))
}

func (r *rig) login(code string) string {
	r.t.Helper()
	var g accessGrantDTO
	r.ok(200, "POST", "/login", "", map[string]any{"password": code}, &g)
	return g.Token
}

// The events, as the WebSocket sends them.
func TestGoldenEvents(t *testing.T) {
	r, _, _ := busyEvening(t)
	st := r.player.State()
	cases := []struct {
		name string
		ev   event
	}{
		{"event_state.json", event{"event": "state", "state": r.srv.stateDTO(context.Background())}},
		{"event_song_started.json", event{"event": "song_started", "now_playing": r.srv.nowPlaying(context.Background(), st.Song)}},
		{"event_song_ended.json", event{"event": "song_ended", "reason": "skipped"}},
		{"event_singers_changed.json", event{"event": "singers_changed", "singers": r.srv.singersDTO()}},
		{"event_settings_changed.json", event{"event": "settings_changed", "settings": r.srv.stateDTO(context.Background()).Settings}},
		{"event_desync.json", event{"event": "desync", "skipped": 3}},
	}
	q, rs := r.srv.queueAndRequests()
	cases = append(cases,
		struct {
			name string
			ev   event
		}{"event_queue_changed.json", event{"event": "queue_changed", "queue": q}},
		struct {
			name string
			ev   event
		}{"event_requests_changed.json", event{"event": "requests_changed", "requests": rs}},
	)
	// A song the evening did not hold is reported as a file.
	foreign := feeder.Song{Number: 19535, Title: "FLY ME TO THE MOON"}
	cases = append(cases, struct {
		name string
		ev   event
	}{"event_song_started_unqueued.json", event{"event": "song_started", "now_playing": r.srv.nowPlaying(context.Background(), &foreign)}})

	for _, c := range cases {
		b, err := json.Marshal(c.ev)
		require.NoError(t, err)
		golden(t, c.name, append(b, '\n'))
	}
}

// Every golden file names the Rust type it must parse as, for the check in
// testdata/kmcheck.
func TestGoldenFilesAreListed(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "golden", "types.txt"))
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		require.Len(t, f, 2, line)
		listed[f[0]] = true
	}
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json*"))
	require.NoError(t, err)
	var missing []string
	for _, f := range files {
		if !listed[filepath.Base(f)] {
			missing = append(missing, filepath.Base(f))
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing)
}
