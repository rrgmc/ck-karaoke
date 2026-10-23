package catalog

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkFrames splits a database file the way the device sends it: each frame
// is the 0x4B id, an opaque header, and a slice of the file. The header is 72
// bytes (id included) on the first chunk and 75 on the rest, and nothing in it
// gives the payload length.
func chunkFrames(db []byte, maxChunk int) [][]byte {
	var out [][]byte
	for off := 0; off < len(db); off += maxChunk {
		header := 75
		if off == 0 {
			header = 72
		}
		end := min(off+maxChunk, len(db))
		fr := append([]byte{0x4B}, bytes.Repeat([]byte{0xAA}, header-1)...)
		out = append(out, append(fr, db[off:end]...))
	}
	return out
}

// Reassembly must reproduce the file byte for byte despite the varying header
// length. A fixed offset still yields a file that opens and lists songs, so
// only byte equality and the integrity check catch it.
func TestReassembleReproducesDatabase(t *testing.T) {
	want := readFixture(t)
	frames := chunkFrames(want, 1500)
	require.Greater(t, len(frames), 2, "the fixture should span several chunks")

	r := NewReassembler()
	for _, fr := range frames {
		require.NoError(t, r.Add(fr))
	}
	got, err := r.Finish()
	require.NoError(t, err)
	assert.True(t, bytes.Equal(got, want), "reassembled %d bytes, want %d", len(got), len(want))

	path := filepath.Join(t.TempDir(), "song.db")
	require.NoError(t, os.WriteFile(path, got, 0o644))
	assert.NoError(t, Verify(path))
}

// A fixed header length is the mistake the reassembler exists to avoid: it
// shifts every chunk after the first by three bytes.
func TestFixedOffsetCorrupts(t *testing.T) {
	want := readFixture(t)
	var got []byte
	for _, fr := range chunkFrames(want, 1500) {
		got = append(got, fr[72:]...)
	}
	assert.False(t, bytes.Equal(got, want))
}

func TestReassemblerRejectsExtraChunk(t *testing.T) {
	db := readFixture(t)
	r := NewReassembler()
	for _, fr := range chunkFrames(db, 1500) {
		require.NoError(t, r.Add(fr))
	}
	assert.Error(t, r.Add(chunkFrames(db, 1500)[1]), "a chunk after the file was complete")
}

func TestDeclaredSize(t *testing.T) {
	// Page size 1024 (0x0400) at offset 16, page count 2169 at offset 28.
	hdr := make([]byte, 100)
	copy(hdr, sqliteMagic)
	hdr[16], hdr[17] = 0x04, 0x00
	hdr[28], hdr[29], hdr[30], hdr[31] = 0x00, 0x00, 0x08, 0x79

	got, err := declaredSize(hdr)
	if err != nil {
		t.Fatalf("declaredSize: %v", err)
	}
	if want := 1024 * 2169; got != want {
		t.Errorf("declaredSize = %d, want %d", got, want)
	}

	// A page size of 1 escapes to 65536 rather than meaning one byte.
	hdr[16], hdr[17] = 0x00, 0x01
	if got, err := declaredSize(hdr); err != nil || got != 65536*2169 {
		t.Errorf("page size escape: got %d, %v", got, err)
	}

	// Nonsense must be rejected, not silently accepted.
	hdr[16], hdr[17] = 0x00, 0x03 // not a power of two
	if _, err := declaredSize(hdr); err == nil {
		t.Error("declaredSize accepted a page size of 3")
	}
}

func TestReassemblerRejectsNonDatabase(t *testing.T) {
	r := NewReassembler()
	if err := r.Add(append([]byte{0x4B}, bytes.Repeat([]byte{0}, 512)...)); err == nil {
		t.Error("accepted a first chunk with no SQLite header")
	}
	if _, err := r.Finish(); err == nil {
		t.Error("Finish accepted an empty reassembly")
	}
}
