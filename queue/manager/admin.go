package manager

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// adminHandler serves the configuration page and, for it, the API with no
// access check. It listens on loopback only.
func (m *Manager) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", m.api.TrustedHandler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("CK Karaoke queue manager\n"))
	})
	return mux
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("manager: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}
