package webui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rrgmc/ck-karaoke/catalog"
)

// The song list is the device's own song database. It normally comes from the
// device over the session, the first time one comes up. Until then the Songs
// tab says what it is waiting for, and offers to import a copy of the file
// instead, so an app that has never reached a device still has a way to a
// song list.
//
// The list only ever goes from absent to present: there is no replacing one
// that is loaded, so the handlers may read it twice without holding a lock.

// maxSongList bounds an imported song list. The device's file is a few
// megabytes; this leaves a wide margin and still refuses a video picked by
// mistake before it fills the disk.
const maxSongList = 64 << 20

// songListState is what the Songs tab shows while there is no song list.
type songListState struct {
	// Downloading is true while the device is sending the list. Have and
	// Total are the bytes so far and in all; Total is 0 until it is known.
	Downloading bool
	Have, Total int
	// Err is why the last download or import failed.
	Err error
	// Ready is set once the list is loaded, which tells an open page to
	// reload.
	Ready bool
	// CanImport offers the import, which needs somewhere to write the file.
	CanImport bool
}

// Errors the song list pages show.
var (
	errSongListDownload = errors.New("the song list download failed")
	errSongListTooLarge = errors.New("the file is too large to be a song list")
	errNotSongList      = errors.New("the file is not a song list")
	errNoFile           = errors.New("no file was chosen")
)

// Percent is the download's progress, 0 to 100.
func (st songListState) Percent() int {
	if st.Total <= 0 {
		return 0
	}
	return st.Have * 100 / st.Total
}

// cat returns the song list, or nil while there is none.
func (s *Server) cat() *catalog.DB { return s.songs.Load() }

// Catalogue returns the song list, or nil while there is none. The caller
// that assembled the server closes it.
func (s *Server) Catalogue() *catalog.DB { return s.cat() }

// setCatalogue loads a song list that has just arrived and tells every open
// page.
func (s *Server) setCatalogue(db *catalog.DB) {
	s.defaultLan.Store(int64(db.LanTypeNamed(context.Background(), DefaultLanguage)))
	s.songs.Store(db)
	info := db.Info()
	s.logger.Info("song catalogue ready",
		"file", db.Path(), "songs", info.Songs, "artists", info.Singers, "version", info.DBVersion)
	s.setSongListState(func(st *songListState) { *st = songListState{Ready: true} })
}

// songListNow returns the current state.
func (s *Server) songListNow() songListState {
	s.listMu.Lock()
	defer s.listMu.Unlock()
	st := s.listState
	st.CanImport = s.songListPath != ""
	return st
}

// setSongListState changes the state and publishes it.
func (s *Server) setSongListState(change func(*songListState)) {
	s.listMu.Lock()
	change(&s.listState)
	s.listMu.Unlock()
	s.publish(eventSongList, "_songlist.html", s.songListNow())
}

// downloadSongList fetches the song list over the device session, when there
// is none and nothing is fetching it already. It runs when the session comes
// up, and returns at once otherwise.
func (s *Server) downloadSongList() {
	if s.cat() != nil || s.songListPath == "" {
		return
	}
	unit, ok := s.dev.(unitBackend)
	if !ok {
		return
	}
	c, err := unit.dev.get()
	if err != nil {
		return
	}
	s.listMu.Lock()
	if s.listState.Downloading {
		s.listMu.Unlock()
		return
	}
	s.listState = songListState{Downloading: true}
	s.listMu.Unlock()

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-s.stopping:
				cancel()
			case <-ctx.Done():
			}
		}()

		s.logger.Info("downloading the song database from the device", "device", s.dev.Addr())
		var lastPct int
		err := catalog.Fetch(ctx, c, s.songListPath, func(have, total int) {
			// Published in whole percents: every chunk would be hundreds of
			// updates to every open page.
			pct := 0
			if total > 0 {
				pct = have * 100 / total
			}
			if pct == lastPct && have > 0 {
				return
			}
			lastPct = pct
			s.setSongListState(func(st *songListState) { st.Have, st.Total = have, total })
		})
		var db *catalog.DB
		if err == nil {
			db, err = catalog.Open(s.songListPath)
		}
		if err != nil {
			s.logger.Error("downloading the song database", "error", err)
			s.setSongListState(func(st *songListState) {
				*st = songListState{Err: errSongListDownload}
			})
			return
		}
		s.setCatalogue(db)
	}()
}

// songListImportPage offers to import a song list file.
func (s *Server) songListImportPage(w http.ResponseWriter, r *http.Request) {
	if s.cat() != nil || s.songListPath == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.page(w, r, "songlist_import.html", pageData{Title: langOf(r).T("Import a song list")})
}

// songListImport installs an uploaded song list. The file is checked the way
// a download is, written beside the destination, and moved into place only
// once it opens as a song list.
func (s *Server) songListImport(w http.ResponseWriter, r *http.Request) {
	if s.cat() != nil || s.songListPath == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	fail := func(err error) {
		s.page(w, r, "songlist_import.html", pageData{
			Title:    langOf(r).T("Import a song list"),
			SongList: songListState{Err: err, CanImport: true},
		})
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSongList)
	file, header, err := r.FormFile("file")
	if err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			fail(errSongListTooLarge)
		case errors.Is(err, http.ErrMissingFile):
			fail(errNoFile)
		default:
			fail(errNotSongList)
		}
		return
	}
	defer file.Close()

	db, err := installSongList(file, s.songListPath)
	if err != nil {
		s.logger.Warn("importing a song list", "error", err)
		fail(errNotSongList)
		return
	}
	name := "a song list file"
	if header != nil && header.Filename != "" {
		name = header.Filename
	}
	s.logger.Info("imported a song list", "file", name)
	s.setCatalogue(db)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// songListExport sends the song list as a file, so another installation of
// the app can import it.
func (s *Server) songListExport(w http.ResponseWriter, r *http.Request) {
	db := s.cat()
	if db == nil {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(db.Path())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	const name = "song.db"
	if s.saveFile != nil {
		// 204 leaves the page where it was; the outcome arrives as a toast.
		path, err := s.saveFile(name, data)
		switch {
		case err != nil:
			s.logger.Error("saving the song list", "error", err)
			s.notify(toastBad, "The song list could not be saved: %s", err.Error())
		case path != "":
			s.notify(toastGood, "Song list saved to %s", path)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// installSongList writes src to path and opens it, leaving nothing behind at
// path unless it is a song list.
func installSongList(src io.Reader, path string) (*catalog.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".song.db-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	_, copyErr := io.Copy(tmp, src)
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		os.Remove(name)
		return nil, err
	}
	if err := catalog.Verify(name); err != nil {
		os.Remove(name)
		return nil, err
	}
	// Opened before it is moved into place, because Open is what checks the
	// tables: a valid SQLite file of something else passes Verify.
	db, err := catalog.Open(name)
	if err != nil {
		os.Remove(name)
		return nil, err
	}
	db.Close()
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return nil, fmt.Errorf("installing %s: %w", path, err)
	}
	return catalog.Open(path)
}
