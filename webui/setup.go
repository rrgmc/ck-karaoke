package webui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/i18n"
	"github.com/rrgmc/ck-karaoke/protocol"
)

// setupData drives the Settings page. Info and Error are the queue manager's
// part, shown only when the backend has a Setup.
type setupData struct {
	Lang i18n.Lang
	Info SetupInfo
	// Error is a message about the last code entered, already translated.
	Error string
}

// settingsData drives the parts of the Settings page every build has.
type settingsData struct {
	// LangChoice is the language picked on this device, or empty for the
	// system's.
	LangChoice i18n.Lang
	// HasName offers the name the unit shows beside songs queued from here.
	// Only the unit backend has one.
	HasName bool
	Name    string
	// NameErr is why the last name was refused, already translated.
	NameErr string
	// HasDevice shows the karaoke machine's address and whether the session
	// is up. Only the unit backend has one; the queue manager's Setup shows
	// its own.
	HasDevice    bool
	DeviceAddr   string
	DeviceOnline bool
	// Songs describes the song list, and HasSongs says whether there is one.
	Songs    catalog.Info
	HasSongs bool
}

// errNoName refuses a name that is empty once cleaned.
var errNoName = errors.New("the name is empty")

// settings returns the state the Settings page shows.
func (s *Server) settings(r *http.Request) settingsData {
	out := settingsData{LangChoice: recallLang(r)}
	if _, ok := s.dev.(unitBackend); ok {
		out.HasDevice = true
		out.DeviceAddr = s.dev.Addr()
		out.DeviceOnline = s.dev.Online()
	}
	if unit, ok := s.dev.(unitBackend); ok && s.nameFile != "" {
		out.HasName = true
		out.Name = unit.dev.Identity().Name
	}
	if db := s.cat(); db != nil {
		out.Songs, out.HasSongs = db.Info(), true
	}
	return out
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
	lang := langOf(r)
	data := pageData{
		Title:    lang.T("Settings"),
		Settings: s.settings(r),
	}
	if _, ok := s.setup(); ok {
		data.Setup = setupData{Lang: lang, Info: s.setupInfo(r.Context())}
	}
	s.page(w, r, "setup.html", data)
}

// setSessionName changes the name the unit shows beside songs queued from here, keeps
// it for the next start, and answers with the refreshed name block.
func (s *Server) setSessionName(w http.ResponseWriter, r *http.Request) {
	unit, ok := s.dev.(unitBackend)
	if !ok || s.nameFile == "" {
		http.NotFound(w, r)
		return
	}
	lang := langOf(r)
	// Checked before cleaning: CleanName turns an empty name into a default.
	raw := strings.TrimSpace(r.FormValue("name"))
	name := protocol.CleanName(raw)
	var err error
	if raw == "" {
		err = errNoName
	} else {
		err = writeName(s.nameFile, name)
	}
	data := s.settings(r)
	if err != nil {
		if errors.Is(err, errNoName) {
			data.NameErr = lang.T("Type a name first.")
		} else {
			s.logger.Error("saving the session name", "error", err)
			data.NameErr = errorText(lang, err)
		}
		s.fragment(w, r, "_settings_name.html", data)
		return
	}
	unit.dev.SetName(name)
	data.Name = name
	withToast(w, toastGood, lang.T("Name saved. The karaoke machine shows it from the next song you add."))
	s.fragment(w, r, "_settings_name.html", data)
}

// writeName saves the session name for the next start. Written through a
// temporary file, so a start never reads half a name.
func writeName(path, name string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".name-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(name + "\n")
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// ReadName returns the session name saved on the Settings page, or "" when
// none was saved. The caller that assembles the server reads it at start.
func ReadName(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return protocol.CleanName(strings.TrimSpace(string(b)))
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
