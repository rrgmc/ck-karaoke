// Package kmapi serves KaraokeMachine's HTTP API in front of the queue
// manager, so that KM Remote connects to it unchanged.
//
// The contract is KaraokeMachine's (crates/machine/km-api): plain HTTP and JSON
// under /api/v1, one server-to-client WebSocket at /api/v1/events, and an mDNS
// announcement as _karaokemachine._tcp. The unit's catalogue maps onto it as
// one package of MIDI songs whose numbers are the unit's song ids.
package kmapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
)

// DefaultPort is KaraokeMachine's port. The iOS remote finds a machine by
// sweeping the network on this port alone.
const DefaultPort = 8177

// App is what /discover names the application. KM Remote accepts nothing else.
const App = "karaokemachine"

// PackageID is the one package the unit's catalogue is reported as.
const PackageID = "ck"

// StateInterval is how often the state event goes out. KM Remote reconnects
// after five seconds without a frame.
const StateInterval = 250 * time.Millisecond

var errNotNumber = errors.New("not a song number")

// Player is the unit, as the feeder drives it.
type Player interface {
	State() feeder.State
	Play(ctx context.Context) error
	Pause(ctx context.Context) error
	Skip(ctx context.Context) error
	Restart(ctx context.Context) error
	Stop(ctx context.Context) error
	SetTranspose(ctx context.Context, semitones int) error
	SetTempo(ctx context.Context, ratio float64) error
	SetVolume(ctx context.Context, level float64) error
}

// Catalog is the unit's song catalogue. *catalog.DB implements it.
type Catalog interface {
	Song(ctx context.Context, id int) (catalog.Song, error)
	SongsAfter(ctx context.Context, after, limit int) ([]catalog.Song, error)
	Info() catalog.Info
}

// Config is what an operator changes while the server runs.
type Config struct {
	// Name is what remotes list the server as.
	Name string
	// Room is the level everybody has without a code.
	Room  Level
	Codes Codes
	Seats fair.SeatRules
	Rules fair.RequestRules
}

// Options configures a Server.
type Options struct {
	Room      *feeder.Room
	Player    Player
	Catalog   Catalog
	Durations *feeder.Durations
	// ID names this server to remotes: 16 hex characters, kept across
	// restarts.
	ID string
	// Port is the port the API is served on, for /discover and mDNS.
	Port int
	// Secret keys the access tokens. Tokens survive a restart while it does.
	Secret []byte
	// Evening is the evening's word. Empty makes a new one.
	Evening string
	// WaitingForHost reports a kept evening that starts no song until a host
	// presses Play.
	WaitingForHost bool
	Version        string
	Config         Config
	Logger         *slog.Logger
	// Addrs lists this computer's LAN addresses. Defaults to LANAddrs.
	Addrs func() []net.IP
	Now   func() time.Time
}

// Server serves the API.
type Server struct {
	opts Options
	log  *slog.Logger
	auth *auth
	hub  *hub

	mu             sync.Mutex
	cfg            Config
	evening        string
	waitingForHost bool
	lastCommand    string // the transport command a song end may be answering
	songCache      catalog.Song
}

// New returns a server. Call Run to start its events, and serve Handler.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Addrs == nil {
		opts.Addrs = LANAddrs
	}
	if opts.Durations == nil {
		opts.Durations, _ = feeder.OpenDurations("")
	}
	if opts.Port == 0 {
		opts.Port = DefaultPort
	}
	if opts.Evening == "" {
		opts.Evening = randomHex(8)
	}
	if len(opts.Secret) == 0 {
		opts.Secret = []byte(randomHex(32))
	}
	if opts.Config.Rules == (fair.RequestRules{}) {
		opts.Config.Rules = fair.DefaultRequestRules
	}
	s := &Server{
		opts:           opts,
		log:            opts.Logger,
		auth:           newAuth(opts.Secret, opts.Config.Room, opts.Config.Codes, opts.Now),
		hub:            newHub(),
		cfg:            opts.Config,
		evening:        opts.Evening,
		waitingForHost: opts.WaitingForHost,
	}
	return s
}

// Configure applies a new configuration.
func (s *Server) Configure(cfg Config) {
	if cfg.Rules == (fair.RequestRules{}) {
		cfg.Rules = fair.DefaultRequestRules
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.auth.set(cfg.Room, cfg.Codes)
}

func (s *Server) config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Evening returns the evening's word.
func (s *Server) Evening() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evening
}

// WaitingForHost reports whether a kept evening still waits for a host.
func (s *Server) WaitingForHost() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitingForHost
}

// Run sends the state event every StateInterval and the queue and request
// events after each change of the evening, until ctx is cancelled. It then
// closes every event stream.
func (s *Server) Run(ctx context.Context) {
	changed, unsubscribe := s.opts.Room.Subscribe()
	defer unsubscribe()
	defer s.hub.close()
	tick := time.NewTicker(StateInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.hub.publish(event{"event": "state", "state": s.stateDTO(ctx)})
		case <-changed:
			s.publishQueue()
		}
	}
}

// FeederEvent passes on what the feeder reports. Wire it to
// feeder.Options.OnEvent.
func (s *Server) FeederEvent(e feeder.Event) {
	switch e := e.(type) {
	case feeder.SongStarted:
		if np := s.nowPlaying(context.Background(), &e.Song); np != nil {
			s.hub.publish(event{"event": "song_started", "now_playing": np})
		}
	case feeder.SongEnded:
		s.mu.Lock()
		reason := "finished"
		if !e.Natural {
			switch s.lastCommand {
			case "stop":
				reason = "stopped"
			default:
				reason = "skipped"
			}
		}
		s.lastCommand = ""
		s.mu.Unlock()
		if e.Natural {
			if took, err := s.opts.Durations.Learn(e.Song.Number, e.Length); err != nil {
				s.log.Warn("keeping a song's length", "number", e.Song.Number, "error", err)
			} else if took {
				s.log.Debug("learned a song's length", "number", e.Song.Number, "length", e.Length)
			}
		}
		s.hub.publish(event{"event": "song_ended", "reason": reason})
	}
}

func (s *Server) publishQueue() {
	q, r := s.queueAndRequests()
	s.hub.publish(event{"event": "queue_changed", "queue": q})
	s.hub.publish(event{"event": "requests_changed", "requests": r})
}

func (s *Server) publishSingers() {
	s.hub.publish(event{"event": "singers_changed", "singers": s.singersDTO()})
}

// stateDTO is the whole state in one read.
func (s *Server) stateDTO(ctx context.Context) stateDTO {
	st := s.opts.Player.State()
	out := stateDTO{
		Transport: transportDTO(feeder.Idle),
		Settings: settingsDTO{
			Transpose:   st.Key,
			TempoRatio:  feeder.TempoRatio(st.TempoSteps),
			MusicVolume: float64(st.Volume) / 100,
		},
	}
	if !st.Connected {
		out.Settings = settingsDTO{TempoRatio: 1, MusicVolume: 1}
	}
	s.opts.Room.View(func(e *fair.Evening, _ time.Time) { out.QueueLen = e.Queue().Len() })
	if st.Connected {
		out.Transport = transportDTO(st.Transport)
		if st.Song != nil {
			out.NowPlaying = s.nowPlaying(ctx, st.Song)
			out.PositionMS = uint32(max(st.Position.Milliseconds(), 0))
		}
	}
	if out.NowPlaying == nil && (out.Transport == "playing" || out.Transport == "paused") {
		out.Transport = "idle"
	}
	return out
}

// nowPlaying describes the playing song.
func (s *Server) nowPlaying(ctx context.Context, song *feeder.Song) *nowPlayingDTO {
	np := &nowPlayingDTO{
		Title: song.Title, Kind: "midi", DurationMS: s.opts.Durations.Get(song.Number),
		TransposeAvailable: true, TempoAvailable: true, HasLyrics: true,
	}
	if song.Number != 0 {
		if c, ok := s.lookup(ctx, song.Number); ok {
			np.Title, np.Artist, np.Language = c.Title, optString(c.Artist), languageCode(c.Language)
		}
	}
	if song.Entry != nil {
		n, id := Number(song.Number), song.Entry.ID
		np.Origin = originDTO{Kind: "catalog", Number: &n, EntryID: &id}
		np.Singer = optString(song.Entry.Singer)
	} else {
		// A song the evening did not hold: played from the unit's own remote,
		// or already playing when the session opened. "catalog" needs an
		// entry, so it is reported as a file.
		path := "unit:" + strconv.FormatUint(uint64(song.Number), 10)
		np.Origin = originDTO{Kind: "file", Path: &path}
	}
	return np
}

// lookup describes a song from the catalogue, remembering the last one, since
// the state goes out four times a second.
func (s *Server) lookup(ctx context.Context, number uint32) (catalog.Song, bool) {
	s.mu.Lock()
	if s.songCache.ID == int(number) {
		c := s.songCache
		s.mu.Unlock()
		return c, true
	}
	s.mu.Unlock()
	c, err := s.opts.Catalog.Song(ctx, int(number))
	if err != nil {
		return catalog.Song{}, false
	}
	s.mu.Lock()
	s.songCache = c
	s.mu.Unlock()
	return c, true
}

// queueAndRequests reads the queue and the requests together.
func (s *Server) queueAndRequests() (queueDTO, requestsDTO) {
	var q queueDTO
	var r requestsDTO
	s.opts.Room.View(func(e *fair.Evening, now time.Time) {
		q = queueOf(e)
		r = s.requestsOf(e, now)
	})
	return q, r
}

func queueOf(e *fair.Evening) queueDTO {
	entries := e.Queue().Entries()
	q := queueDTO{Entries: make([]queueEntryDTO, 0, len(entries)), Len: len(entries), Capacity: fair.MaxQueued}
	var ahead uint64
	known := true
	for i, en := range entries {
		d := queueEntryDTO{
			ID: en.ID, Position: i, Number: Number(en.Number), Title: en.Title,
			Artist: optString(en.Artist), Singer: optString(en.Singer), DurationMS: en.DurationMS,
			Seat: en.Seat,
		}
		// The wait is the sum of the lengths ahead, and unknown once one of
		// them is.
		if known {
			a := ahead
			d.AheadMS = &a
		}
		if en.DurationMS == 0 {
			known = false
		}
		ahead += uint64(en.DurationMS)
		q.Entries = append(q.Entries, d)
	}
	return q
}

func (s *Server) requestsOf(e *fair.Evening, now time.Time) requestsDTO {
	reqs := e.Requests(now)
	out := requestsDTO{
		Requests: make([]requestDTO, 0, len(reqs)), Capacity: fair.MaxRequests,
		Accepting: e.Accepting(), Sung: []sungDTO{}, WaitingForHost: s.WaitingForHost(),
	}
	for _, r := range reqs {
		out.Requests = append(out.Requests, requestOf(e, r))
	}
	for _, sung := range e.Sung() {
		out.Sung = append(out.Sung, sungDTO{Number: Number(sung.Number), Singer: sung.Seat})
	}
	return out
}

func requestOf(e *fair.Evening, r fair.Request) requestDTO {
	d := requestDTO{
		ID: r.ID, Singer: r.Seat, Number: Number(r.Number), Title: r.Title, Artist: optString(r.Artist),
		DurationMS: r.DurationMS, State: string(r.State), EntryID: r.Entry,
	}
	if r.State == fair.Queued && r.Entry != nil {
		if p := e.Queue().PositionOf(*r.Entry); p >= 0 {
			d.Position = &p
		}
	}
	return d
}

func (s *Server) singersDTO() singersDTO {
	out := singersDTO{Singers: []singerDTO{}, Capacity: fair.MaxSeats, Evening: s.Evening()}
	s.opts.Room.View(func(e *fair.Evening, _ time.Time) {
		for _, seat := range e.Seats().All() {
			out.Singers = append(out.Singers, singerOf(seat))
		}
	})
	return out
}

func singerOf(seat fair.Seat) singerDTO {
	d := singerDTO{ID: seat.ID, Name: optString(seat.Name), Phone: seat.Phone}
	if seat.Guest != 0 {
		g := seat.Guest
		d.Guest = &g
	}
	return d
}

// catalogVersion changes whenever an exported field does: with the unit's
// database version, and with each length learned.
func (s *Server) catalogVersion() uint64 {
	return uint64(s.opts.Catalog.Info().DBVersion)<<32 | uint64(s.opts.Durations.Len())
}

func (s *Server) songOf(c catalog.Song) songDTO {
	return songDTO{
		Number: Number(c.ID), Title: c.Title, Artist: optString(c.Artist), Language: languageCode(c.Language),
		Kind: "midi", DurationMS: s.opts.Durations.Get(uint32(c.ID)), PackageID: PackageID,
	}
}

// LANAddrs lists this computer's IPv4 LAN addresses, best first: 192.168/16,
// then 10/8, then 172.16/12, then the rest.
func LANAddrs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip := ipn.IP.To4(); ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				out = append(out, ip)
			}
		}
	}
	rank := func(ip net.IP) int {
		switch {
		case ip[0] == 192 && ip[1] == 168:
			return 0
		case ip[0] == 10:
			return 1
		case ip[0] == 172 && ip[1]&0xf0 == 16:
			return 2
		}
		return 3
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && rank(out[j]) < rank(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
