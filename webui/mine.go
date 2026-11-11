package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rrgmc/ck-karaoke/i18n"
)

// caps is the backend's capabilities, or the unit's when there is no backend.
func (s *Server) caps() Capabilities {
	if s.dev == nil {
		return Capabilities{}
	}
	return s.dev.Capabilities()
}

// entryActionTimeout bounds moving or removing one queue entry.
const entryActionTimeout = 15 * time.Second

// entryAction moves or removes one queue entry and answers with the queue.
func (s *Server) entryAction(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	// Longer than other commands: the unit moves a song by removing entries,
	// waiting for its queue to show that, and adding them back.
	ctx, cancel := context.WithTimeout(context.Background(), entryActionTimeout)
	defer cancel()
	if err := s.dev.EntryAction(ctx, r.PathValue("action"), r.PathValue("entry")); err != nil {
		withToast(w, toastBad, errorText(lang, err))
	}
	s.fragment(w, r, "_queue.html", s.cachedQueue(r.Context()))
}

// mineData drives the My songs page.
type mineData struct {
	Lang i18n.Lang
	Me   Me
	// Requests are Me.Requests joined with the catalogue.
	Requests []mineRequest
	// Error is a message about the last action, already translated.
	Error string
}

type mineRequest struct {
	MyRequest
	Title  string
	Artist string
}

func (s *Server) singer() (Singer, bool) {
	sg, ok := s.dev.(Singer)
	return sg, ok && s.caps().Requests
}

func (s *Server) mineData(ctx context.Context, lang i18n.Lang) mineData {
	sg, _ := s.singer()
	ctx, cancel := context.WithTimeout(ctx, interactiveTimeout)
	defer cancel()
	d := mineData{Lang: lang, Me: sg.Me(ctx)}
	ids := make([]int, 0, len(d.Me.Requests))
	for _, rq := range d.Me.Requests {
		if n, err := strconv.Atoi(rq.SongID); err == nil {
			ids = append(ids, n)
		}
	}
	titles := map[int]struct{ Title, Artist string }{}
	if s.cat() != nil {
		if found, err := s.cat().Songs(ctx, ids); err == nil {
			for id, song := range found {
				titles[id] = struct{ Title, Artist string }{song.Title, song.Artist}
			}
		}
	}
	for _, rq := range d.Me.Requests {
		mr := mineRequest{MyRequest: rq}
		if n, err := strconv.Atoi(rq.SongID); err == nil {
			mr.Title, mr.Artist = titles[n].Title, titles[n].Artist
		}
		d.Requests = append(d.Requests, mr)
	}
	return d
}

func (s *Server) minePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.singer(); !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	s.page(w, r, "mine.html", pageData{Title: lang.T("My songs"), Mine: s.mineData(r.Context(), lang)})
}

func (s *Server) mineFragment(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.singer(); !ok {
		http.NotFound(w, r)
		return
	}
	s.fragment(w, r, "_mine.html", s.mineData(r.Context(), langOf(r)))
}

// setName names this phone's singer and answers with the refreshed block.
func (s *Server) setName(w http.ResponseWriter, r *http.Request) {
	sg, ok := s.singer()
	if !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	var problem string
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		problem = lang.T("Type a name first.")
	} else {
		ctx, cancel := deviceCtx()
		err := sg.SetName(ctx, name)
		cancel()
		if err != nil {
			problem = errorText(lang, err)
		} else {
			withToast(w, toastGood, lang.T("Hello, %s", name))
		}
	}
	d := s.mineData(r.Context(), lang)
	d.Error = problem
	s.fragment(w, r, "_mine.html", d)
}

// withdraw takes back one request and answers with the refreshed block.
func (s *Server) withdraw(w http.ResponseWriter, r *http.Request) {
	sg, ok := s.singer()
	if !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	ctx, cancel := deviceCtx()
	err := sg.Withdraw(ctx, r.PathValue("id"))
	cancel()
	d := s.mineData(r.Context(), lang)
	if err != nil {
		d.Error = errorText(lang, err)
	}
	s.fragment(w, r, "_mine.html", d)
}
