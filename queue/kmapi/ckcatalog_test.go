package kmapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
)

func TestCKCatalogIsMissingWithoutAFile(t *testing.T) {
	r := newRig(t, Config{Room: View})

	for _, path := range []string{"/ck/catalog", "/ck/catalog/song.db"} {
		status, b := r.do("GET", path, "", nil)
		assert.Equal(t, http.StatusNotFound, status, path)
		var e wire.Error
		require.NoError(t, json.Unmarshal(b, &e), path)
		assert.Equal(t, "not_found", e.Error, path)
	}
}

func TestCKCatalogDescribesAndServesTheFile(t *testing.T) {
	r := newRig(t, Config{Room: View})
	content := []byte("SQLite format 3\x00 and the rest of the song database")
	path := filepath.Join(t.TempDir(), "song.db")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	r.srv.opts.CatalogFile = path
	r.srv.opts.Unit = func() (string, string) { return "MK8513-BR-B", "d91c983b" }

	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	var c wire.CKCatalog
	r.ok(200, "GET", "/ck/catalog", "", nil, &c)
	info := r.srv.opts.Catalog.Info()
	assert.Equal(t, wire.CKCatalog{
		Model: "MK8513-BR-B", DatabaseID: "d91c983b", DBVersion: info.DBVersion, Songs: info.Songs,
		Size: int64(len(content)), SHA256: want,
	}, c)

	resp, err := http.Get(r.http.URL + "/api/v1/ck/catalog/song.db")
	require.NoError(t, err)
	b := readAll(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, content, b)
	assert.Equal(t, `"`+want+`"`, resp.Header.Get("ETag"))

	req, err := http.NewRequest("GET", r.http.URL+"/api/v1/ck/catalog/song.db", nil)
	require.NoError(t, err)
	req.Header.Set("If-None-Match", `"`+want+`"`)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Empty(t, readAll(t, resp))
	assert.Equal(t, http.StatusNotModified, resp.StatusCode, "a copy that matches downloads nothing")
}

func TestCKCatalogIsOpenAtTheViewLevel(t *testing.T) {
	r := newRig(t, Config{Room: View, Codes: Codes{Queue: "1111", Control: "2222", Host: "3333"}})
	path := filepath.Join(t.TempDir(), "song.db")
	require.NoError(t, os.WriteFile(path, []byte("db"), 0o600))
	r.srv.opts.CatalogFile = path

	status, _ := r.do("GET", "/ck/catalog/song.db", "", nil)
	assert.Equal(t, http.StatusOK, status)
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}
