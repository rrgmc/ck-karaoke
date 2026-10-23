# CK Karaoke

[![CI](https://github.com/rrgmc/ck-karaoke/actions/workflows/ci.yml/badge.svg)](https://github.com/rrgmc/ck-karaoke/actions/workflows/ci.yml)

CK Karaoke is a remote control for karaoke units on your local network. It works
with the Videoke VSK family and other units sold under the CooKara and RafKara
names. Browse and search the unit's song list, queue songs, control playback,
key, tempo and volumes, and keep favorites folders that you can share between
devices by QR code.

It runs on Android, iOS, Windows, macOS and Linux. Every version is the same web
UI served by the same Go server, wrapped in a native app.

## Repository layout

```
protocol/              wire format: frames, field lists, commands, messages
client/                RakNet session to the device, discovery, database download
catalog/               the device's song database: reassembly, verification, search
favorites/             favorites folders (SQLite)
  qrshare/             one folder as a numeric QR code
  xmlbackup/           every folder as an XML backup file
webui/                 the web UI: handlers, templates, static assets, event stream
server/                assembles the whole server from a Config
cmd/ck-karaoke-server/ debug server for the command line (also the Android binary)
ports/android/         Android app (Kotlin)
ports/ios/             iOS app (Swift) and its Go bridge
ports/desktop/         Windows, macOS and Linux app (Wails v2, separate Go module)
assets/icon/           icon source
internal/cmd/genicon/  renders the icon rasters
docs/                  protocol specification and architecture notes
```

## Running the debug server

The quickest way to try it is the command-line server. It finds the unit by
broadcast, downloads the song list on first run, and serves the UI:

```bash
go run ./cmd/ck-karaoke-server
```

Then open the printed URL on any phone or computer on the same network.

| Flag | Meaning |
|---|---|
| `-addr` | device address; found by broadcast if empty |
| `-listen` | address to serve on (default `:8080`) |
| `-data` | directory for the databases (default `data/` beside the binary) |
| `-catalog`, `-favorites` | database files, overriding `-data` |
| `-refresh-db` | download the song list again |
| `-name` | name the unit shows beside songs queued here (default: host name) |
| `-discover-timeout` | how long to listen for a unit (default 3s) |
| `-allow-folder-delete` | allow deleting favorites folders |
| `-v` | verbose logging, including every request |

The server is unauthenticated. Bind it to a trusted network only.

## Building the apps

| Platform | Command | Details |
|---|---|---|
| Android | `./ports/android/build.sh` | [ports/android/README.md](ports/android/README.md) |
| iOS | `./ports/ios/build.sh` (on a Mac) | [ports/ios/README.md](ports/ios/README.md) |
| Windows, macOS, Linux | `cd ports/desktop && wails build` | [ports/desktop/README.md](ports/desktop/README.md) |

The app ports bind the server to `127.0.0.1` only, so nothing else on the
network can drive the unit through them.

## Using the library

The Go packages can be used on their own:

```go
c, err := client.Dial(ctx, "192.168.1.50", client.Options{})
if err != nil {
    return err
}
defer c.Close()

c.OnStatus(func(s *protocol.Status) { fmt.Println(s) })
if err := c.Reserve(ctx, "19535"); err != nil {
    return err
}
```

The device speaks RakNet protocol version 6, which upstream
`sandertv/go-raknet` does not support. A module that imports this library must
repeat the fork `replace` from this repository's [go.mod](go.mod), because Go
applies `replace` directives only in the main module:

```
replace github.com/sandertv/go-raknet => github.com/RangelReale/go-raknet v0.0.0-20260813174103-4ace5ce8d361
```

## Development

```bash
go build ./... && go vet ./... && go test ./...
```

All tests are hermetic: no device and no external data are needed. The desktop
port is a separate module; build and vet it from `ports/desktop` with
`-tags desktop,production`.

To regenerate the icon rasters after changing the design:

```bash
go run ./internal/cmd/genicon
```

## Continuous integration and releases

[CI](.github/workflows/ci.yml) runs on every push to `master` and every pull
request. It tests the library on Linux, Windows and macOS, vets and builds the
desktop app on all three, builds the Android APK, and builds the iOS app for
the simulator. It produces no downloads.

Binaries are built only for a release. Pushing a version tag starts the
[release workflow](.github/workflows/release.yml), which runs CI, then
publishes a GitHub release with the Android APK, the Windows installer and
executable, the macOS disk image and a `SHA256SUMS.txt`:

```bash
git tag v1.2.3
git push origin v1.2.3
```

The tag must be `vMAJOR.MINOR.PATCH`, with minor and patch below 100; a suffix
such as `v1.2.3-rc1` publishes a pre-release. The tag sets the version of every
app, and the Android version code is `MAJOR×10000 + MINOR×100 + PATCH`.

The APK is signed with the release key from these repository secrets:

| Secret | Value |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | the keystore file, base64-encoded |
| `ANDROID_KEYSTORE_PASSWORD` | the keystore password |
| `ANDROID_KEY_ALIAS` | the key alias |
| `ANDROID_KEY_PASSWORD` | the key password |

Without them the APK is signed with a throwaway debug key, and the next
release will not install over it. The Windows and macOS builds are not
code-signed.

## Documentation

- [docs/PROTOCOL.md](docs/PROTOCOL.md): the device protocol.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): how the server and the ports fit
  together, and the design rules that keep them working.
