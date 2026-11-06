package webui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/favorites/qrshare"
	"github.com/rrgmc/ck-karaoke/favorites/xmlbackup"
)

// CK Karaoke and CK Queue Remote keep separate favorites on a phone. These
// routes receive favorites sent from the other app, on demand, in the two
// shapes the apps already exchange between devices: one folder as a folder
// code, and everything as a backup file.
//
// The sending side is a ckpeer: link on the sync and backup pages, shown only
// when Options.PeerApp is set. The phone port handles the link and opens the
// other app, whose port calls these routes:
//
//   - ckpeer:folder?code=<code> opens GET /favorites/sync/incoming?code=<code>.
//   - ckpeer:backup makes the port fetch /favorites/backup.xml, and the other
//     app's port posts it to POST /favorites/backup/incoming and then opens
//     GET /favorites/backup/incoming.
//
// Both only add, like the sync and backup they reuse, and both ask before
// they write anything.

// incoming is a backup sent from the other app, held until it is confirmed.
// There is one at most: a later one replaces it.
type incoming struct {
	mu     sync.Mutex
	backup *xmlbackup.Favorites
	err    error
}

func (in *incoming) set(b *xmlbackup.Favorites, err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.backup, in.err = b, err
}

func (in *incoming) get() (*xmlbackup.Favorites, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.backup, in.err
}

// take returns the held backup and forgets it.
func (in *incoming) take() *xmlbackup.Favorites {
	in.mu.Lock()
	defer in.mu.Unlock()
	b := in.backup
	in.backup, in.err = nil, nil
	return b
}

// errNothingIncoming is what the confirm page says when nothing was sent, or
// it was already imported.
var errNothingIncoming = errors.New("nothing was received")

// backupIncomingPost holds a backup the port posts as the request body.
//
// The answer is 204 whether or not the file parses. The port opens the
// confirm page next either way, and that page shows the error in the page's
// language.
func (s *Server) backupIncomingPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	read, err := xmlbackup.Parse(r.Body)
	if err != nil {
		s.logger.Warn("reading favorites sent from the other app", "error", err)
		s.incoming.set(nil, err)
	} else {
		s.logger.Info("received favorites from the other app",
			"folders", len(read.Folders), "songs", read.Songs())
		s.incoming.set(&read, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

// backupIncoming shows what the other app sent, and asks before adding it.
func (s *Server) backupIncoming(w http.ResponseWriter, r *http.Request) {
	data := backupData{Back: "/favorites/backup"}
	backup, err := s.incoming.get()
	switch {
	case err != nil:
		data.Err = errorText(langOf(r), err)
	case backup == nil:
		data.Err = errorText(langOf(r), errNothingIncoming)
	default:
		data.Incoming = backup
	}
	s.page(w, r, "backup_incoming.html", pageData{
		Title:  langOf(r).T("Received"),
		Backup: data,
	})
}

// backupIncomingImport adds the held backup and shows the same result as a
// restore from a file.
func (s *Server) backupIncomingImport(w http.ResponseWriter, r *http.Request) {
	backup := s.incoming.take()
	if backup == nil {
		http.Redirect(w, r, "/favorites/backup/incoming", http.StatusSeeOther)
		return
	}
	data := backupData{Back: "/favorites/backup"}
	result, err := s.restoreAll(r.Context(), *backup)
	if err != nil {
		s.logger.Error("importing favorites from the other app", "error", err)
		data.Err = errorText(langOf(r), err)
		data.Incoming = backup
		s.incoming.set(backup, nil)
		s.page(w, r, "backup_incoming.html", pageData{
			Title:  langOf(r).T("Received"),
			Backup: data,
		})
		return
	}
	s.logger.Info("imported favorites from the other app",
		"folders", result.Folders, "created", result.Created,
		"read", result.Read, "added", result.Added, "unknown", result.Unknown)
	data.Result = &result
	s.page(w, r, "backup_done.html", pageData{Title: langOf(r).T("Restored"), Backup: data})
}

// backupIncomingCancel forgets the held backup.
func (s *Server) backupIncomingCancel(w http.ResponseWriter, r *http.Request) {
	s.incoming.take()
	http.Redirect(w, r, "/?mode=favorites", http.StatusSeeOther)
}

// folderIncoming shows a folder code sent from the other app and asks which
// folder here to add it to.
//
// A scanned code goes into the folder the scan started from. A sent code
// starts from no folder, so this page offers them all, with the one of the
// same name chosen. If none has that name, it also offers a new folder with
// it; creating one is the user's choice here, never the code's.
func (s *Server) folderIncoming(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := syncData{Back: "/?mode=favorites"}
	title := langOf(r).T("Received")

	code := r.FormValue("code")
	scanned, err := qrshare.Decode(code)
	if err != nil {
		data.Err = errorText(langOf(r), err)
		s.page(w, r, "sync_incoming.html", pageData{Title: title, Sync: data})
		return
	}
	data.Scanned, data.Raw = scanned, code

	folders, err := s.favs.Folders(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Folders = folders
	data.NewName = scanned.Name
	for _, f := range folders {
		if f.Name == scanned.Name {
			data.Folder = f
			data.NewName = ""
			break
		}
	}
	s.page(w, r, "sync_incoming.html", pageData{Title: title, Sync: data})
}

// folderIncomingMerge adds a sent folder code to the folder chosen for it,
// creating that folder first when the choice was a new one.
func (s *Server) folderIncomingMerge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.FormValue("code")
	back := "/favorites/sync/incoming?code=" + url.QueryEscape(code)

	var folder favorites.Folder
	if choice := r.FormValue("folder"); choice == "new" {
		scanned, err := qrshare.Decode(code)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		folder, _, err = s.favs.EnsureFolder(ctx, scanned.Name)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	} else {
		id, err := strconv.Atoi(choice)
		if err != nil {
			s.fail(w, r, errors.New("no folder was chosen"))
			return
		}
		if folder, err = s.favs.Folder(ctx, id); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.mergeInto(w, r, folder, code, back)
}
