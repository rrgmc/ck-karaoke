package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixture writes a remembered address and a stand-in catalogue, and returns both
// paths. The catalogue only has to exist — fallbackAddr does not open it.
func fixture(t *testing.T, addr string) (lastAddrFile, songDB string) {
	t.Helper()
	dir := t.TempDir()
	lastAddrFile = filepath.Join(dir, "device.addr")
	songDB = filepath.Join(dir, "song.db")
	if addr != "" {
		require.NoError(t, os.WriteFile(lastAddrFile, []byte(addr+"\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(songDB, []byte("not really sqlite"), 0o644))
	return lastAddrFile, songDB
}

func TestFallbackUsesTheRememberedAddress(t *testing.T) {
	last, songs := fixture(t, "192.168.68.62:35379")
	assert.Equal(t, "192.168.68.62:35379", fallbackAddr(client.ErrNoDevice, last, songs))
}

// The catalogue is the only thing that genuinely needs the device, so without one
// there is nothing to fall back to and the failure should surface as itself.
func TestFallbackRefusedWithoutACatalogue(t *testing.T) {
	last, songs := fixture(t, "192.168.68.62:35379")
	require.NoError(t, os.Remove(songs))
	assert.Empty(t, fallbackAddr(client.ErrNoDevice, last, songs))
}

func TestFallbackRefusedWithNothingRemembered(t *testing.T) {
	last, songs := fixture(t, "")
	assert.Empty(t, fallbackAddr(client.ErrNoDevice, last, songs))
}

// Only a silent network justifies carrying on. Anything else is a real fault and
// hiding it behind a stale address would only move the failure somewhere worse.
func TestFallbackRefusedForOtherFailures(t *testing.T) {
	last, songs := fixture(t, "192.168.68.62:35379")
	for _, err := range []error{
		errors.New("opening discovery socket: permission denied"),
		fmt.Errorf("listing interfaces to sweep: %w", os.ErrPermission),
	} {
		assert.Empty(t, fallbackAddr(err, last, songs), err)
	}
}

func TestFallbackDisabledWhenNoFileConfigured(t *testing.T) {
	_, songs := fixture(t, "192.168.68.62:35379")
	assert.Empty(t, fallbackAddr(client.ErrNoDevice, "", songs))
}

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
