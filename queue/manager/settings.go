package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
)

// Settings is what the operator configures. It is kept as settings.json in
// the data directory and edited on the configuration page.
type Settings struct {
	// Unit picks the karaoke unit by model and database id, which stay the
	// same when its DHCP address changes. Empty fields match any unit.
	Unit UnitChoice `json:"unit"`
	// Name is what remotes list this server as.
	Name string `json:"name"`
	// Order is arrival or turns.
	Order fair.Order `json:"order"`
	// MaxWaiting is how many songs one singer may have waiting.
	MaxWaiting uint32 `json:"max_waiting"`
	// NameRequired makes every singer give a name.
	NameRequired bool `json:"name_required"`
	// MaxPerDevice is how many singers one phone may hold; 0 is no limit.
	MaxPerDevice uint32 `json:"max_per_device"`
	// Room is the level everybody has without a code.
	Room string `json:"room"`
	// The access codes. Empty means not set.
	QueueCode   string `json:"queue_code"`
	ControlCode string `json:"control_code"`
	HostCode    string `json:"host_code"`
	// Foreign is what happens to songs queued on the unit by anyone else.
	Foreign feeder.ForeignPolicy `json:"foreign"`
	// KeepEvening keeps the evening across a restart, holding playback until
	// a host presses Play.
	KeepEvening bool `json:"keep_evening"`
}

// UnitChoice identifies a unit.
type UnitChoice struct {
	Model      string `json:"model"`
	DatabaseID string `json:"database_id"`
	// Addr is where it was last found; it is tried first.
	Addr string `json:"addr"`
}

// DefaultSettings are the settings of a first start.
func DefaultSettings() Settings {
	return Settings{
		Name: "Karaoke", Order: fair.Arrival, MaxWaiting: fair.DefaultRequestRules.MaxWaiting,
		Room: kmapi.Request.String(), Foreign: feeder.Adopt,
	}
}

// Validate checks the settings and fills in what is missing.
func (s *Settings) Validate() error {
	d := DefaultSettings()
	if s.Order == "" {
		s.Order = d.Order
	}
	if s.Order != fair.Arrival && s.Order != fair.Turns {
		return fmt.Errorf("order must be arrival or turns, not %q", s.Order)
	}
	if s.Room == "" {
		s.Room = d.Room
	}
	room, err := kmapi.ParseLevel(s.Room)
	if err != nil || room > kmapi.Control {
		return fmt.Errorf("the room level must be view, request, queue or control, not %q", s.Room)
	}
	if s.Foreign == "" {
		s.Foreign = d.Foreign
	}
	if s.Foreign != feeder.Adopt && s.Foreign != feeder.Reroute {
		return fmt.Errorf("the foreign song policy must be adopt or reroute, not %q", s.Foreign)
	}
	codes := map[string]string{}
	for level, code := range map[string]string{"queue": s.QueueCode, "control": s.ControlCode, "host": s.HostCode} {
		if code == "" {
			continue
		}
		if len(code) < 4 {
			return fmt.Errorf("the %s code needs at least 4 characters", level)
		}
		if other, dup := codes[code]; dup {
			return fmt.Errorf("the %s and %s codes are the same", other, level)
		}
		codes[code] = level
	}
	if s.MaxWaiting > 1000 {
		return errors.New("a singer may have at most 1000 songs waiting")
	}
	return nil
}

// API returns the settings as the API server takes them.
func (s Settings) API() kmapi.Config {
	room, _ := kmapi.ParseLevel(s.Room)
	return kmapi.Config{
		Name:  s.Name,
		Room:  room,
		Codes: kmapi.Codes{Queue: s.QueueCode, Control: s.ControlCode, Host: s.HostCode},
		Seats: fair.SeatRules{NameRequired: s.NameRequired, MaxPerDevice: s.MaxPerDevice},
		Rules: fair.RequestRules{MaxWaiting: s.MaxWaiting},
	}
}

// LoadSettings reads settings.json, or returns the defaults when there is none.
func LoadSettings(path string) (Settings, error) {
	s := DefaultSettings()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("reading %s: %w", path, err)
	}
	return s, s.Validate()
}

// SaveSettings writes settings.json whole. The file holds the access codes, so
// only its owner may read it.
func SaveSettings(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'), 0o600)
}

// writeFile replaces a file through a temporary one and a rename.
func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Windows ignores most of the bits; the file keeps its default access there.
	_ = tmp.Chmod(perm)
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
