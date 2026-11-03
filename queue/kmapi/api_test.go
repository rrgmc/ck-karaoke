package kmapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/feeder"
)

func TestAccessLevels(t *testing.T) {
	r := newRig(t, Config{Room: Request, Codes: Codes{Queue: "1111", Control: "2222", Host: "3333"}})

	var a accessDTO
	r.ok(200, "GET", "/access", "", nil, &a)
	assert.Equal(t, accessDTO{Access: Request, Room: Request, QueueCode: true, ControlCode: true, HostCode: true}, a)

	tests := []struct {
		name   string
		method string
		path   string
		token  string
		body   any
		status int
	}{
		{"the room may register a singer", "POST", "/singers", "", map[string]any{"device": "d", "name": "Ana"}, 201},
		{"a plain add needs the queue level", "POST", "/queue", "", map[string]any{"number": "1001"}, 401},
		{"a transport command needs control", "POST", "/transport/stop", "", nil, 401},
		{"a bad token is refused", "POST", "/transport/stop", "v1.control.1.2.3", nil, 401},
		{"a queue token is not enough for control", "POST", "/transport/stop", r.login("1111"), nil, 403},
		{"a control token is", "POST", "/transport/stop", r.login("2222"), nil, 200},
		{"the Host tab needs host", "POST", "/evening/reset", r.login("2222"), nil, 403},
		{"a host token opens it", "POST", "/evening/reset", r.login("3333"), nil, 200},
		{"reading is open", "GET", "/queue", "", nil, 200},
		{"the event stream needs no token", "GET", "/events", "", nil, 426}, // reached the handler: not a handshake
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, b := r.do(tt.method, tt.path, tt.token, tt.body)
			assert.Equal(t, tt.status, status, string(b))
			if status >= 400 && tt.path != "/events" {
				var e errorDTO
				require.NoError(t, json.Unmarshal(b, &e))
				assert.NotEmpty(t, e.Error)
			}
		})
	}

	r.ok(200, "GET", "/access", r.login("3333"), nil, &a)
	assert.Equal(t, Host, a.Access, "a token raises the caller's own level")
	r.ok(200, "GET", "/access", "v1.host.1.2.3", nil, &a)
	assert.Equal(t, Request, a.Access, "a stale token reads as the room level")
}

func TestLogin(t *testing.T) {
	r := newRig(t, Config{Room: Request, Codes: Codes{Control: "2222"}})
	var g accessGrantDTO
	r.ok(200, "POST", "/login", "", map[string]any{"password": " 2222 "}, &g)
	assert.Equal(t, Control, g.Access)
	assert.Equal(t, uint64(12*60*60), g.ExpiresInSecs)

	for range maxFailedLogins {
		status, _ := r.do("POST", "/login", "", map[string]any{"password": "nope"})
		assert.Equal(t, 401, status)
	}
	status, _ := r.do("POST", "/login", "", map[string]any{"password": "2222"})
	assert.Equal(t, 429, status, "too many failures, even with the right code")

	r.srv.Configure(Config{Room: Request, Codes: Codes{Control: "9999"}})
	status, _ = r.do("POST", "/transport/stop", g.Token, nil)
	assert.Equal(t, 401, status, "a changed code ends that level's tokens")

	none := newRig(t, Config{Room: Control})
	status, _ = none.do("POST", "/login", "", map[string]any{"password": "x"})
	assert.Equal(t, 404, status, "no code is set")
}

func TestExportPagesByNumber(t *testing.T) {
	r := newRig(t, Config{Room: Request})
	var numbers []string
	after := ""
	for {
		path := "/songs/export?limit=2"
		if after != "" {
			path += "&after=" + after
		}
		req, err := http.NewRequest("GET", r.http.URL+"/api/v1"+path, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, "application/x-ndjson", resp.Header.Get("Content-Type"))
		version := resp.Header.Get("X-Km-Catalog-Version")
		assert.Equal(t, `"`+version+`"`, resp.Header.Get("ETag"))
		sc := bufio.NewScanner(resp.Body)
		n := 0
		for sc.Scan() {
			var s songDTO
			require.NoError(t, json.Unmarshal(sc.Bytes(), &s))
			numbers = append(numbers, strings.Trim(string(must(json.Marshal(s.Number))), `"`))
			n++
		}
		resp.Body.Close()
		if n == 0 {
			break
		}
		after = numbers[len(numbers)-1]
	}
	assert.Equal(t, []string{"1001", "1229", "3656", "6541", "19535"}, numbers)

	status, _ := r.do("GET", "/songs/export?after=abc", "", nil)
	assert.Equal(t, 400, status)

	var d discoveryDTO
	r.ok(200, "GET", "/discover", "", nil, &d)
	_, _ = r.srv.opts.Durations.Learn(1001, time.Minute)
	var d2 discoveryDTO
	r.ok(200, "GET", "/discover", "", nil, &d2)
	assert.NotEqual(t, *d.CatalogVersion, *d2.CatalogVersion, "a learned length changes the version")
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// A Turns evening through the API, with the error codes KM Remote words.
func TestRequestsThroughTheAPI(t *testing.T) {
	r := newRig(t, Config{Room: Request, Codes: Codes{Host: "3333"}})
	ana := r.register("Ana")
	beto := r.register("Beto")
	assert.Equal(t, "0123456789abcdef", ana.Evening)

	first := r.ask(ana, "1001")
	assert.Equal(t, "queued", first.State)
	require.NotNil(t, first.Position)
	second := r.ask(ana, "3656")
	assert.Equal(t, "waiting", second.State)
	assert.Nil(t, second.EntryID)
	r.ask(beto, "6541")

	var q queueDTO
	r.ok(200, "GET", "/queue", "", nil, &q)
	require.Len(t, q.Entries, 2)
	assert.Equal(t, Number(1001), q.Entries[0].Number)
	assert.Equal(t, Number(6541), q.Entries[1].Number, "Beto's turn comes before Ana's second")

	status, b := r.do("POST", "/singers", "", map[string]any{"device": "other", "name": " ana "})
	assert.Equal(t, 409, status)
	assert.Contains(t, string(b), `"name_taken"`)

	status, _ = r.do("POST", "/requests", "", map[string]any{"singer": ana.Singer.ID, "token": ana.Token, "number": "4242"})
	assert.Equal(t, 404, status, "no such song")

	status, b = r.do("POST", "/requests", "", map[string]any{"singer": ana.Singer.ID, "token": ana.Token, "number": 1001})
	assert.Equal(t, 400, status, "a number is a string: %s", b)

	host := r.login("3333")
	var accepted requestDTO
	r.ok(200, "POST", "/requests/"+itoa(second.ID)+"/accept", host, map[string]any{"next": true}, &accepted)
	assert.Equal(t, "queued", accepted.State)
	require.NotNil(t, accepted.Position)
	assert.Equal(t, 0, *accepted.Position)

	var rs requestsDTO
	r.ok(200, "POST", "/evening/reset", host, nil, &rs)
	assert.Empty(t, rs.Requests)
	var singers singersDTO
	r.ok(200, "GET", "/singers", "", nil, &singers)
	assert.Empty(t, singers.Singers)
	assert.NotEqual(t, ana.Evening, singers.Evening, "a new evening has a new word")
}

func itoa(n uint64) string { return string(must(json.Marshal(n))) }

func TestTransport(t *testing.T) {
	r := newRig(t, Config{Room: Control})
	tests := []struct {
		verb string
		code string
	}{{"play", "nothing_queued"}, {"pause", "nothing_playing"}, {"skip", "nothing_playing"}, {"restart", "nothing_loaded"}}
	for _, tt := range tests {
		status, b := r.do("POST", "/transport/"+tt.verb, "", nil)
		assert.Equal(t, 409, status)
		assert.Contains(t, string(b), `"`+tt.code+`"`)
	}
	r.ok(200, "POST", "/transport/stop", "", nil, nil)

	r.player.mu.Lock()
	r.player.state.Transport, r.player.state.Song = feeder.Playing, &feeder.Song{Number: 1001, Title: "X"}
	r.player.mu.Unlock()
	for _, verb := range []string{"pause", "play", "skip", "restart"} {
		r.ok(200, "POST", "/transport/"+verb, "", nil, nil)
	}
	assert.Equal(t, []string{"stop", "pause", "play", "skip", "restart"}, r.player.Calls())

	r.ok(200, "PUT", "/settings", "", map[string]any{"transpose": -2, "tempo_ratio": nil, "melody_enabled": nil,
		"music_volume": 0.5, "lyric_offset_ms": nil}, nil)
	status, b := r.do("PUT", "/settings", "", map[string]any{"melody_enabled": true})
	assert.Equal(t, 409, status)
	assert.Contains(t, string(b), "no_melody_midi")
	status, _ = r.do("PUT", "/settings", "", map[string]any{"transpose": 7})
	assert.Equal(t, 400, status)
	assert.Contains(t, r.player.Calls(), "transpose -2")
	assert.Contains(t, r.player.Calls(), "volume 0.50")

	status, b = r.do("POST", "/demo/start", "", nil)
	assert.Equal(t, 409, status, string(b))
	status, b = r.do("GET", "/nothing/here", "", nil)
	assert.Equal(t, 404, status)
	assert.Contains(t, string(b), "unknown_endpoint")
}

// The stream sends the state at once, then the state on its ticker and the
// queue and requests after each change.
func TestEventStream(t *testing.T) {
	r := newRig(t, Config{Room: Request})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.srv.Run(ctx)
	}()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.http.URL, "http")+"/api/v1/events", nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	read := func() map[string]any {
		rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
		defer rcancel()
		_, b, err := conn.Read(rctx)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(b, &m))
		return m
	}
	assert.Equal(t, "state", read()["event"], "the state goes out at once")
	start := time.Now()
	assert.Equal(t, "state", read()["event"])
	assert.Less(t, time.Since(start), time.Second, "and every quarter second")

	r.register("Ana")
	seen := map[string]bool{}
	for i := 0; i < 20 && !(seen["queue_changed"] && seen["requests_changed"] && seen["singers_changed"]); i++ {
		seen[read()["event"].(string)] = true
	}
	assert.True(t, seen["singers_changed"])
	assert.True(t, seen["queue_changed"])
	assert.True(t, seen["requests_changed"])

	cancel()
	<-done
}
