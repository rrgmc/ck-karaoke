module github.com/rrgmc/ck-karaoke/ports/queue-desktop

go 1.27.3

require (
	fyne.io/systray v1.12.2
	github.com/rrgmc/ck-karaoke v0.0.0
)

require (
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grandcat/zeroconf v1.0.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/miekg/dns v1.1.73 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sandertv/go-raknet v0.0.0-00010101000000-000000000000 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.56.0 // indirect
)

// The tray app is its own module so that the library does not depend on a
// tray library. It always builds against the library in this repository.
replace github.com/rrgmc/ck-karaoke => ../..

// The library needs the RakNet v6 fork, and replace directives apply only in
// the main module, so it is repeated here. See the library's go.mod.
replace github.com/sandertv/go-raknet => github.com/RangelReale/go-raknet v0.0.0-20260813174103-4ace5ce8d361
