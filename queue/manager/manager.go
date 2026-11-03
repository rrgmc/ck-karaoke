// Package manager assembles the queue manager: it finds the karaoke unit,
// opens its catalogue, keeps the fair evening, feeds the unit, serves
// KaraokeMachine's API on the LAN and the configuration page on loopback.
//
// Every front end starts it the same way, with a Config and Run, as
// server.Run does for CK Karaoke.
package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/version"
)

// DefaultAdminListen is where the configuration page is served. It is loopback
// only: the page changes settings and acts as the host without a code.
const DefaultAdminListen = "127.0.0.1:8176"

// Config is everything Run needs.
type Config struct {
	// DataDir holds the catalogue, the settings, the evening and the learned
	// song lengths.
	DataDir string
	// Listen is where the API is served. It must be reachable from the
	// remotes' phones; defaults to ":8177".
	Listen string
	// AdminListen is where the configuration page is served. It must be
	// loopback; defaults to DefaultAdminListen.
	AdminListen string
	// Addr is the unit's address. Empty finds it by model and database id.
	Addr string
	// Logger receives everything. Nil logs to stderr; Verbose then adds debug.
	Logger  *slog.Logger
	Verbose bool
	// Ready is called once both listeners are open.
	Ready func(api, admin net.Addr)
	// Progress reports the catalogue download on a first start.
	Progress func(have, total int)
}

// Manager is a running queue manager.
type Manager struct {
	cfg      Config
	log      *slog.Logger
	settings string // the path of settings.json

	mu  sync.Mutex
	set Settings

	dialer    *unitDialer
	catalog   *catalog.DB
	durations *feeder.Durations
	room      *feeder.Room
	feeder    *feeder.Feeder
	api       *kmapi.Server
	started   time.Time
}

// Run serves until ctx is cancelled, then shuts down and closes the unit
// session cleanly before it returns.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Logger == nil {
		level := slog.LevelInfo
		if cfg.Verbose {
			level = slog.LevelDebug
		}
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	}
	if cfg.Listen == "" {
		cfg.Listen = fmt.Sprintf(":%d", kmapi.DefaultPort)
	}
	if cfg.AdminListen == "" {
		cfg.AdminListen = DefaultAdminListen
	}
	if err := checkLoopback(cfg.AdminListen); err != nil {
		return err
	}
	if cfg.DataDir == "" {
		return errors.New("manager: no data directory")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	m := &Manager{cfg: cfg, log: cfg.Logger, settings: filepath.Join(cfg.DataDir, "settings.json"), started: time.Now()}
	m.log.Info("CK Karaoke queue manager", "version", version.Version, "data", cfg.DataDir)
	return m.run(ctx)
}

func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("configuration page address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("the configuration page must listen on loopback, not %q", addr)
	}
	return nil
}

func (m *Manager) path(name string) string { return filepath.Join(m.cfg.DataDir, name) }

func (m *Manager) run(ctx context.Context) error {
	set, err := LoadSettings(m.settings)
	if err != nil {
		m.log.Warn("settings unusable; using the defaults", "error", err)
	}
	m.set = set
	id, err := keptHex(m.path("id"), 8)
	if err != nil {
		return err
	}
	secret, err := keptHex(m.path("secret"), 32)
	if err != nil {
		return err
	}

	m.dialer = &unitDialer{log: m.log, override: m.cfg.Addr, choice: func() UnitChoice { return m.Settings().Unit },
		found: m.rememberUnit}

	if m.catalog, err = m.openCatalogue(ctx); err != nil {
		return err
	}
	defer m.catalog.Close()
	info := m.catalog.Info()
	m.log.Info("song catalogue ready", "songs", info.Songs, "version", info.DBVersion)

	if m.durations, err = feeder.OpenDurations(m.path("durations.json")); err != nil {
		return err
	}

	evening, word, held := m.openEvening()
	m.room = feeder.NewRoom(evening, m.describe)

	identity := protocol.NewIdentity()
	identity.Name = protocol.CleanName(set.Name)
	m.feeder = feeder.New(feeder.Options{
		Dialer: m.dialer, Room: m.room, Identity: &identity, Foreign: set.Foreign, Held: held,
		Logger: m.log, OnEvent: func(e feeder.Event) {
			if m.api != nil {
				m.api.FeederEvent(e)
			}
		},
	})
	m.api = kmapi.New(kmapi.Options{
		Room: m.room, Player: m.feeder, Catalog: m.catalog, Durations: m.durations,
		ID: id, Port: portOf(m.cfg.Listen), Secret: []byte(secret), Evening: word, WaitingForHost: held,
		Version: version.Version, Config: set.API(), Logger: m.log,
	})

	apiLn, err := net.Listen("tcp", m.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", m.cfg.Listen, err)
	}
	adminLn, err := net.Listen("tcp", m.cfg.AdminListen)
	if err != nil {
		apiLn.Close()
		return fmt.Errorf("listening on %s: %w", m.cfg.AdminListen, err)
	}
	apiSrv := &http.Server{Handler: m.apiHandler(), ReadHeaderTimeout: 10 * time.Second}
	adminSrv := &http.Server{Handler: m.adminHandler(), ReadHeaderTimeout: 10 * time.Second}

	var wg sync.WaitGroup
	feederDone := make(chan struct{})
	go func() {
		defer close(feederDone)
		m.feeder.Run(ctx)
	}()
	for _, fn := range []func(){
		func() { m.api.Run(ctx) },
		func() { kmapi.Announce(ctx, m.api.Announcement, m.log.Info) },
		func() { m.keepEvening(ctx) },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	errc := make(chan error, 2)
	go func() { errc <- apiSrv.Serve(apiLn) }()
	go func() { errc <- adminSrv.Serve(adminLn) }()
	m.log.Info("listening", "api", apiLn.Addr().String(), "admin", adminLn.Addr().String())
	if m.cfg.Ready != nil {
		m.cfg.Ready(apiLn.Addr(), adminLn.Addr())
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
	}
	m.log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(shutdownCtx)
	_ = adminSrv.Shutdown(shutdownCtx)
	// The session closes when ctx is cancelled, and closing waits for the
	// disconnect notification to reach the unit; returning first would leave
	// the unit holding a connection slot.
	select {
	case <-feederDone:
	case <-time.After(client.CloseTimeout + time.Second):
		m.log.Warn("the unit session did not close in time")
	}
	wg.Wait()
	m.saveEvening()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return serveErr
}

// Settings returns the current settings.
func (m *Manager) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.set
}

// UpdateSettings validates, saves and applies new settings.
func (m *Manager) UpdateSettings(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	old := m.set
	m.set = s
	m.mu.Unlock()
	if err := SaveSettings(m.settings, s); err != nil {
		return err
	}
	m.api.Configure(s.API())
	m.feeder.SetForeign(s.Foreign)
	if s.Order != old.Order {
		m.room.Do(func(e *fair.Evening, now time.Time) { e.SetOrder(s.Order, now) })
	}
	if s.Unit.Model != old.Unit.Model || s.Unit.DatabaseID != old.Unit.DatabaseID {
		m.feeder.Reconnect()
	}
	if s.KeepEvening && !old.KeepEvening {
		m.saveEvening()
	} else if !s.KeepEvening && old.KeepEvening {
		_ = os.Remove(m.path("evening.json"))
	}
	m.log.Info("settings changed")
	return nil
}

// rememberUnit keeps the address a unit was found at, and makes a unit chosen
// by default the chosen one.
func (m *Manager) rememberUnit(dev client.Device) {
	m.mu.Lock()
	s := m.set
	s.Unit.Addr = dev.Addr
	if s.Unit.Model == "" && s.Unit.DatabaseID == "" {
		s.Unit.Model, s.Unit.DatabaseID = dev.Info.Model, dev.Info.DatabaseID
	}
	m.set = s
	m.mu.Unlock()
	if err := SaveSettings(m.settings, s); err != nil {
		m.log.Warn("cannot save the unit's address", "error", err)
	}
}

// describe describes a song number from the catalogue.
func (m *Manager) describe(n uint32) (fair.Song, bool) {
	s, err := m.catalog.Song(context.Background(), int(n))
	if err != nil {
		return fair.Song{Number: n, Title: fmt.Sprint(n)}, false
	}
	return fair.Song{Number: n, Title: s.Title, Artist: s.Artist, DurationMS: m.durations.Get(n)}, true
}

// openCatalogue opens song.db, downloading it from the unit when it is missing.
// With no catalogue the unit is the only source of one, so it waits for it.
func (m *Manager) openCatalogue(ctx context.Context) (*catalog.DB, error) {
	path := m.path("song.db")
	if db, err := catalog.Open(path); err == nil {
		return db, nil
	}
	for {
		err := m.fetchCatalogue(ctx, path)
		if err == nil {
			return catalog.Open(path)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		m.log.Warn("cannot download the song catalogue yet; still looking for the unit", "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func (m *Manager) fetchCatalogue(ctx context.Context, path string) error {
	identity := protocol.NewIdentity()
	identity.Name = "QueueManager"
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u, err := m.dialer.Dial(dctx, identity)
	if err != nil {
		return err
	}
	c := u.(*client.Client)
	defer c.Close()
	m.log.Info("downloading the song catalogue from the unit")
	return catalog.Fetch(ctx, c, path, m.cfg.Progress)
}

// openEvening restores a kept evening, or starts a new one. A restored evening
// holds playback until a host presses Play.
func (m *Manager) openEvening() (*fair.Evening, string, bool) {
	set := m.Settings()
	if set.KeepEvening {
		if kept, ok := fair.Load(m.path("evening.json"), m.log); ok {
			e, err := fair.Restore(kept.Evening, set.Order, time.Now())
			if err == nil && isWord(kept.Word) {
				m.log.Info("restored the kept evening; playback waits for a host", "queued", e.Queue().Len())
				return e, kept.Word, true
			}
			m.log.Warn("the kept evening could not be restored", "error", err)
		}
	}
	return fair.NewEvening(set.Order), "", false
}

// isWord reports whether an evening word is one KM Remote keeps: ASCII letters
// and digits, 1 to 64 of them.
func isWord(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// keepEvening writes the evening after each change while KeepEvening is on.
func (m *Manager) keepEvening(ctx context.Context) {
	changed, unsubscribe := m.room.Subscribe()
	defer unsubscribe()
	var last fair.Snapshot
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
			dirty = true
		case <-tick.C:
			if dirty && m.Settings().KeepEvening {
				if snap := m.snapshot(); !snapshotsEqual(snap, last) {
					m.writeEvening(snap)
					last = snap
				}
				dirty = false
			}
		}
	}
}

func (m *Manager) snapshot() fair.Snapshot {
	var playing *fair.Entry
	if st := m.feeder.State(); st.Song != nil && st.Song.Entry != nil {
		e := *st.Song.Entry
		playing = &e
	}
	var snap fair.Snapshot
	m.room.View(func(e *fair.Evening, now time.Time) { snap = e.Snapshot(playing, now) })
	return snap
}

func (m *Manager) saveEvening() {
	if m.room != nil && m.Settings().KeepEvening {
		m.writeEvening(m.snapshot())
	}
}

func (m *Manager) writeEvening(snap fair.Snapshot) {
	if err := fair.Write(m.path("evening.json"), fair.Kept{Word: m.api.Evening(), Evening: snap}); err != nil {
		m.log.Warn("cannot keep the evening", "error", err)
	}
}

func snapshotsEqual(a, b fair.Snapshot) bool {
	return reflect.DeepEqual(a, b)
}

// keptHex reads a random value kept in a file, or makes and keeps one.
func keptHex(path string, bytes int) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); len(s) == bytes*2 {
			return s, nil
		}
	}
	s := randomHex(bytes)
	if err := writeFile(path, []byte(s+"\n"), 0o600); err != nil {
		return "", err
	}
	return s, nil
}

func portOf(listen string) int {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return kmapi.DefaultPort
	}
	var n int
	if _, err := fmt.Sscan(p, &n); err != nil || n == 0 {
		return kmapi.DefaultPort
	}
	return n
}
