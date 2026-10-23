module github.com/rrgmc/ck-karaoke

go 1.27.3

require (
	github.com/sandertv/go-raknet v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
	golang.org/x/text v0.41.0
	modernc.org/sqlite v1.56.0
	rsc.io/qr v0.2.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// The device speaks RakNet protocol version 6; upstream go-raknet hardcodes 11
// and aborts on mismatch, so the raknet6 fork is required, not optional. This
// pin also carries the raw unconnected-pong payload these devices send; see
// docs/PROTOCOL.md.
replace github.com/sandertv/go-raknet => github.com/RangelReale/go-raknet v0.0.0-20260813174103-4ace5ce8d361

// To iterate on the RakNet library, point it at a local checkout:
//
//	replace github.com/sandertv/go-raknet => ../go-raknet
