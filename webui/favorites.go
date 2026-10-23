package webui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/rrgmc/ck-karaoke/favorites"
)

// sheetData drives the folder picker.
type sheetData struct {
	SongID  int
	Title   string
	Artist  string
	Folders []favorites.Folder
	// In holds the folders this song is already filed in, so each row can show
	// whether tapping it files or unfiles.
	In map[int]bool
	// AllowDelete mirrors the server flag, so the markup can leave the control
	// out entirely rather than offer something the handler will refuse.
	AllowDelete bool
	// Multi keeps the sheet open as folders are ticked. Off by default: a song
	// usually goes in one folder, and making that the one-tap case is worth more
	// than saving a tap on the rarer one. Not remembered between openings —
	// the primary way has to be what you get when you have not asked.
	Multi bool
	// Err is a failed create or delete, shown inside the sheet rather than as a
	// toast, because it belongs to the form that caused it.
	Err string
}

// starData drives the favorite button on a song row.
type starData struct {
	ID int
	On bool
	// OOB marks the copy sent alongside a sheet response, which updates the row
	// behind the sheet without re-rendering the list.
	OOB bool
}

// sheet renders the folder picker for one song.
func (s *Server) sheet(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	songID, err := strconv.Atoi(r.PathValue("song"))
	if err != nil {
		s.fail(w, errors.New("bad song id"))
		return
	}
	s.renderSheet(w, r, songID, "")
}

// sheetClose empties the sheet container.
func (s *Server) sheetClose(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

// toggleFavorite files a song into a folder, or takes it out again.
func (s *Server) toggleFavorite(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	folderID, err1 := strconv.Atoi(r.PathValue("folder"))
	songID, err2 := strconv.Atoi(r.PathValue("song"))
	if err1 != nil || err2 != nil {
		s.fail(w, errors.New("bad folder or song id"))
		return
	}

	added, err := s.favs.Toggle(r.Context(), folderID, songID)
	if err != nil {
		s.logger.Error("toggling favorite", "folder", folderID, "song", songID, "error", err)
		s.renderSheet(w, r, songID, err.Error())
		return
	}

	if sheetMulti(r) {
		// Re-read rather than assume: another phone may have changed the same
		// song between the write and this render.
		s.renderSheet(w, r, songID, "")
	} else {
		// One folder was the whole intent, so the sheet gets out of the way.
		// An empty body clears #sheet; the star and the toast ride along as
		// out-of-band swaps, which is why nothing else is written here.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.starOOB(w, r, songID)
	}

	if name := s.folderName(r, folderID); name != "" {
		if added {
			withToast(w, toastGood, "Added to "+name)
		} else {
			withToast(w, toastInfo, "Removed from "+name)
		}
	}
}

// createFolder adds a folder and returns to the picker.
func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	songID := atoiDefault(r.FormValue("song"), 0)
	name := strings.TrimSpace(r.FormValue("name"))

	_, err := s.favs.CreateFolder(r.Context(), name)
	// A blank or duplicate name is the user's to fix and is reported to them;
	// anything else is ours and worth logging.
	if err != nil && !errors.Is(err, favorites.ErrNoName) && !errors.Is(err, favorites.ErrDuplicateName) {
		s.logger.Error("creating folder", "name", name, "error", err)
	}

	// Two entry points, one creation path. Without a song this came from the
	// folder list, which has no sheet to report into: it re-renders itself and
	// says what happened in a toast. The picker keeps its own error line,
	// because there the message belongs to the form that caused it.
	if songID == 0 {
		s.browse(w, r)
		if err != nil {
			withToast(w, toastBad, err.Error())
		} else {
			withToast(w, toastGood, "Created "+name)
		}
		return
	}

	if err != nil {
		s.renderSheet(w, r, songID, err.Error())
		return
	}
	s.renderSheet(w, r, songID, "")
	withToast(w, toastGood, "Created "+name)
}

// renameFolder changes a folder's name from inside it.
//
// Not gated behind -allow-folder-delete: a rename loses nothing, and the flag
// exists to stop a guest destroying a list rather than to keep it tidy. It does
// change the name a sync code carries, so the other device will stop matching
// this folder automatically — which the receiving page already handles by
// offering the folders that do exist.
func (s *Server) renameFolder(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		s.fail(w, errors.New("bad folder id"))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))

	err = s.favs.RenameFolder(r.Context(), id, name)
	// A blank or duplicate name is the user's to fix; anything else is ours.
	if err != nil && !errors.Is(err, favorites.ErrNoName) && !errors.Is(err, favorites.ErrDuplicateName) {
		s.logger.Error("renaming folder", "folder", id, "name", name, "error", err)
	}

	s.browse(w, r)
	if err != nil {
		withToast(w, toastBad, err.Error())
	} else {
		withToast(w, toastGood, "Renamed to "+name)
	}
}

// removeFavorite takes one song out of the folder being looked at.
//
// Behind the ⋯ toggle and behind a confirmation, for the same reason play-now
// is: a folder is a list someone built, and a mistap beside every row is an
// easy way to quietly lose part of it.
func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	folderID, err1 := strconv.Atoi(r.PathValue("id"))
	songID, err2 := strconv.Atoi(r.PathValue("song"))
	if err1 != nil || err2 != nil {
		s.fail(w, errors.New("bad folder or song id"))
		return
	}

	name := s.folderName(r, folderID)
	if err := s.favs.RemoveSong(r.Context(), folderID, songID); err != nil {
		s.logger.Error("removing a favorite", "folder", folderID, "song", songID, "error", err)
		s.fail(w, err)
		return
	}

	// The whole browse block, not the row: the count beside the search box is
	// part of it and would otherwise go on claiming the song is still there.
	s.browse(w, r)
	if name != "" {
		withToast(w, toastInfo, "Removed from "+name)
	}
}

// deleteFolder removes a folder, when the server was started with that enabled.
func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// Checked here and not only in the template: the route is guessable and no
	// client is authenticated, so hiding the button is not a control.
	if !s.allowFolderDelete {
		http.Error(w, "deleting folders is disabled; start the server with -allow-folder-delete", http.StatusForbidden)
		return
	}

	folderID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		s.fail(w, errors.New("bad folder id"))
		return
	}
	songID := atoiDefault(r.FormValue("song"), 0)
	name := s.folderName(r, folderID)

	if err := s.favs.DeleteFolder(r.Context(), folderID); err != nil {
		if !errors.Is(err, favorites.ErrLastFolder) {
			s.logger.Error("deleting folder", "folder", folderID, "error", err)
		}
		s.renderSheet(w, r, songID, err.Error())
		return
	}
	s.renderSheet(w, r, songID, "")
	withToast(w, toastInfo, "Deleted "+name)
}

// renderSheet writes the picker plus an out-of-band refresh of the row's star,
// so the list behind the sheet stays current without being re-rendered.
func (s *Server) renderSheet(w http.ResponseWriter, r *http.Request, songID int, errMsg string) {
	data := sheetData{
		SongID:      songID,
		Err:         errMsg,
		AllowDelete: s.allowFolderDelete,
		Multi:       sheetMulti(r),
	}

	folders, err := s.favs.Folders(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	data.Folders = folders

	if data.In, err = s.favs.FoldersForSong(r.Context(), songID); err != nil {
		s.fail(w, err)
		return
	}

	if s.songs != nil {
		if song, err := s.songs.Song(r.Context(), songID); err == nil {
			data.Title, data.Artist = song.Title, song.Artist
		}
	}
	if data.Title == "" {
		data.Title = "Song " + strconv.Itoa(songID)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "_sheet.html", data); err != nil {
		s.logger.Error("rendering sheet", "error", err)
		return
	}
	if err := s.tmpl.ExecuteTemplate(w, "_star", starData{
		ID: songID, On: len(data.In) > 0, OOB: true,
	}); err != nil {
		s.logger.Error("rendering star", "error", err)
	}
}

// starOOB refreshes one row's star behind the sheet, for the responses that
// close the sheet instead of re-rendering it and so cannot carry it along.
func (s *Server) starOOB(w http.ResponseWriter, r *http.Request, songID int) {
	in, err := s.favs.FoldersForSong(r.Context(), songID)
	if err != nil {
		s.logger.Error("reading folders for song", "song", songID, "error", err)
		return
	}
	if err := s.tmpl.ExecuteTemplate(w, "_star", starData{
		ID: songID, On: len(in) > 0, OOB: true,
	}); err != nil {
		s.logger.Error("rendering star", "error", err)
	}
}

// sheetMulti reports whether the picker should stay open. Read from the request
// rather than remembered, so every fresh opening starts in the one-tap way.
func sheetMulti(r *http.Request) bool { return r.FormValue("multi") == "1" }

// folderName is for wording a message, so a lookup failure is not worth
// reporting — the caller falls back to saying nothing.
func (s *Server) folderName(r *http.Request, id int) string {
	f, err := s.favs.Folder(r.Context(), id)
	if err != nil {
		return ""
	}
	return f.Name
}
