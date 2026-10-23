// Package catalog downloads, verifies and queries the device's song database.
//
// The database is a plain SQLite file. The device streams it as a small number
// of very large frames, each carrying an opaque header before its slice of the
// file.
package catalog

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// sqliteMagic opens every SQLite database file.
const sqliteMagic = "SQLite format 3\x00"

// maxHeaderLen bounds the per-chunk header. Typical values are 72 and 75; a
// value far outside that means the framing is not what we think it is, and
// guessing further would corrupt the file silently.
const maxHeaderLen = 256

// Reassembler rebuilds the database file from the device's chunks.
//
// Each chunk is <header><slice of file>, and the header length varies between
// chunks: typically 72 bytes for the first and 75 for later ones. Nothing in
// the header encodes the payload length, so stripping a fixed offset drifts and
// corrupts the file.
//
// So the boundaries are derived from the payload instead:
//
//   - The first chunk's payload starts at the SQLite magic, which gives its
//     header length exactly.
//   - The SQLite header then gives the file's true total size
//     (page size × page count).
//   - The first chunk's payload length is the device's maximum chunk size, so
//     every later chunk carries min(max, remaining) bytes, and its header is
//     whatever precedes that.
//
// Every quantity is measured rather than assumed, and Finish cross-checks the
// result against the size the file declares for itself.
type Reassembler struct {
	out       []byte
	total     int
	maxChunk  int
	chunkSeen int
}

// NewReassembler returns a Reassembler ready for the first chunk.
func NewReassembler() *Reassembler { return &Reassembler{} }

// Add consumes one raw chunk frame, including its leading 0x4B id.
func (r *Reassembler) Add(frame []byte) error {
	r.chunkSeen++

	if r.total == 0 {
		return r.addFirst(frame)
	}

	remaining := r.total - len(r.out)
	if remaining <= 0 {
		return fmt.Errorf("chunk %d arrived after the file was complete", r.chunkSeen)
	}
	payload := min(r.maxChunk, remaining)
	header := len(frame) - payload
	if header < 0 {
		return fmt.Errorf("chunk %d is %d bytes, too short for a %d byte payload",
			r.chunkSeen, len(frame), payload)
	}
	if header > maxHeaderLen {
		return fmt.Errorf("chunk %d implies a %d byte header, beyond the %d byte limit",
			r.chunkSeen, header, maxHeaderLen)
	}
	r.out = append(r.out, frame[header:]...)
	return nil
}

// addFirst locates the payload by the SQLite magic and reads the file's declared
// size from the database header.
func (r *Reassembler) addFirst(frame []byte) error {
	header := indexOf(frame, sqliteMagic, maxHeaderLen)
	if header < 0 {
		return errors.New("first chunk has no SQLite header; the device did not send a database")
	}
	payload := frame[header:]
	if len(payload) < 100 {
		return fmt.Errorf("first chunk carries only %d bytes, too few for a SQLite header", len(payload))
	}

	total, err := declaredSize(payload)
	if err != nil {
		return err
	}
	r.total = total
	r.maxChunk = len(payload)
	r.out = append(r.out, payload...)
	return nil
}

// Finish returns the assembled file, checking it against the size the database
// declares for itself.
func (r *Reassembler) Finish() ([]byte, error) {
	if r.total == 0 {
		return nil, errors.New("no chunks received")
	}
	if len(r.out) != r.total {
		return nil, fmt.Errorf("assembled %d bytes but the database declares %d (%d chunks)",
			len(r.out), r.total, r.chunkSeen)
	}
	return r.out, nil
}

// Size reports the declared total and how much has arrived, for progress.
func (r *Reassembler) Size() (have, total int) { return len(r.out), r.total }

// declaredSize reads page size × page count from a SQLite header. Together these
// give the file's exact length, which is what makes the reassembly checkable.
func declaredSize(db []byte) (int, error) {
	if len(db) < 100 {
		return 0, errors.New("short SQLite header")
	}
	pageSize := int(binary.BigEndian.Uint16(db[16:18]))
	if pageSize == 1 {
		// 1 is the escape for a 65536-byte page.
		pageSize = 65536
	}
	if pageSize < 512 || pageSize&(pageSize-1) != 0 {
		return 0, fmt.Errorf("implausible SQLite page size %d", pageSize)
	}
	pages := int(binary.BigEndian.Uint32(db[28:32]))
	if pages <= 0 {
		return 0, fmt.Errorf("implausible SQLite page count %d", pages)
	}
	return pageSize * pages, nil
}

// indexOf finds needle within the first limit bytes of haystack.
func indexOf(haystack []byte, needle string, limit int) int {
	end := min(len(haystack), limit+len(needle))
	for i := 0; i+len(needle) <= end; i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return i
		}
	}
	return -1
}
