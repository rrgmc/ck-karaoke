package webui

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"strconv"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/favorites/qrshare"
	"github.com/rrgmc/ck-karaoke/i18n"
	"rsc.io/qr"
)

// syncData drives the pages that carry a folder between two devices.
//
// Every one of them is about one folder, named in the path. Sync is reachable
// only from inside a folder, which is what removed the questions that made this
// confusing: there is no folder to pick when sending and none to pick when
// receiving, because the folder you are in is the answer to both.
type syncData struct {
	// Folder is the folder being synced, on this device.
	Folder favorites.Folder

	// Sending: this folder's code.
	Code string
	// TooDense marks a folder whose code is valid but larger than a QR can
	// hold, at somewhere over a thousand songs. Left undetected this would be a
	// broken image rather than an explanation, since the text underneath still
	// works perfectly well.
	TooDense bool

	// Scanned is what a read code turned out to hold. Its name is shown so a
	// code from the wrong folder is obvious before anything is merged; it does
	// not decide where the songs go, because being inside a folder already did.
	Scanned qrshare.Folder
	// Raw is that same code verbatim, carried to the merge in a hidden field.
	// Separate from Code, which is this folder's own code on the sending side:
	// the two never appear together, but naming them the same thing invites
	// exactly the mix-up that would send the wrong songs somewhere.
	Raw string

	// Back is where "‹ Back" goes, or empty on a screen with nothing above it.
	// Every sync screen also carries an exit straight to the favorites list,
	// because this runs several screens deep and backing out one at a time is
	// tedious when what you want is simply to be done with it.
	Back string

	// Result is a merge that happened, and is nil otherwise.
	Result *mergeResult
	// Err is shown above whatever form caused it.
	Err string
}

// Path is the sync root for this folder, which every screen builds on.
func (d syncData) Path() string { return "/favorites/sync/" + strconv.Itoa(d.Folder.ID) }

// mergeResult is what a merge did, in the terms the person watching cares
// about: what arrived, what was already here, and what this device cannot show.
type mergeResult struct {
	Folder   string
	FolderID int
	Scanned  int
	Added    int
	Already  int
	// Unknown counts ids this catalogue does not carry. They are deliberately
	// not stored: folder counts come from the favorites database while the
	// listing is filtered through the catalogue, so keeping an id nothing can
	// display would make a folder claim more songs than it shows.
	Unknown int
}

// syncFolder resolves the folder every sync screen hangs off, sending anyone
// who arrives with a stale id back to the folder list rather than erroring.
func (s *Server) syncFolder(w http.ResponseWriter, r *http.Request) (favorites.Folder, bool) {
	id, err := strconv.Atoi(r.PathValue("folder"))
	if err != nil {
		s.fail(w, r, errors.New("bad folder id"))
		return favorites.Folder{}, false
	}
	folder, err := s.favs.Folder(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/?mode=favorites", http.StatusSeeOther)
		return favorites.Folder{}, false
	}
	return folder, true
}

// syncChoose asks which device this is. Everything after it shows one side of
// the exchange, because the page that showed both described the scheme instead
// of doing anything.
func (s *Server) syncChoose(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.syncFolder(w, r)
	if !ok {
		return
	}
	s.page(w, r, "sync.html", pageData{
		Title: langOf(r).T("Sync %s", folder.Name),
		Sync: syncData{
			Folder: folder,
			Back:   "/?mode=favorites&folder=" + strconv.Itoa(folder.ID),
		},
	})
}

// syncSend shows this folder's code.
func (s *Server) syncSend(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.syncFolder(w, r)
	if !ok {
		return
	}
	data, err := s.folderCode(r.Context(), langOf(r), folder)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Back = data.Path()
	s.page(w, r, "sync_code.html", pageData{Title: folder.Name, Sync: data})
}

// syncReceive opens the camera.
func (s *Server) syncReceive(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.syncFolder(w, r)
	if !ok {
		return
	}
	data := syncData{Folder: folder}
	data.Back = data.Path()
	s.page(w, r, "sync_receive.html", pageData{Title: langOf(r).T("Receive into %s", folder.Name), Sync: data})
}

// syncScanned shows what was read before anything is written.
//
// The songs are going into the folder this started from, so there is nothing to
// choose here — but the code carries the name of the folder it came from, and
// showing that is how you notice you have scanned the wrong one.
func (s *Server) syncScanned(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.syncFolder(w, r)
	if !ok {
		return
	}
	data := syncData{Folder: folder}

	scanned, err := qrshare.Decode(r.FormValue("code"))
	if err != nil {
		data.Err = errorText(langOf(r), err)
		data.Back = data.Path()
		s.page(w, r, "sync_receive.html", pageData{Title: langOf(r).T("Receive into %s", folder.Name), Sync: data})
		return
	}

	data.Scanned, data.Raw = scanned, r.FormValue("code")
	data.Back = data.Path() + "/receive"
	s.page(w, r, "sync_confirm.html", pageData{Title: langOf(r).T("Received"), Sync: data})
}

// syncMerge files the scanned songs into the folder this started from.
//
// There is no redirect after the post: merging is idempotent, so a reload that
// repeats it changes nothing, and showing the outcome directly is worth more
// than protecting against a resubmission that cannot do harm.
func (s *Server) syncMerge(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.syncFolder(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	data := syncData{Folder: folder, Back: "/favorites/sync/" + strconv.Itoa(folder.ID) + "/receive"}

	scanned, err := qrshare.Decode(r.FormValue("code"))
	if err != nil {
		data.Err = errorText(langOf(r), err)
		data.Back = data.Path()
		s.page(w, r, "sync_receive.html", pageData{Title: langOf(r).T("Receive into %s", folder.Name), Sync: data})
		return
	}
	data.Scanned, data.Raw = scanned, r.FormValue("code")

	// Drop anything this device's catalogue cannot show before it is written,
	// rather than storing it and letting the folder's count drift away from what
	// opening the folder lists.
	known, err := s.knownSongs(ctx, scanned.IDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	keep := make([]int, 0, len(scanned.IDs))
	for _, id := range scanned.IDs {
		if known[id] {
			keep = append(keep, id)
		}
	}

	added, err := s.favs.AddSongs(ctx, folder.ID, keep)
	if err != nil {
		s.logger.Error("merging favorites", "folder", folder.ID, "songs", len(keep), "error", err)
		data.Err = errorText(langOf(r), err)
		s.page(w, r, "sync_confirm.html", pageData{Title: langOf(r).T("Received"), Sync: data})
		return
	}

	s.logger.Info("merged favorites", "folder", folder.Name,
		"scanned", len(scanned.IDs), "added", added, "unknown", len(scanned.IDs)-len(keep))

	s.page(w, r, "sync_done.html", pageData{
		Title: langOf(r).T("Merged"),
		Sync: syncData{Folder: folder, Result: &mergeResult{
			Folder:   folder.Name,
			FolderID: folder.ID,
			Scanned:  len(scanned.IDs),
			Added:    added,
			Already:  len(keep) - added,
			Unknown:  len(scanned.IDs) - len(keep),
		}},
	})
}

// favoritesQR renders one folder's code as a QR image.
func (s *Server) favoritesQR(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.syncFolder(w, r)
	if !ok {
		return
	}
	data, err := s.folderCode(r.Context(), langOf(r), folder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if data.Code == "" || data.TooDense {
		http.Error(w, "that folder cannot be shown as one code", http.StatusBadRequest)
		return
	}

	// Level M rather than L: these are read off a glossy screen at an angle, and
	// 15% recovery costs a few modules against a much better chance of a scan
	// succeeding first time.
	c, err := qr.Encode(data.Code, qr.M)
	if err != nil {
		s.logger.Error("encoding a QR code", "folder", folder.ID, "error", err)
		http.Error(w, "that folder is too large to show as one code", http.StatusBadRequest)
		return
	}
	// Roughly 900 pixels however dense the code turned out, rounded up so that
	// every module is a whole number of pixels.
	//
	// The size is set here rather than left to the page because the browser
	// shows this at around 350 CSS pixels, which on a phone at three device
	// pixels to one is a little over a thousand real ones. A code generated at
	// its natural size would be scaled up tenfold to fill that and read as a
	// blur; one generated far larger would be scaled down, and losing modules to
	// a downscale is worse still.
	//
	// The image is (Size+8)×Scale, not Size×Scale: the library adds the
	// four-module quiet zone the standard requires, and a reader needs it, so
	// the border is part of what has to fit rather than something to trim.
	c.Scale = max(4, (900+quiet(c.Size)-1)/quiet(c.Size))

	w.Header().Set("Content-Type", "image/png")
	// The folder changes as songs are filed, and the URL does not.
	w.Header().Set("Cache-Control", "no-store")
	w.Write(c.PNG())
}

// quiet is a code's width in modules including the quiet zone rsc.io/qr draws
// around it, which is what decides the size of the image it writes.
func quiet(size int) int { return max(size, 1) + 8 }

// folderCode encodes a folder, reporting whether the result is beyond what a QR
// can hold.
func (s *Server) folderCode(ctx context.Context, lang i18n.Lang, folder favorites.Folder) (syncData, error) {
	ids, err := s.favs.SongIDs(ctx, folder.ID)
	if err != nil {
		return syncData{}, err
	}

	data := syncData{Folder: folder}
	if data.Code, err = qrshare.Encode(qrshare.Folder{Name: folder.Name, IDs: ids}); err != nil {
		data.Err = errorText(lang, err)
		return data, nil
	}
	// Asked here as well as in the image handler, because a page that links an
	// image the handler will refuse shows a broken icon and no reason.
	if _, err := qr.Encode(data.Code, qr.M); err != nil {
		data.TooDense = true
	}
	return data, nil
}

// knownSongs reports which of the ids this device's catalogue carries.
func (s *Server) knownSongs(ctx context.Context, ids []int) (map[int]bool, error) {
	found, err := s.lookupSongs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if found == nil {
		// No catalogue to check against, so nothing is rejected.
		out := make(map[int]bool, len(ids))
		for _, id := range ids {
			out[id] = true
		}
		return out, nil
	}
	out := make(map[int]bool, len(found))
	for id := range found {
		out[id] = true
	}
	return out, nil
}

// lookupSongs reads a set of ids out of the catalogue, and returns a nil map
// when there is no catalogue at all — which is not the same as one that carries
// none of them, and callers have to tell the two apart.
//
// In batches, because every id becomes a bound parameter and a whole favorites
// database can be thousands of them.
func (s *Server) lookupSongs(ctx context.Context, ids []int) (map[int]catalog.Song, error) {
	if s.songs == nil {
		return nil, nil
	}
	out := make(map[int]catalog.Song, len(ids))
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		found, err := s.songs.Songs(ctx, ids[start:min(start+batch, len(ids))])
		if err != nil {
			return nil, err
		}
		maps.Copy(out, found)
	}
	return out, nil
}
