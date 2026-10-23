package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// env builds a lookup over a fixed set of variables.
func env(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestLocalZoneStampsTheRightDay(t *testing.T) {
	zone := LocalZone(env(map[string]string{
		EnvZoneName:   "America/Sao_Paulo",
		EnvZoneOffset: "-10800",
	}))
	require.NotNil(t, zone)
	assert.Equal(t, "America/Sao_Paulo", zone.String(), "the name is what gets printed")

	// The case this was written for: 00:02 UTC on the 17th is 21:02 on the 16th
	// in Brazil, and a backup saved then was being named for the wrong day.
	at := time.Date(2026, 8, 17, 0, 2, 26, 0, time.UTC).In(zone)
	assert.Equal(t, "2026-08-16", at.Format("2006-01-02"))
	assert.Equal(t, "21:02", at.Format("15:04"))
}

// Nil means "leave time.Local alone", which is what a desktop wants — there Go
// finds the zone by itself and anything set here would override it.
func TestLocalZoneIsNilWithoutAnOffset(t *testing.T) {
	assert.Nil(t, LocalZone(env(nil)))
	assert.Nil(t, LocalZone(env(map[string]string{EnvZoneName: "America/Sao_Paulo"})),
		"a name alone cannot be resolved without a zoneinfo database")
	assert.Nil(t, LocalZone(env(map[string]string{EnvZoneOffset: "not a number"})))
}

// A wrapper passing minutes or milliseconds by mistake would otherwise invent a
// zone hundreds of hours from UTC, and stamping UTC is the better failure.
func TestLocalZoneRefusesAnImpossibleOffset(t *testing.T) {
	kept := map[string]string{
		"the widest zone there is":          "50400", // +14:00, Kiritimati
		"and the other extreme":             "-50400",
		"an offset that is not whole hours": "20700", // +05:45, Kathmandu
	}
	for why, offset := range kept {
		assert.NotNil(t, LocalZone(env(map[string]string{EnvZoneOffset: offset})), why)
	}

	refused := map[string]string{
		"milliseconds mistaken for seconds": "-10800000",
		"a whole day":                       "86400",
		"minutes mistaken for seconds, far enough out to notice": "-864000",
	}
	for why, offset := range refused {
		assert.Nil(t, LocalZone(env(map[string]string{EnvZoneOffset: offset})), why)
	}
}

// UTC itself has to survive the round trip, or a phone in London would be told
// its zone was unset and fall through to the same UTC by accident rather than on
// purpose — indistinguishable until the day it is not.
func TestLocalZoneAcceptsZero(t *testing.T) {
	zone := LocalZone(env(map[string]string{EnvZoneName: "Europe/London", EnvZoneOffset: "0"}))
	require.NotNil(t, zone)

	_, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, zone).Zone()
	assert.Equal(t, 0, offset)
}

// The name is a label and nothing depends on it, so a missing one must not cost
// the offset that does matter.
func TestLocalZoneWithoutAName(t *testing.T) {
	zone := LocalZone(env(map[string]string{EnvZoneOffset: "-10800"}))
	require.NotNil(t, zone)
	assert.Equal(t, "local", zone.String())

	_, offset := time.Date(2026, 8, 16, 12, 0, 0, 0, zone).Zone()
	assert.Equal(t, -10800, offset)
}
