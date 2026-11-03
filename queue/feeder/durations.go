package feeder

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MinLearned is the shortest length worth learning. A song that ends sooner
// was most likely cut short by a command the feeder did not see.
const MinLearned = 30 * time.Second

// Durations holds song lengths learned from playback. The unit's catalogue has
// no durations, so a song's length is known only after it has played to its
// end once: the last position the status reported is its length.
type Durations struct {
	mu   sync.Mutex
	path string
	ms   map[uint32]uint32
}

// OpenDurations reads the learned lengths kept at path. An empty path keeps
// them in memory only; a missing file starts empty.
func OpenDurations(path string) (*Durations, error) {
	d := &Durations{path: path, ms: map[uint32]uint32{}}
	if path == "" {
		return d, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &d.ms); err != nil {
		// A damaged file only loses what was learned.
		d.ms = map[uint32]uint32{}
	}
	return d, nil
}

// Get returns a song's length in milliseconds, or 0 when it is not known.
func (d *Durations) Get(number uint32) uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ms[number]
}

// Len returns how many lengths are known.
func (d *Durations) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ms)
}

// Learn records a length from a song that played to its end, and writes the
// file when anything changed. It reports whether the length was taken.
func (d *Durations) Learn(number uint32, length time.Duration) (bool, error) {
	if length < MinLearned || number == 0 {
		return false, nil
	}
	ms := uint32(length.Milliseconds())
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ms[number] == ms {
		return false, nil
	}
	d.ms[number] = ms
	if d.path == "" {
		return true, nil
	}
	data, err := json.Marshal(d.ms)
	if err != nil {
		return true, err
	}
	tmp := d.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
		return true, err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return true, err
	}
	return true, os.Rename(tmp, d.path)
}
