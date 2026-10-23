package webui

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rrgmc/ck-karaoke/favorites"
	"github.com/rrgmc/ck-karaoke/favorites/xmlbackup"
)

// Backup carries the whole favorites database out as one XML file, and reads one
// back in.
//
// It is the counterpart of sync rather than a replacement for it, and the two
// stay apart on purpose. Sync moves one folder between two devices in the same
// room and needs no file, no storage and no account. A backup is the whole
// database, going somewhere it can sit for a year — the filesystem, Dropbox,
// Drive — because a phone that is reset or replaced loses its favorites
// outright, and on Android allowBackup="false" means even an uninstall does.
//
// What they share is the guarantee: restoring only ever adds. It creates folders
// it does not find and files songs with `insert or ignore`; there is no delete
// path here any more than there is in sync, and there is not meant to be one.
// So restoring the same file twice is not a case that has been handled, it is
// simply the same insert again.

// maxUpload bounds a restore. An 1,100-song export — past the point a folder
// fits in any QR code — is under 100 KB, so this is two orders of magnitude of
// room and still small enough that a file picked by mistake fails fast.
const maxUpload = 8 << 20

// backupData drives the backup pages.
type backupData struct {
	// Folders and Songs are what a backup taken now would contain, shown before
	// taking one so an empty database says so rather than downloading a file
	// with nothing in it. Empty folders are left out of both, because they are
	// left out of the file — there is nothing in one to carry, and a page
	// promising four folders that writes three is worse than one that says
	// three.
	Folders []favorites.Folder
	Songs   int

	// Filename is what the download is called, and what the Android picker
	// offers as the name to save under.
	Filename string

	// Result is a restore that happened, and is nil otherwise.
	Result *restoreResult
	// Back is where "‹ Back" goes, or empty on a screen with nothing above it.
	Back string
	// Err is shown above whatever form caused it.
	Err string
}

// Path is the backup root, which every screen builds on.
func (d backupData) Path() string { return "/favorites/backup" }

// restoreResult is what a restore did, in the terms the person watching cares
// about.
type restoreResult struct {
	// Folders is how many the file held, Created how many of those were not here.
	Folders int
	Created int
	// Read is the memberships in the file; Added and Already split what became
	// of them.
	Read    int
	Added   int
	Already int
	// Unknown counts ids this catalogue does not carry, dropped for the same
	// reason a merge drops them: folder counts come from the favorites database
	// while the listing is filtered through the catalogue, so keeping an id
	// nothing can display would make a folder claim more songs than it lists.
	Unknown int
}

// backupFilename dates the file, because a backup is a thing you take more than
// once and two called the same name in a downloads folder is a thing you take
// once.
func backupFilename(now time.Time) string {
	return "ck-karaoke-favorites-" + now.Format("2006-01-02") + ".xml"
}

// backupChoose asks what you came here to do. Save and restore are two different
// intentions and this never guesses which, the way the sync pages never guess
// which side of an exchange you are on.
func (s *Server) backupChoose(w http.ResponseWriter, r *http.Request) {
	data := backupData{Filename: backupFilename(time.Now()), Back: "/?mode=favorites"}

	folders, err := s.favs.Folders(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, f := range folders {
		if f.Songs == 0 {
			continue
		}
		data.Folders = append(data.Folders, f)
		data.Songs += f.Songs
	}

	s.page(w, r, "backup.html", pageData{Title: "Backup", Backup: data})
}

// backupExport writes every folder and every song in it.
//
// The response is an attachment, and that is load-bearing rather than
// decorative: it is the header both app wrappers key off to turn this into a
// file the phone can put somewhere. In an ordinary browser it is what makes the
// page save rather than render.
func (s *Server) backupExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	folders, err := s.favs.Folders(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}

	out := xmlbackup.Favorites{
		Exported:  time.Now().UTC().Format(time.RFC3339),
		Catalogue: s.catalogue().ProjectName,
	}
	for _, folder := range folders {
		ids, err := s.favs.SongIDs(ctx, folder.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if len(ids) == 0 {
			continue // an empty folder carries nothing and restores to nothing
		}
		known, err := s.lookupSongs(ctx, ids)
		if err != nil {
			s.fail(w, err)
			return
		}

		songs := make([]xmlbackup.Song, 0, len(ids))
		for _, id := range ids {
			// A song the catalogue cannot name is still written, id alone.
			// Export preserves and import filters, and the asymmetry is
			// deliberate: dropping an id here would lose it for good, whereas
			// storing one at restore is the count drift above.
			song := xmlbackup.Song{ID: id}
			if found, ok := known[id]; ok {
				song.Title, song.Artist = found.Title, found.Artist
			}
			songs = append(songs, song)
		}
		out.Folders = append(out.Folders, xmlbackup.Folder{Name: folder.Name, Songs: songs})
	}

	name := backupFilename(time.Now())
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	// The favorites change as songs are filed, and the URL does not.
	w.Header().Set("Cache-Control", "no-store")

	if err := xmlbackup.Marshal(w, out); err != nil {
		// Too late for a page: the headers are out and a browser is already
		// writing a file. Logging is the whole of what is left to do, and the
		// truncated file is what tells the user.
		s.logger.Error("writing a favorites backup", "error", err)
		return
	}
	s.logger.Info("wrote a favorites backup", "folders", len(out.Folders), "songs", out.Songs())
}

// backupImport shows the file picker.
func (s *Server) backupImport(w http.ResponseWriter, r *http.Request) {
	data := backupData{Back: "/favorites/backup"}
	s.page(w, r, "backup_import.html", pageData{Title: "Restore favorites", Backup: data})
}

// backupRestore files everything in the uploaded file.
//
// There is no confirmation step and no redirect afterwards, for the reason
// syncMerge has neither: the write is add-only and idempotent, so a reload that
// repeats it changes nothing, and showing the outcome directly is worth more
// than guarding against a resubmission that cannot do harm.
func (s *Server) backupRestore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := backupData{Back: "/favorites/backup"}

	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	file, header, err := r.FormFile("file")
	if err != nil {
		data.Err = uploadError(err)
		s.page(w, r, "backup_import.html", pageData{Title: "Restore favorites", Backup: data})
		return
	}
	defer file.Close()

	read, err := xmlbackup.Parse(file)
	if err != nil {
		data.Err = err.Error()
		s.page(w, r, "backup_import.html", pageData{Title: "Restore favorites", Backup: data})
		return
	}

	result := restoreResult{Folders: len(read.Folders), Read: read.Songs()}
	for _, folder := range read.Folders {
		target, created, err := s.favs.EnsureFolder(ctx, folder.Name)
		if err != nil {
			s.logger.Error("restoring a favorites backup", "folder", folder.Name, "error", err)
			data.Err = err.Error()
			s.page(w, r, "backup_import.html", pageData{Title: "Restore favorites", Backup: data})
			return
		}
		if created {
			result.Created++
		}

		ids := make([]int, len(folder.Songs))
		for i, song := range folder.Songs {
			ids[i] = song.ID
		}
		known, err := s.knownSongs(ctx, ids)
		if err != nil {
			s.fail(w, err)
			return
		}
		keep := make([]int, 0, len(ids))
		for _, id := range ids {
			if known[id] {
				keep = append(keep, id)
			}
		}
		result.Unknown += len(ids) - len(keep)

		added, err := s.favs.AddSongs(ctx, target.ID, keep)
		if err != nil {
			s.logger.Error("restoring a favorites backup", "folder", folder.Name, "error", err)
			data.Err = err.Error()
			s.page(w, r, "backup_import.html", pageData{Title: "Restore favorites", Backup: data})
			return
		}
		result.Added += added
		result.Already += len(keep) - added
	}

	name := "a favorites file"
	if header != nil && header.Filename != "" {
		name = header.Filename
	}
	s.logger.Info("restored a favorites backup", "file", name,
		"folders", result.Folders, "created", result.Created,
		"read", result.Read, "added", result.Added, "unknown", result.Unknown)

	data.Result = &result
	s.page(w, r, "backup_done.html", pageData{Title: "Restored", Backup: data})
}

// uploadError words the two ways picking a file goes wrong, because neither of
// the underlying messages is worth showing to anyone.
func uploadError(err error) string {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return fmt.Sprintf("that file is larger than %d MB, so it is not a favorites backup", maxUpload>>20)
	}
	if errors.Is(err, http.ErrMissingFile) {
		return "no file was chosen"
	}
	return "that file could not be read"
}
