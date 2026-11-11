package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The file sits on disk between runs, so a truncated or edited one must not become
// something the client tries to dial.
func TestRememberedAddressIsValidated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device.addr")

	for _, bad := range []string{
		"", "   ", "not-an-address", "192.168.68.62", // no port
		"192.168.68.", "999.1.1.1:35379", ":35379",
	} {
		require.NoError(t, os.WriteFile(path, []byte(bad), 0o644))
		assert.Empty(t, readAddr(path), "should reject %q", bad)
	}

	require.NoError(t, os.WriteFile(path, []byte("  192.168.68.62:35379\n"), 0o644))
	assert.Equal(t, "192.168.68.62:35379", readAddr(path), "and accept a good one, trimmed")
}

func TestReadAddrOnMissingFile(t *testing.T) {
	assert.Empty(t, readAddr(filepath.Join(t.TempDir(), "absent")))
}

func TestRememberAddrRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.addr")

	rememberAddr(path, "192.168.68.62:35379", quietLogger())
	assert.Equal(t, "192.168.68.62:35379", readAddr(path))

	// A move to a new address must replace the old one, not append to it.
	rememberAddr(path, "192.168.68.63:35379", quietLogger())
	assert.Equal(t, "192.168.68.63:35379", readAddr(path))

	// And no temporary files left lying beside it.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

// An unwritable location costs the next start its fallback and nothing more.
func TestRememberAddrSurvivesAnUnwritableDirectory(t *testing.T) {
	assert.NotPanics(t, func() {
		rememberAddr(filepath.Join(t.TempDir(), "no-such-dir", "device.addr"),
			"192.168.68.62:35379", quietLogger())
	})
}
