package manager

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
	"github.com/rrgmc/ck-karaoke/version"
)

//go:embed admin.html
var adminPage []byte

// adminHandler serves the configuration and operator page on loopback. The
// page reads and acts through the API with no access check, and through the
// /admin routes for the settings and the status.
func (m *Manager) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", m.api.TrustedHandler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(adminPage)
	})
	mux.HandleFunc("GET /admin/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.Settings())
	})
	mux.HandleFunc("PUT /admin/settings", func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&s); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// The address is the server's to learn, not the page's to set.
		s.Unit.Addr = m.Settings().Unit.Addr
		if err := m.UpdateSettings(s); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, m.Settings())
	})
	mux.HandleFunc("GET /admin/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.status())
	})
	mux.HandleFunc("GET /admin/units", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		devices, err := client.Discover(ctx, "", 3*time.Second)
		units := []unitDTO{}
		for _, d := range devices {
			units = append(units, unitDTO{Addr: d.Addr, Product: d.Info.Product, Model: d.Info.Model,
				DatabaseID: d.Info.DatabaseID})
		}
		if err != nil && len(units) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"units": units, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"units": units})
	})
	return mux
}

// apiHandler serves the API on the LAN, and a short page at its root for a
// person who opens the address in a browser.
func (m *Manager) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", m.api.Handler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "This is a CK Karaoke queue manager ("+m.Settings().Name+").\n"+
			"Queue songs with KM Remote. The configuration page is on the computer that runs it.\n")
	})
	return mux
}

type unitDTO struct {
	Addr       string `json:"addr"`
	Product    string `json:"product"`
	Model      string `json:"model"`
	DatabaseID string `json:"database_id"`
}

type statusDTO struct {
	Version     string   `json:"version"`
	Started     string   `json:"started"`
	ID          string   `json:"id"`
	APIListen   string   `json:"api_listen"`
	URLs        []string `json:"urls"`
	Connected   bool     `json:"connected"`
	UnitAddr    string   `json:"unit_addr"`
	Songs       int      `json:"songs"`
	Lengths     int      `json:"lengths"`
	Evening     string   `json:"evening"`
	WaitHost    bool     `json:"waiting_for_host"`
	Held        bool     `json:"held"`
	Foreign     string   `json:"foreign"`
	Order       string   `json:"order"`
	Announced   string   `json:"announced"`
	ServiceType string   `json:"service_type"`
}

func (m *Manager) status() statusDTO {
	st := m.feeder.State()
	a := m.api.Announcement()
	s := statusDTO{
		Version: version.Version, Started: m.started.Format(time.RFC3339), ID: a.ID, APIListen: m.cfg.Listen,
		URLs: []string{}, Connected: st.Connected, UnitAddr: m.dialer.Addr(), Songs: m.catalog.Info().Songs,
		Lengths: m.durations.Len(), Evening: m.api.Evening(), WaitHost: m.api.WaitingForHost(), Held: st.Held,
		Foreign: string(m.feeder.Foreign()), Order: string(m.Settings().Order), Announced: a.Instance(),
		ServiceType: kmapi.ServiceType,
	}
	for _, ip := range a.Addrs {
		s.URLs = append(s.URLs, "http://"+ip+":"+itoa(a.Port))
	}
	return s
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("manager: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}
