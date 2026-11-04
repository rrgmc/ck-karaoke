// Package kmapitest serves a queue manager's API with a fake unit and a fake
// catalogue, for testing its clients against the real server.
package kmapitest

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
)

// The access codes every Server has.
const (
	QueueCode   = "1111"
	ControlCode = "2222"
	HostCode    = "3333"
)

// Songs is a catalogue held in memory.
type Songs []catalog.Song

// Song finds one song.
func (c Songs) Song(_ context.Context, id int) (catalog.Song, error) {
	for _, s := range c {
		if s.ID == id {
			return s, nil
		}
	}
	return catalog.Song{}, errors.New("no such song")
}

// SongsAfter lists songs in number order.
func (c Songs) SongsAfter(_ context.Context, after, limit int) ([]catalog.Song, error) {
	var out []catalog.Song
	for _, s := range c {
		if s.ID > after && len(out) < limit {
			out = append(out, s)
		}
	}
	return out, nil
}

// Info reports database version 7 and the song count.
func (c Songs) Info() catalog.Info { return catalog.Info{DBVersion: 7, Songs: len(c)} }

// Catalog is the catalogue every Server has.
var Catalog = Songs{
	{ID: 1001, Title: "PRESENTE DE DEUS", Artist: "FERNANDINHO"},
	{ID: 3656, Title: "MENINA", Artist: "LUAN SANTANA"},
	{ID: 6541, Title: "4 MINUTES", Artist: "MADONNA"},
}

// Player is a fake unit that records the commands it gets.
type Player struct {
	mu    sync.Mutex
	state feeder.State
	calls []string
}

// State reports the unit's state.
func (p *Player) State() feeder.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// SetConnected connects or disconnects the unit.
func (p *Player) SetConnected(c bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.Connected = c
}

// Calls lists the commands sent so far.
func (p *Player) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *Player) call(c string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, c)
	return nil
}

// The commands record their name.
func (p *Player) Play(context.Context) error               { return p.call("play") }
func (p *Player) Pause(context.Context) error              { return p.call("pause") }
func (p *Player) Skip(context.Context) error               { return p.call("skip") }
func (p *Player) Restart(context.Context) error            { return p.call("restart") }
func (p *Player) Stop(context.Context) error               { return p.call("stop") }
func (p *Player) SetTranspose(context.Context, int) error  { return p.call("transpose") }
func (p *Player) SetTempo(context.Context, float64) error  { return p.call("tempo") }
func (p *Player) SetVolume(context.Context, float64) error { return p.call("volume") }

// Server is a running API with a fake unit.
type Server struct {
	API    *kmapi.Server
	Player *Player
	// URL is the API's base address, such as http://127.0.0.1:1234.
	URL string
}

// New serves the API with the given room level and song database file ("" for
// none), runs its event loop, and stops both when the test ends.
func New(t testing.TB, room kmapi.Level, catalogFile string) *Server {
	t.Helper()
	lookup := func(n uint32) (fair.Song, bool) {
		s, err := Catalog.Song(context.Background(), int(n))
		return fair.Song{Number: n, Title: s.Title, Artist: s.Artist}, err == nil
	}
	p := &Player{state: feeder.State{Connected: true, Transport: feeder.Idle, Volume: 50}}
	api := kmapi.New(kmapi.Options{
		Room:        feeder.NewRoom(fair.NewEvening(fair.Turns), lookup),
		Player:      p,
		Catalog:     Catalog,
		CatalogFile: catalogFile,
		Unit:        func() (string, string) { return "MK8513-BR-B", "d91c983b" },
		ID:          "00112233aabbccdd",
		Secret:      []byte("secret"),
		Version:     "1.3.0",
		Config: kmapi.Config{
			Name: "Living room", Room: room,
			Codes: kmapi.Codes{Queue: QueueCode, Control: ControlCode, Host: HostCode},
		},
		Logger: slog.New(slog.DiscardHandler),
		Addrs:  func() []net.IP { return []net.IP{net.IPv4(127, 0, 0, 1)} },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); api.Run(ctx) }()
	hs := httptest.NewServer(api.Handler())
	t.Cleanup(func() {
		cancel()
		<-done
		hs.Close()
	})
	return &Server{API: api, Player: p, URL: hs.URL}
}
