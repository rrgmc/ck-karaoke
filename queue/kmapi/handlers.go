package kmapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
)

// apiError is an answer other than success: a status and an ErrorDto.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func notFound(what string) *apiError {
	return &apiError{http.StatusNotFound, "not_found", "no such " + what}
}
func badRequest(msg string) *apiError { return &apiError{http.StatusBadRequest, "bad_request", msg} }
func conflict(code, msg string) *apiError {
	return &apiError{http.StatusConflict, code, msg}
}

// mapError turns an error from the evening or the player into its answer.
func mapError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	codes := []struct {
		err  error
		code string
	}{
		{fair.ErrQueueFull, "queue_full"},
		{fair.ErrNameTaken, "name_taken"},
		{fair.ErrNameRequired, "name_required"},
		{fair.ErrUnnamedTaken, "unnamed_taken"},
		{fair.ErrDeviceFull, "device_full"},
		{fair.ErrSeatsFull, "singers_full"},
		{fair.ErrSingerFull, "singer_requests_full"},
		{fair.ErrRequestsFull, "requests_full"},
		{fair.ErrFinished, "request_finished"},
		{fair.ErrClosed, "requests_closed"},
		{fair.ErrNotWaiting, "request_not_waiting"},
	}
	for _, c := range codes {
		if errors.Is(err, c.err) {
			return conflict(c.code, err.Error())
		}
	}
	switch {
	case errors.Is(err, fair.ErrNameUnusable), errors.Is(err, fair.ErrDeviceUnusable):
		return badRequest(err.Error())
	case errors.Is(err, fair.ErrSeatNotFound), errors.Is(err, fair.ErrNoSinger):
		return notFound("singer")
	case errors.Is(err, fair.ErrRequestUnknown):
		return notFound("request")
	case errors.Is(err, fair.ErrWrongToken):
		return &apiError{http.StatusForbidden, "forbidden", err.Error()}
	case errors.Is(err, feeder.ErrOffline):
		return conflict("player_offline", err.Error())
	}
	return &apiError{http.StatusInternalServerError, "internal", err.Error()}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err *apiError) {
	if err.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="karaokemachine"`)
	}
	writeJSON(w, err.status, errorDTO{Error: err.code, Message: err.msg})
}

// handler is a route that answers a value or an error.
type handler func(r *http.Request) (int, any, error)

func (s *Server) serve(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, v, err := h(r)
		var re *retryError
		if errors.As(err, &re) {
			w.Header().Set("Retry-After", strconv.Itoa(int(re.wait.Seconds())+1))
			err = re.apiError
		}
		if err != nil {
			ae := mapError(err)
			if ae.status >= 500 {
				s.log.Error("api", "path", r.URL.Path, "error", err)
			}
			writeError(w, ae)
			return
		}
		writeJSON(w, status, v)
	}
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler { return s.authorize(s.routes()) }

// TrustedHandler returns the API's routes with no access check, for a page
// served on loopback to the operator.
func (s *Server) TrustedHandler() http.Handler { return s.routes() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern string, h handler) { mux.Handle(pattern, s.serve(h)) }

	route("GET /api/v1/discover", s.discover)
	route("GET /api/v1/access", s.access)
	route("POST /api/v1/login", s.login)
	route("GET /api/v1/state", func(r *http.Request) (int, any, error) { return 200, s.stateDTO(r.Context()), nil })
	route("GET /api/v1/settings", func(r *http.Request) (int, any, error) { return 200, s.stateDTO(r.Context()).Settings, nil })
	route("PUT /api/v1/settings", s.putSettings)
	route("GET /api/v1/packages", s.packages)
	mux.HandleFunc("GET /api/v1/songs/export", s.export)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)

	route("GET /api/v1/queue", s.getQueue)
	route("POST /api/v1/queue", s.addToQueue)
	route("DELETE /api/v1/queue", s.clearQueue)
	route("DELETE /api/v1/queue/{id}", s.removeEntry)
	route("POST /api/v1/queue/{id}/move", s.moveEntry)

	route("GET /api/v1/singers", func(*http.Request) (int, any, error) { return 200, s.singersDTO(), nil })
	route("POST /api/v1/singers", s.registerSinger)
	route("POST /api/v1/singers/hosted", s.createSinger)
	route("PUT /api/v1/singers/{id}/name", s.renameSinger)
	route("POST /api/v1/singers/{id}/reissue", s.reissueSinger)
	route("DELETE /api/v1/singers/{id}", s.removeSinger)
	route("POST /api/v1/singers/{id}/merge", s.mergeSinger)
	route("POST /api/v1/singers/{id}/requests", s.requestForSinger)

	route("GET /api/v1/requests", s.getRequests)
	route("POST /api/v1/requests", s.addRequest)
	route("POST /api/v1/requests/{id}/withdraw", s.withdraw)
	route("POST /api/v1/requests/{id}/decline", s.decline)
	route("POST /api/v1/requests/{id}/accept", s.accept)
	route("PUT /api/v1/requests/accepting", s.setAccepting)
	route("POST /api/v1/evening/reset", s.resetEvening)

	for _, verb := range []string{"play", "pause", "skip", "restart", "stop"} {
		route("POST /api/v1/transport/"+verb, s.transport(verb))
	}
	route("POST /api/v1/demo/start", func(*http.Request) (int, any, error) {
		return 0, nil, conflict("unavailable", "this machine has no demo")
	})
	route("/api/", func(r *http.Request) (int, any, error) {
		return 0, nil, &apiError{http.StatusNotFound, "unknown_endpoint", "no endpoint " + r.URL.Path}
	})
	return mux
}

// authorize lets a request through when the room level, or the bearer token's,
// is enough for its route. No token, or one that does not verify, is 401; a
// token whose level is too low is 403.
func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, "/api/v1")
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		needed := requiredLevel(r.Method, path)
		room, _ := s.auth.snapshot()
		if needed <= room {
			next.ServeHTTP(w, r)
			return
		}
		token := bearer(r)
		if token == "" {
			writeError(w, &apiError{http.StatusUnauthorized, "unauthorized",
				fmt.Sprintf("'%s' needs the %s level; exchange a code at /api/v1/login", path, needed)})
			return
		}
		held, ok := s.auth.levelOf(token)
		switch {
		case !ok:
			writeError(w, &apiError{http.StatusUnauthorized, "unauthorized", "that token is not valid; log in again"})
		case held < needed:
			writeError(w, &apiError{http.StatusForbidden, "forbidden",
				fmt.Sprintf("'%s' needs the %s level, and this token opens %s", path, needed, held)})
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// decode reads a JSON body. A missing body reads as an empty object.
func decode(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return badRequest(err.Error())
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		b = []byte("{}")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return badRequest("unreadable body: " + err.Error())
	}
	return nil
}

func pathID(r *http.Request) (uint64, error) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, badRequest("not an id: " + r.PathValue("id"))
	}
	return id, nil
}

// Discovery, access and the catalogue.

func (s *Server) discover(r *http.Request) (int, any, error) {
	cfg := s.config()
	info := s.opts.Catalog.Info()
	version := s.catalogVersion()
	d := discoveryDTO{
		V: 1, ID: s.opts.ID, Name: cfg.Name, App: App, Version: s.opts.Version, API: "/api/v1",
		Port: uint16(s.opts.Port), URLs: []string{}, SongCount: &info.Songs, CatalogVersion: &version,
		// No demo, no song book, and the root page only says what this is.
		Features: featuresDTO{},
	}
	for _, ip := range s.opts.Addrs() {
		d.URLs = append(d.URLs, fmt.Sprintf("http://%s:%d", ip, s.opts.Port))
	}
	d.Reachable = len(d.URLs) > 0
	if !d.Reachable {
		d.Problem = &struct {
			Kind string `json:"kind"`
		}{"no_network"}
	}
	return 200, d, nil
}

func (s *Server) access(r *http.Request) (int, any, error) {
	room, codes := s.auth.snapshot()
	return 200, accessDTO{
		Access: s.auth.callerLevel(bearer(r)), Room: room,
		QueueCode: codes.Queue != "", ControlCode: codes.Control != "", HostCode: codes.Host != "",
	}, nil
}

func (s *Server) login(r *http.Request) (int, any, error) {
	var req loginRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	addr, _, _ := net.SplitHostPort(r.RemoteAddr)
	level, token, wait, err := s.auth.login(addr, req.Password)
	switch {
	case errors.Is(err, errTooManyTrys):
		return 0, nil, &retryError{&apiError{http.StatusTooManyRequests, "too_many_requests", err.Error()}, wait}
	case errors.Is(err, errNoCodes):
		return 0, nil, notFound("access code")
	case err != nil:
		return 0, nil, &apiError{http.StatusUnauthorized, "unauthorized", err.Error()}
	}
	s.log.Info("a remote logged in", "level", level, "from", addr)
	return 200, accessGrantDTO{Token: token, ExpiresInSecs: uint64(TokenTTL.Seconds()), Access: level}, nil
}

// retryError is a 429 that says when to try again.
type retryError struct {
	apiError *apiError
	wait     time.Duration
}

func (e *retryError) Error() string { return e.apiError.msg }

func (s *Server) packages(r *http.Request) (int, any, error) {
	info := s.opts.Catalog.Info()
	name := info.ProjectName
	if name == "" {
		name = "Karaoke unit"
	}
	return 200, packagesDTO{
		Packages: []packageDTO{{
			ID: PackageID, Name: name, Version: strconv.Itoa(info.DBVersion), SongCount: info.Songs,
			InstalledAt: "", FlagNames: []string{},
		}},
		SongCount: info.Songs,
	}, nil
}

// export serves the catalogue as NDJSON, one song per line, paged by number.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	limit := 1000
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeError(w, badRequest("limit is not a number"))
			return
		}
		limit = int(min(max(n, 1), 5000))
	}
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		var n Number
		if err := n.UnmarshalJSON([]byte(strconv.Quote(strings.TrimSpace(v)))); err != nil {
			writeError(w, badRequest("after is not a song number"))
			return
		}
		after = int(n)
	}
	songs, err := s.opts.Catalog.SongsAfter(r.Context(), after, limit)
	if err != nil {
		writeError(w, mapError(err))
		return
	}
	version := strconv.FormatUint(s.catalogVersion(), 10)
	var b strings.Builder
	for _, c := range songs {
		line, _ := json.Marshal(s.songOf(c))
		b.Write(line)
		b.WriteByte('\n')
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("ETag", `"`+version+`"`)
	w.Header().Set("X-Km-Catalog-Version", version)
	_, _ = io.WriteString(w, b.String())
}

// The queue.

func (s *Server) getQueue(*http.Request) (int, any, error) {
	q, _ := s.queueAndRequests()
	return 200, q, nil
}

// song describes a requested number from the catalogue, or answers 404.
func (s *Server) song(ctx context.Context, n Number) (fair.RequestedSong, error) {
	c, err := s.opts.Catalog.Song(ctx, int(n))
	if err != nil {
		return fair.RequestedSong{}, notFound("song")
	}
	return fair.RequestedSong{Number: uint32(n), Title: c.Title, Artist: c.Artist,
		DurationMS: s.opts.Durations.Get(uint32(n))}, nil
}

func (s *Server) addToQueue(r *http.Request) (int, any, error) {
	var req addToQueueRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	song, err := s.song(r.Context(), req.Number)
	if err != nil {
		return 0, nil, err
	}
	var out addedToQueueDTO
	s.opts.Room.Do(func(e *fair.Evening, _ time.Time) {
		var id uint64
		if id, err = e.Add(fair.Song{Number: song.Number, Title: song.Title, Artist: song.Artist,
			DurationMS: song.DurationMS}); err == nil {
			out = addedToQueueDTO{EntryID: id, Position: e.Queue().PositionOf(id), Title: song.Title,
				Artist: optString(song.Artist)}
		}
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, out, nil
}

func (s *Server) clearQueue(*http.Request) (int, any, error) {
	var q queueDTO
	s.opts.Room.Do(func(e *fair.Evening, now time.Time) {
		e.Clear(now)
		q = queueOf(e)
	})
	return 200, q, nil
}

func (s *Server) removeEntry(r *http.Request) (int, any, error) {
	id, err := pathID(r)
	if err != nil {
		return 0, nil, err
	}
	var q queueDTO
	found := false
	s.opts.Room.Do(func(e *fair.Evening, now time.Time) {
		_, found = e.Remove(id, now)
		q = queueOf(e)
	})
	if !found {
		return 0, nil, notFound("queue entry")
	}
	return 200, q, nil
}

func (s *Server) moveEntry(r *http.Request) (int, any, error) {
	id, err := pathID(r)
	if err != nil {
		return 0, nil, err
	}
	var req moveRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	var q queueDTO
	found := false
	s.opts.Room.Do(func(e *fair.Evening, _ time.Time) {
		found = e.MoveTo(id, req.ToIndex)
		q = queueOf(e)
	})
	if !found {
		return 0, nil, notFound("queue entry")
	}
	return 200, q, nil
}

// Singers.

func (s *Server) registerSinger(r *http.Request) (int, any, error) {
	var req registerSingerRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	var name *fair.SeatName
	if req.Name != nil {
		if n, ok := fair.NameOf(*req.Name); ok {
			name = &n
		}
	}
	cfg := s.config()
	var issued fair.Issued
	var err error
	s.opts.Room.Do(func(e *fair.Evening, _ time.Time) {
		issued, err = e.Register(strings.TrimSpace(req.Device), name, randomHex(16), cfg.Seats)
	})
	if err != nil {
		return 0, nil, err
	}
	s.log.Info("a singer registered", "singer", issued.Seat.ID)
	s.publishSingers()
	return http.StatusCreated, singerIssuedDTO{Singer: singerOf(issued.Seat), Token: issued.Token, Evening: s.Evening()}, nil
}

func (s *Server) createSinger(r *http.Request) (int, any, error) {
	var req createSingerRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	var name *fair.SeatName
	if req.Name != nil {
		if n, ok := fair.NameOf(*req.Name); ok {
			name = &n
		}
	}
	var seat fair.Seat
	var err error
	s.opts.Room.Do(func(e *fair.Evening, _ time.Time) { seat, err = e.CreateSinger(name) })
	if err != nil {
		return 0, nil, err
	}
	s.publishSingers()
	return http.StatusCreated, singerOf(seat), nil
}

func (s *Server) renameSinger(r *http.Request) (int, any, error) {
	id, err := pathID(r)
	if err != nil {
		return 0, nil, err
	}
	var req renameSingerRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	name, ok := fair.NameOf(req.Name)
	if !ok {
		return 0, nil, badRequest("a singer's name cannot be empty")
	}
	var seat fair.Seat
	s.opts.Room.Do(func(e *fair.Evening, _ time.Time) { seat, err = e.Rename(id, req.Token, name) })
	if err != nil {
		return 0, nil, err
	}
	s.publishSingers()
	return 200, singerOf(seat), nil
}

func (s *Server) reissueSinger(r *http.Request) (int, any, error) {
	return s.seatAction(r, func(e *fair.Evening, id uint64, _ time.Time) (fair.Seat, error) { return e.OpenSeat(id) })
}

func (s *Server) removeSinger(r *http.Request) (int, any, error) {
	return s.seatAction(r, func(e *fair.Evening, id uint64, now time.Time) (fair.Seat, error) {
		return e.RemoveSinger(id, now)
	})
}

func (s *Server) mergeSinger(r *http.Request) (int, any, error) {
	var req mergeSingerRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	return s.seatAction(r, func(e *fair.Evening, id uint64, _ time.Time) (fair.Seat, error) {
		return e.Merge(id, req.Into)
	})
}

func (s *Server) seatAction(r *http.Request, fn func(*fair.Evening, uint64, time.Time) (fair.Seat, error)) (int, any, error) {
	id, err := pathID(r)
	if err != nil {
		return 0, nil, err
	}
	var seat fair.Seat
	s.opts.Room.Do(func(e *fair.Evening, now time.Time) { seat, err = fn(e, id, now) })
	if err != nil {
		return 0, nil, err
	}
	s.publishSingers()
	return 200, singerOf(seat), nil
}

// Requests.

func (s *Server) getRequests(*http.Request) (int, any, error) {
	_, rs := s.queueAndRequests()
	return 200, rs, nil
}

func (s *Server) ask(ctx context.Context, seat uint64, proof fair.Proof, n Number) (int, any, error) {
	song, err := s.song(ctx, n)
	if err != nil {
		return 0, nil, err
	}
	rules := s.config().Rules
	var out requestDTO
	s.opts.Room.Do(func(e *fair.Evening, now time.Time) {
		var req fair.Request
		if req, err = e.Request(seat, proof, song, rules, now); err == nil {
			out = requestOf(e, req)
		}
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, out, nil
}

func (s *Server) addRequest(r *http.Request) (int, any, error) {
	var req addRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	return s.ask(r.Context(), req.Singer, fair.ByToken(req.Token), req.Number)
}

func (s *Server) requestForSinger(r *http.Request) (int, any, error) {
	id, err := pathID(r)
	if err != nil {
		return 0, nil, err
	}
	var req addSingerRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	return s.ask(r.Context(), id, fair.ByLevel, req.Number)
}

func (s *Server) requestAction(r *http.Request, fn func(*fair.Evening, uint64, time.Time) (fair.Request, error)) (int, any, error) {
	id, err := pathID(r)
	if err != nil {
		return 0, nil, err
	}
	var out requestDTO
	s.opts.Room.Do(func(e *fair.Evening, now time.Time) {
		var req fair.Request
		if req, err = fn(e, id, now); err == nil {
			out = requestOf(e, req)
		}
	})
	if err != nil {
		return 0, nil, err
	}
	return 200, out, nil
}

func (s *Server) withdraw(r *http.Request) (int, any, error) {
	var req withdrawRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	return s.requestAction(r, func(e *fair.Evening, id uint64, now time.Time) (fair.Request, error) {
		return e.Withdraw(id, req.Token, now)
	})
}

func (s *Server) decline(r *http.Request) (int, any, error) {
	return s.requestAction(r, func(e *fair.Evening, id uint64, now time.Time) (fair.Request, error) {
		return e.Decline(id, now)
	})
}

func (s *Server) accept(r *http.Request) (int, any, error) {
	var req acceptRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	return s.requestAction(r, func(e *fair.Evening, id uint64, now time.Time) (fair.Request, error) {
		return e.Accept(id, req.Next, now)
	})
}

func (s *Server) setAccepting(r *http.Request) (int, any, error) {
	var req acceptingRequest
	if err := decode(r, &req); err != nil {
		return 0, nil, err
	}
	s.opts.Room.Do(func(e *fair.Evening, _ time.Time) { e.SetAccepting(req.Accepting) })
	_, rs := s.queueAndRequests()
	return 200, rs, nil
}

func (s *Server) resetEvening(*http.Request) (int, any, error) {
	s.ResetEvening()
	_, rs := s.queueAndRequests()
	return 200, rs, nil
}

// ResetEvening starts a new evening: no queue, no requests, no record, no
// seats, and a new word, so phones drop the seats they kept.
func (s *Server) ResetEvening() {
	s.mu.Lock()
	s.evening = randomHex(8)
	s.waitingForHost = false
	s.mu.Unlock()
	s.opts.Room.Do(func(e *fair.Evening, _ time.Time) { e.Reset() })
	s.publishSingers()
}

// Playback.

func (s *Server) transport(verb string) handler {
	return func(r *http.Request) (int, any, error) {
		ctx := r.Context()
		st := s.opts.Player.State()
		if !st.Connected {
			return 0, nil, conflict("player_offline", "the karaoke unit is not connected")
		}
		var err error
		switch verb {
		case "play":
			queued := 0
			s.opts.Room.View(func(e *fair.Evening, _ time.Time) { queued = e.Queue().Len() })
			if st.Song == nil && queued == 0 && !st.Held {
				return 0, nil, conflict("nothing_queued", "nothing is queued")
			}
			s.mu.Lock()
			s.waitingForHost = false
			s.mu.Unlock()
			err = s.opts.Player.Play(ctx)
			s.publishQueue()
		case "pause":
			if st.Song == nil {
				return 0, nil, conflict("nothing_playing", "nothing is playing")
			}
			err = s.opts.Player.Pause(ctx)
		case "skip":
			if st.Song == nil {
				return 0, nil, conflict("nothing_playing", "nothing is playing")
			}
			s.setLastCommand("skip")
			err = s.opts.Player.Skip(ctx)
		case "restart":
			if st.Song == nil {
				return 0, nil, conflict("nothing_loaded", "nothing is loaded")
			}
			s.setLastCommand("restart")
			err = s.opts.Player.Restart(ctx)
		case "stop":
			s.setLastCommand("stop")
			err = s.opts.Player.Stop(ctx)
		}
		if err != nil {
			return 0, nil, err
		}
		return 200, s.stateDTO(ctx), nil
	}
}

func (s *Server) setLastCommand(c string) {
	s.mu.Lock()
	s.lastCommand = c
	s.mu.Unlock()
}

func (s *Server) putSettings(r *http.Request) (int, any, error) {
	var p settingsPatchDTO
	if err := decode(r, &p); err != nil {
		return 0, nil, err
	}
	switch {
	case p.Transpose != nil && (*p.Transpose < -6 || *p.Transpose > 6):
		return 0, nil, badRequest("transpose goes from -6 to 6")
	case p.TempoRatio != nil && (*p.TempoRatio < 0.75 || *p.TempoRatio > 1.25):
		return 0, nil, badRequest("tempo_ratio goes from 0.75 to 1.25")
	case p.MusicVolume != nil && (*p.MusicVolume < 0 || *p.MusicVolume > 1):
		return 0, nil, badRequest("music_volume goes from 0 to 1")
	case p.MelodyEnabled != nil:
		// The unit has a melody toggle but reports no melody state, so a
		// setting could not be kept true.
		return 0, nil, conflict("no_melody_midi", "this machine cannot switch the melody")
	}
	ctx := r.Context()
	var err error
	if p.Transpose != nil {
		err = s.opts.Player.SetTranspose(ctx, *p.Transpose)
	}
	if err == nil && p.TempoRatio != nil {
		err = s.opts.Player.SetTempo(ctx, *p.TempoRatio)
	}
	if err == nil && p.MusicVolume != nil {
		err = s.opts.Player.SetVolume(ctx, *p.MusicVolume)
	}
	if err != nil {
		return 0, nil, err
	}
	settings := s.expectSettings(p)
	s.hub.publish(event{"event": "settings_changed", "settings": settings})
	return 200, settings, nil
}

// expectSettings is the settings as the patch leaves them. The unit reports
// the change in its next status, up to two seconds later.
func (s *Server) expectSettings(p settingsPatchDTO) settingsDTO {
	cur := s.stateDTO(context.Background()).Settings
	if p.Transpose != nil {
		cur.Transpose = *p.Transpose
	}
	if p.TempoRatio != nil {
		cur.TempoRatio = feeder.TempoRatio(feeder.TempoSteps(*p.TempoRatio))
	}
	if p.MusicVolume != nil {
		cur.MusicVolume = float64(feeder.VolumeLevel(*p.MusicVolume)) / 100
	}
	return cur
}
