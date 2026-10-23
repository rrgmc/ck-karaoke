package server

import (
	"strconv"
	"time"
)

// Environment variables the phone wrappers set before the server starts.
const (
	// EnvZoneName is the IANA name of the phone's time zone, for reading.
	EnvZoneName = "CK_TZ"
	// EnvZoneOffset is that zone's offset from UTC, in seconds, for arithmetic.
	EnvZoneOffset = "CK_TZ_OFFSET"
)

// LocalZone builds the time zone the wrapper says it is in, or nil if it said
// nothing. Pass it to Config.Local.
//
// # Why this exists at all
//
// Go does not work out the local zone on either phone. Both
// zoneinfo_android.go and zoneinfo_ios.go define initLocal as, in full,
// `localLoc = *UTC` — each with a TODO above it — and neither consults TZ. So a
// phone in Brazil believes it is in UTC, and after 21:00 local it is already
// tomorrow: a favorites backup saved on the evening of the 16th was named
// ck-karaoke-favorites-2026-08-17.xml. Every line in the server's own log
// disagreed with the logcat timestamp printed beside it, too, which is its own
// small tax on ever debugging anything.
//
// Setting TZ does not help, because nothing reads it. The zone has to be handed
// over, so the wrappers hand it over.
//
// # Why two variables, and why not a zone name alone
//
// The offset is what does the work, because resolving a name to a zone needs a
// zoneinfo database and neither phone reliably has one Go can find: iOS has no
// platform source at all, and Android's has moved to an apex path that Go's
// platformZoneSources does not list. Embedding tzdata would fix that and costs
// several hundred kilobytes, which is a poor trade for stamping a filename.
//
// The name is carried anyway, purely so that the zone reads as
// "America/Sao_Paulo" rather than "-03" wherever it is printed. It is a label,
// and an empty or absurd one costs nothing.
//
// # What this gives up
//
// A fixed offset does not follow a daylight-saving transition, so a server left
// running across one would stamp the hour before it. That is worth knowing and
// not worth fixing here: both wrappers restart the server far more often than a
// zone changes — Android on every launch, iOS on every resume from a long
// suspension — and the values this feeds are a filename and a log stamp.
func LocalZone(getenv func(string) string) *time.Location {
	raw := getenv(EnvZoneOffset)
	if raw == "" {
		return nil
	}
	offset, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	// Fourteen hours is the widest zone in use, and a value outside it means
	// something has been passed in the wrong units — minutes or milliseconds
	// rather than seconds. Better to keep UTC than to invent a zone.
	const limit = 14 * 60 * 60
	if offset < -limit || offset > limit {
		return nil
	}

	name := getenv(EnvZoneName)
	if name == "" {
		name = "local"
	}
	return time.FixedZone(name, offset)
}
