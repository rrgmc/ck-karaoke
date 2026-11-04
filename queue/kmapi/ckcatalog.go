package kmapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
)

// CK's own routes, outside KaraokeMachine's API. They serve the unit's song
// database as it is, so CK Queue Remote browses and searches the same file
// CK Karaoke downloads from the unit.

// catalogFile is the song database's path and hash. The file does not change
// while the server runs, so the hash is taken once.
func (s *Server) catalogFile() (path, sum string, size int64, err error) {
	path = s.opts.CatalogFile
	if path == "" {
		return "", "", 0, notFound("song database")
	}
	s.catalogHash.Do(func() {
		f, err := os.Open(path)
		if err != nil {
			s.catalogErr = err
			return
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			s.catalogErr = err
			return
		}
		s.catalogSum, s.catalogSize = hex.EncodeToString(h.Sum(nil)), n
	})
	return path, s.catalogSum, s.catalogSize, s.catalogErr
}

func (s *Server) ckCatalog(*http.Request) (int, any, error) {
	_, sum, size, err := s.catalogFile()
	if err != nil {
		return 0, nil, err
	}
	info := s.opts.Catalog.Info()
	c := wire.CKCatalog{DBVersion: info.DBVersion, Songs: info.Songs, Size: size, SHA256: sum}
	if s.opts.Unit != nil {
		c.Model, c.DatabaseID = s.opts.Unit()
	}
	return http.StatusOK, c, nil
}

func (s *Server) ckSongDB(w http.ResponseWriter, r *http.Request) {
	path, sum, _, err := s.catalogFile()
	if err != nil {
		writeError(w, mapError(err))
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, mapError(err))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, mapError(err))
		return
	}
	// ServeContent answers If-None-Match from the ETag, so a remote whose
	// copy matches downloads nothing.
	w.Header().Set("ETag", `"`+sum+`"`)
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	http.ServeContent(w, r, "song.db", st.ModTime(), f)
}
