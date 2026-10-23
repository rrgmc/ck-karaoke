package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// assetVersions maps each embedded static file to a short hash of its contents.
//
// Files embedded with go:embed carry a zero modification time, so
// http.FileServerFS sends no Last-Modified and no ETag, and every request for
// them is answered with a fresh 200 — an If-Modified-Since cannot even produce
// a 304. A phone was therefore refetching about 300KB of CSS and JavaScript on
// every tab tap, in four requests, and a browser opens only around six
// connections per host with the event stream permanently holding one of them.
// That was enough to make plain navigation stall, with no button pressed and
// nothing wrong with the device.
//
// Hashing the contents lets the templates ask for a URL that changes whenever
// the file does, which is what makes a long cache lifetime safe.
var assetVersions = buildAssetVersions()

func buildAssetVersions() map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		out[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:])[:10]
		return nil
	})
	return out
}

// assetURL is the template helper behind {{asset "app.css"}}. An unknown name
// still yields a usable URL, so a typo is a cache miss rather than a broken
// page.
func assetURL(name string) string {
	if v, ok := assetVersions[name]; ok {
		return "/static/" + name + "?v=" + v
	}
	return "/static/" + name
}

// staticHandler serves the embedded assets with cache headers.
//
// The year-long lifetime applies only when the request carries the content hash
// the templates emit, so a new binary is picked up immediately: its pages ask
// for a different URL. A request without the hash — someone's bookmark, or a
// hand-typed path — gets a short lifetime instead, so it cannot pin a stale
// copy for a year.
func staticHandler() http.Handler {
	files := http.FileServerFS(staticFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		if v, ok := assetVersions[name]; ok {
			// ServeContent checks preconditions against whatever ETag is already
			// on the response, so setting it here is what turns a revalidation
			// into a 304 rather than another full body.
			w.Header().Set("ETag", `"`+v+`"`)
			if r.URL.Query().Get("v") == v {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=300")
			}
		}
		files.ServeHTTP(w, r)
	})
}
