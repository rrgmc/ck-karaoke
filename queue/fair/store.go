package fair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// The evening on disk: one JSON file that holds the queue, the seats with their
// tokens, the requests, the record of what was sung and the evening's word. A
// server that starts again finds the night where it stopped, and every phone
// keeps its seat.
//
// A file this build cannot read is set aside as "<name>.bad", and the evening
// starts empty: a lost queue costs less than a server that will not start.
//
// A seat's token is written as it is. It proves one singer's seat for one
// evening and nothing else.

// EveningVersion is the file version this build writes and reads.
const EveningVersion = 1

// Kept is what one write of the evening carries.
type Kept struct {
	// Word names the evening. A phone keeps it beside its seats, and a new
	// word tells it that its seats are gone.
	Word    string   `json:"word"`
	Evening Snapshot `json:"evening"`
}

type eveningFile struct {
	Version int `json:"version"`
	Kept
}

// Load reads an evening file. It returns false when there is no file or when
// the file could not be used; an unusable file is renamed to "<path>.bad".
func Load(path string, logger *slog.Logger) (Kept, bool) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Kept{}, false
	}
	if err != nil {
		logger.Error("the kept evening could not be opened; starting an empty evening", "error", err)
		return Kept{}, false
	}
	var f eveningFile
	if err := json.Unmarshal(data, &f); err != nil {
		setAside(path, err.Error(), logger)
		return Kept{}, false
	}
	if f.Version != EveningVersion {
		setAside(path, fmt.Sprintf("it is in version %d, and this build reads %d", f.Version, EveningVersion), logger)
		return Kept{}, false
	}
	return f.Kept, true
}

// Write writes the evening file whole, through a temporary file and a rename,
// so a power cut leaves the evening before or the evening after.
func Write(path string, kept Kept) error {
	data, err := json.MarshalIndent(eveningFile{Version: EveningVersion, Kept: kept}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func setAside(path, reason string, logger *slog.Logger) {
	bad := path + ".bad"
	logger.Warn("a kept evening could not be used, and it was set aside", "reason", reason, "kept", bad)
	_ = os.Rename(path, bad)
}
