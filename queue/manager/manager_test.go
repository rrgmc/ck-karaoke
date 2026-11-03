package manager

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/kmapi"
)

func TestSettingsValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Settings)
		err    string
	}{
		{"the defaults", func(*Settings) {}, ""},
		{"turns", func(s *Settings) { s.Order = fair.Turns }, ""},
		{"an unknown order", func(s *Settings) { s.Order = "random" }, "order must be"},
		{"host is never a room level", func(s *Settings) { s.Room = "host" }, "room level"},
		{"a short code", func(s *Settings) { s.QueueCode = "123" }, "at least 4"},
		{"two equal codes", func(s *Settings) { s.QueueCode, s.HostCode = "1234", "1234" }, "are the same"},
		{"an unknown foreign policy", func(s *Settings) { s.Foreign = "drop" }, "foreign song policy"},
		{"too many waiting", func(s *Settings) { s.MaxWaiting = 1001 }, "at most 1000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := DefaultSettings()
			tt.change(&s)
			err := s.Validate()
			if tt.err == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.err)
			}
		})
	}
}

func TestSettingsFillTheBlanksAndRoundTrip(t *testing.T) {
	s := Settings{Name: "Bar"}
	require.NoError(t, s.Validate())
	assert.Equal(t, fair.Arrival, s.Order)
	assert.Equal(t, "request", s.Room, "the default room level asks for songs")

	path := filepath.Join(t.TempDir(), "settings.json")
	s.HostCode = "4321"
	require.NoError(t, SaveSettings(path, s))
	read, err := LoadSettings(path)
	require.NoError(t, err)
	assert.Equal(t, s, read)

	missing, err := LoadSettings(filepath.Join(t.TempDir(), "none.json"))
	require.NoError(t, err)
	assert.Equal(t, DefaultSettings(), missing)

	api := read.API()
	assert.Equal(t, kmapi.Request, api.Room)
	assert.Equal(t, "4321", api.Codes.Host)
}

func TestTheConfigurationPageStaysOnLoopback(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:8176", "localhost:9000", "[::1]:8176"} {
		assert.NoError(t, checkLoopback(ok), ok)
	}
	for _, bad := range []string{":8176", "0.0.0.0:8176", "192.168.1.5:8176"} {
		assert.Error(t, checkLoopback(bad), bad)
	}
}

func TestEveningWords(t *testing.T) {
	assert.True(t, isWord("fd590bf4ef11f152"))
	assert.False(t, isWord(""))
	assert.False(t, isWord("not-a-word"), "KM Remote keeps letters and digits only")
}

func TestKeptHexSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id")
	a, err := keptHex(path, 8)
	require.NoError(t, err)
	assert.Len(t, a, 16)
	b, err := keptHex(path, 8)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}
