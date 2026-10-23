package webui

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/protocol"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server is the web UI.
type Server struct {
	dev    *Device
	songs  *catalog.DB
	favs   *favorites.DB
	logger *slog.Logger
	// allowFolderDelete gates folder deletion, which is the one irreversible
	// action in the UI and is off unless the operator asked for it.
	allowFolderDelete bool
	saveFile          func(name string, data []byte) (string, error)
	tmpl              *template.Template
	hub               *hub
	// defaultLan is the language a first-time visitor sees. Resolved from the
	// catalogue by name rather than hardcoded, since the ids come from the
	// device's own table.
	defaultLan int
	// stopping is closed when shutdown begins, so the event streams let go of
	// their connections.
	stopping chan struct{}
	stopOnce sync.Once
	// refreshing guards against piling up queue requests, which take about two
	// seconds each.
	refreshing atomic.Bool
}

// DefaultLanguage is what the language filter starts on. The catalogue is
// mostly Portuguese, but the filter opening on one language rather than all of
// them is the point: eleven thousand songs in a language you do not sing is a
// worse starting page than two thousand in one you do.
const DefaultLanguage = "English"

// Options are the settings that are not wiring.
type Options struct {
	// AllowFolderDelete enables deleting favorites folders. Off by default:
	// every client is unauthenticated by design, and deletion cannot be undone.
	AllowFolderDelete bool

	// SaveFile, if set, saves a file through the host's own dialog instead of
	// sending it to the browser as a download. It returns where the file went,
	// or "" if the user cancelled.
	//
	// For hosts whose web view has no download handling of its own, such as the
	// desktop port's. The phone ports handle the download header natively and
	// leave this nil.
	SaveFile func(name string, data []byte) (string, error)
}

// New builds a Server. The Device must be run separately.
func New(dev *Device, songs *catalog.DB, favs *favorites.DB, logger *slog.Logger, opts Options) (*Server, error) {
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	srv := &Server{
		dev: dev, songs: songs, favs: favs, logger: logger,
		tmpl: tmpl, hub: newHub(), stopping: make(chan struct{}),
		allowFolderDelete: opts.AllowFolderDelete,
		saveFile:          opts.SaveFile,
	}
	if songs != nil {
		srv.defaultLan = songs.LanTypeNamed(context.Background(), DefaultLanguage)
	}
	return srv, nil
}

// BeginShutdown releases the event streams.
//
// http.Server.Shutdown waits for active connections to become idle, and an SSE
// connection never does — it is meant to stay open. So shutdown always ran to
// its deadline and reported "context deadline exceeded", which reads as a fault
// when it was only a stream doing its job. Wired to RegisterOnShutdown, the
// streams close themselves and shutdown finishes at once.
func (s *Server) BeginShutdown() {
	s.stopOnce.Do(func() { close(s.stopping) })
}

// AllowFolderDelete reports whether the delete control should be offered.
func (s *Server) AllowFolderDelete() bool { return s.allowFolderDelete }

// PublishStatus pushes new device state to every connected browser. Wire it to
// the Device's status callback.
//
// Nothing is rendered when nobody is subscribed. The device pushes state about
// every 1.5s and this is not a cheap callback — playerData looks the song up in
// SQLite and the template is executed in full — so a server with no page open
// was doing that forty times a minute for no reader. The cost of being wrong is
// only that a page opening in the next 1.5s misses one replay from hub.latest,
// and it does not need one: every page renders the player server-side on load.
//
// This says nothing about a phone whose screen is off. A paused WebView holds
// its stream open and still counts here; what stops the work there is the app
// giving the session up, in ServerService.appBackgrounded.
func (s *Server) PublishStatus(st *protocol.Status) {
	if s.hub.count() == 0 {
		return
	}
	html, err := s.render("_player.html", s.playerData(st))
	if err != nil {
		s.logger.Error("rendering player", "error", err)
		return
	}
	s.hub.publish(eventPlayer, html)
}

// WatchConnection wires the device's up/down transitions to notifications, so a
// dropped session announces itself instead of quietly failing every button.
// WatchQueue wires reserved-list updates — whether asked for or pushed by the
// device — to the browsers.
func (s *Server) WatchQueue(dev *Device) {
	dev.OnQueue(s.PublishQueue)
}

func (s *Server) WatchConnection(dev *Device) {
	dev.OnConnection(func(online bool, reason error) {
		// The banner is part of the page shell, so without this it only changed
		// on a full reload — it would sit there saying "unreachable" long after
		// the device came back.
		s.publishBanner(online, reason)
		s.publishConn(online)

		switch {
		case online:
			s.notify(toastGood, "Device connected")
		case errors.Is(reason, client.ErrBusy):
			// Worth spelling out: the usual cause is another
			// remote still holding the single connection slot.
			s.notify(toastBad, "Device is busy — it allows one connection at a time. "+
				"Close any other remote.")
		case reason != nil:
			s.notify(toastWarn, "Device unreachable — retrying")
		default:
			s.notify(toastWarn, "Device disconnected — reconnecting")
		}
	})
}

// publishBanner pushes the connection banner to every open page.
func (s *Server) publishBanner(online bool, reason error) {
	html, err := s.render("_banner.html", bannerData{
		Online:     online,
		DeviceAddr: s.dev.Addr(),
		Reason:     offlineReason(reason),
	})
	if err != nil {
		s.logger.Error("rendering banner", "error", err)
		return
	}
	s.hub.publish(eventBanner, html)
}

// publishConn updates the session indicator in the tab bar.
func (s *Server) publishConn(online bool) {
	html, err := s.render("_conn.html", online)
	if err != nil {
		s.logger.Error("rendering connection indicator", "error", err)
		return
	}
	s.hub.publish(eventConn, html)
}

// offlineReason turns a dial failure into something worth reading.
func offlineReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, client.ErrBusy):
		return "busy — it serves two clients at once, so a slot may be taken by another remote"
	default:
		return "unreachable"
	}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /manifest.json", s.manifest)

	mux.HandleFunc("GET /{$}", s.browse)
	mux.HandleFunc("GET /now", s.now)
	mux.HandleFunc("GET /queue", s.queuePage)
	mux.HandleFunc("GET /reset", s.reset)
	// Kept so old links and bookmarks to the catalogue search still work.
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		target := "/"
		if raw := r.URL.RawQuery; raw != "" {
			target += "?" + raw
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})

	mux.HandleFunc("GET /events", s.serveSSE)
	mux.HandleFunc("GET /fragment/player", s.playerFragment)
	mux.HandleFunc("GET /fragment/queue", s.queueFragment)

	mux.HandleFunc("GET /favorites/sheet/close", s.sheetClose)
	mux.HandleFunc("GET /favorites/sheet/{song}", s.sheet)
	mux.HandleFunc("POST /favorites/toggle/{folder}/{song}", s.toggleFavorite)
	mux.HandleFunc("POST /favorites/folders", s.createFolder)
	mux.HandleFunc("POST /favorites/folders/{id}/rename", s.renameFolder)
	mux.HandleFunc("POST /favorites/folders/{id}/remove/{song}", s.removeFavorite)
	mux.HandleFunc("POST /favorites/folders/{id}/delete", s.deleteFolder)

	// Always one folder, named in the path, and always one side of the exchange
	// at a time. Sync is reachable only from inside a folder: that is what
	// removes the "which folder?" question from both ends of it.
	mux.HandleFunc("GET /favorites/sync/{folder}", s.syncChoose)
	mux.HandleFunc("GET /favorites/sync/{folder}/send", s.syncSend)
	mux.HandleFunc("GET /favorites/sync/{folder}/receive", s.syncReceive)
	mux.HandleFunc("POST /favorites/sync/{folder}/receive", s.syncScanned)
	mux.HandleFunc("POST /favorites/sync/{folder}/merge", s.syncMerge)
	mux.HandleFunc("GET /favorites/qr/{folder}", s.favoritesQR)

	// Backup is the whole database rather than one folder, so unlike sync it
	// hangs off nothing and is reachable from the folder list. Plain navigations,
	// not htmx: the export is a file the browser has to be allowed to save, and
	// an htmx swap would eat it.
	mux.HandleFunc("GET /favorites/backup", s.backupChoose)
	mux.HandleFunc("GET /favorites/backup.xml", s.backupExport)
	mux.HandleFunc("GET /favorites/backup/import", s.backupImport)
	mux.HandleFunc("POST /favorites/backup/import", s.backupRestore)

	mux.HandleFunc("POST /control/{action}", s.control)
	mux.HandleFunc("POST /song/{id}/{action}", s.songAction)

	return logRequests(s.logger, mux)
}

// catalogue returns the song database's metadata, or a zero value when none is
// loaded.
func (s *Server) catalogue() catalog.Info {
	if s.songs == nil {
		return catalog.Info{}
	}
	return s.songs.Info()
}

// render executes one template to a string.
func (s *Server) render(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// page renders a full page, or just the named fragment when htmx asked for one.
func (s *Server) page(w http.ResponseWriter, r *http.Request, name string, data pageData) {
	data.Online = s.dev.Online()
	data.DeviceAddr = s.dev.Addr()
	data.OfflineReason = offlineReason(s.dev.LastError())
	if q := s.dev.LastQueue(); q != nil {
		data.QueueCount = q.Total
	}
	if s.songs != nil {
		data.Catalogue = s.songs.Info()
	}
	data.Nav = navFor(r.URL.Path)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("rendering page", "template", name, "error", err)
	}
}

// fragment writes one htmx fragment.
func (s *Server) fragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("rendering fragment", "template", name, "error", err)
	}
}

// fail reports a problem to the user as a small banner htmx can swap in, rather
// than a bare HTTP error the UI would have to handle specially.
func (s *Server) fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, ErrOffline) {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<div class="alert alert-danger py-2 my-2">%s</div>`,
		template.HTMLEscapeString(err.Error()))
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	// The icons are hashed like every other asset, so the URLs are built rather
	// than written out. An empty list here is what stopped this being installable
	// as a home-screen app at all.
	fmt.Fprintf(w, `{
  "name": "CK Karaoke",
  "short_name": "CK Karaoke",
  "start_url": "/",
  "display": "standalone",
  "background_color": "#ffffff",
  "theme_color": "#ffffff",
  "icons": [
    {"src": %q, "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": %q, "sizes": "512x512", "type": "image/png", "purpose": "any"}
  ]
}`, assetURL("icon-192.png"), assetURL("icon-512.png"))
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The event stream is long-lived; logging its start and end is noise.
		if r.URL.Path == "/events" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Debug("request", "method", r.Method, "path", r.URL.Path,
			"took", time.Since(start).Round(time.Millisecond))
	})
}

func navFor(path string) string {
	switch {
	case strings.HasPrefix(path, "/now"):
		return "now"
	case strings.HasPrefix(path, "/queue"):
		return "queue"
	default:
		return "browse"
	}
}
