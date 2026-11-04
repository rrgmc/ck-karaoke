package webui

import (
	"context"
	"net/http"
	"strings"

	"github.com/rrgmc/ck-karaoke/i18n"
)

// setupData drives the Setup page.
type setupData struct {
	Lang i18n.Lang
	Info SetupInfo
	// Error is a message about the last code entered, already translated.
	Error string
}

// setup returns the backend's Setup, if it has one.
func (s *Server) setup() (Setup, bool) {
	st, ok := s.dev.(Setup)
	return st, ok
}

func (s *Server) setupInfo(ctx context.Context) SetupInfo {
	st, _ := s.setup()
	ctx, cancel := context.WithTimeout(ctx, interactiveTimeout)
	defer cancel()
	return st.SetupInfo(ctx)
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.setup(); !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	s.page(w, r, "setup.html", pageData{
		Title: lang.T("Setup"),
		Setup: setupData{Lang: lang, Info: s.setupInfo(r.Context())},
	})
}

// enterCode exchanges a code and answers with the refreshed setup block.
func (s *Server) enterCode(w http.ResponseWriter, r *http.Request) {
	st, ok := s.setup()
	if !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	data := setupData{Lang: lang}
	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		data.Error = lang.T("Type the code first.")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), interactiveTimeout)
		err := st.EnterCode(ctx, code)
		cancel()
		if err != nil {
			data.Error = errorText(lang, err)
		} else {
			withToast(w, toastGood, lang.T("Code accepted"))
		}
	}
	data.Info = s.setupInfo(r.Context())
	s.fragment(w, r, "_setup.html", data)
}

// forgetCode drops the code and answers with the refreshed setup block.
func (s *Server) forgetCode(w http.ResponseWriter, r *http.Request) {
	st, ok := s.setup()
	if !ok {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	data := setupData{Lang: lang}
	if err := st.ForgetCode(); err != nil {
		data.Error = errorText(lang, err)
	}
	data.Info = s.setupInfo(r.Context())
	s.fragment(w, r, "_setup.html", data)
}
