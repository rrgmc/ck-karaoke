package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/rrgmc/ck-karaoke/i18n"
)

// hostData drives the Host page.
type hostData struct {
	Lang    i18n.Lang
	View    HostView
	Waiting []hostRequest
	// Error is a message about the last action, already translated.
	Error string
}

type hostRequest struct {
	HostRequest
	Title  string
	Artist string
}

// host returns the backend's Host when this app holds the host level.
func (s *Server) host() (Host, bool) {
	h, ok := s.dev.(Host)
	return h, ok && h.HostAllowed()
}

func (s *Server) hostData(ctx context.Context, lang i18n.Lang) hostData {
	h, _ := s.host()
	ctx, cancel := context.WithTimeout(ctx, interactiveTimeout)
	defer cancel()
	d := hostData{Lang: lang, View: h.HostView(ctx)}
	ids := make([]int, 0, len(d.View.Waiting))
	for _, rq := range d.View.Waiting {
		if n, err := strconv.Atoi(rq.SongID); err == nil {
			ids = append(ids, n)
		}
	}
	var found map[int]struct{ Title, Artist string }
	if s.cat() != nil {
		if songs, err := s.cat().Songs(ctx, ids); err == nil {
			found = make(map[int]struct{ Title, Artist string }, len(songs))
			for id, song := range songs {
				found[id] = struct{ Title, Artist string }{song.Title, song.Artist}
			}
		}
	}
	for _, rq := range d.View.Waiting {
		hr := hostRequest{HostRequest: rq}
		if n, err := strconv.Atoi(rq.SongID); err == nil {
			hr.Title, hr.Artist = found[n].Title, found[n].Artist
		}
		d.Waiting = append(d.Waiting, hr)
	}
	return d
}

func (s *Server) hostPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.host(); !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	s.page(w, r, "host.html", pageData{Title: lang.T("Host"), Host: s.hostData(r.Context(), lang)})
}

func (s *Server) hostFragment(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.host(); !ok {
		http.NotFound(w, r)
		return
	}
	s.fragment(w, r, "_host.html", s.hostData(r.Context(), langOf(r)))
}

// hostAction runs one action and answers with the refreshed page block.
func (s *Server) hostAction(w http.ResponseWriter, r *http.Request) {
	h, ok := s.host()
	if !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	action := r.PathValue("action")
	value := strings.TrimSpace(r.FormValue("value"))
	var problem string
	if action == "add-singer" && value == "" {
		problem = lang.T("Type a name first.")
	} else {
		ctx, cancel := deviceCtx()
		err := h.HostAction(ctx, action, r.FormValue("id"), value)
		cancel()
		if err != nil {
			problem = errorText(lang, err)
		}
	}
	d := s.hostData(r.Context(), lang)
	d.Error = problem
	s.fragment(w, r, "_host.html", d)
}
