package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The VERSION file has to be valid, or every build that reads it breaks.
func TestVersionFileIsValid(t *testing.T) {
	_, err := code(Version)
	require.NoError(t, err)
	assert.NotPanics(t, func() { Code() })
}

func TestCode(t *testing.T) {
	for v, want := range map[string]int{
		"0.9.0": 900, "1.2.3": 10203, "1.2.3-rc1": 10203, "12.0.99": 120099,
	} {
		got, err := code(v)
		require.NoError(t, err, v)
		assert.Equal(t, want, got, v)
	}
	for _, v := range []string{"1.2", "v1.2.3", "1.100.0", "1.2.3-", ""} {
		_, err := code(v)
		assert.Error(t, err, v)
	}
}

func TestNumeric(t *testing.T) {
	saved := Version
	t.Cleanup(func() { Version = saved })
	Version = "1.2.3-rc1"
	assert.Equal(t, "1.2.3", Numeric())
}
