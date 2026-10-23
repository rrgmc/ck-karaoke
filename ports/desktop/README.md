# CK Karaoke for Windows, macOS and Linux

A [Wails v2](https://wails.io) window around the same server the phone ports
run. The server runs in-process on a goroutine, bound to `127.0.0.1` on a free
port. The window first shows a startup page from its own assets
(`frontend/index.html`); once the server is listening, the page navigates the
window to `http://127.0.0.1:<port>/`.

This directory is its own Go module, so the library does not depend on Wails.
Its `go.mod` points at the library in this repository with a `replace`, and
repeats the RakNet fork `replace` that the library needs.

## Building

Install the Wails CLI once:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
wails doctor   # lists anything missing for this platform
```

Then, from this directory:

| Platform | Command | Needs |
|---|---|---|
| Windows | `wails build` | the WebView2 runtime (part of Windows 11) |
| macOS | `wails build -platform darwin/universal` | Xcode command line tools |
| Linux | `wails build -tags webkit2_41` | `libgtk-3-dev`, `libwebkit2gtk-4.1-dev`, a C compiler |

The output is in `build/bin/`. Wails builds for the host platform; build each
platform on that platform. Add `-nsis` on Windows to also build the installer,
`CK Karaoke-amd64-installer.exe`; it needs [NSIS](https://nsis.sourceforge.io)
on `PATH`. The installer scripts are in `build/windows/installer/`, and
`build/darwin/Info.plist` adds the macOS privacy strings (without the local
network one, macOS drops the discovery broadcast).

`./package.sh <dir> [version]` builds and packages for the host platform
exactly as a release does: the installer on Windows,
a verified universal disk image on macOS (it also checks the bundle's version,
identifier and privacy strings), and a tarball on Linux. CI runs it on every
push; the release workflow runs it on a version tag (see
[docs/BUILDING.md](../../docs/BUILDING.md#continuous-integration-and-releases)).

The macOS release is signed with a Developer ID and notarized; see
[docs/BUILDING.md](../../docs/BUILDING.md#continuous-integration-and-releases)
for the secrets this needs. A build without them is not signed: open it once
with right-click, Open, or run
`xattr -dr com.apple.quarantine "/Applications/CK Karaoke.app"`.

The Windows installer is not code-signed. Windows SmartScreen may warn on first
run ("More info", then "Run anyway").

Without the CLI, plain Go works too, minus the icon and the app bundle:

```bash
# Windows
go build -tags desktop,production -ldflags "-H windowsgui" -o build/bin/ck-karaoke.exe .
# macOS: name the framework the Wails CLI would add
CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=10.13"   go build -tags desktop,production -o build/bin/ck-karaoke .
# Linux
go build -tags desktop,production,webkit2_41 -o build/bin/ck-karaoke .
```

The icon comes from `build/appicon.png`, which `internal/cmd/genicon` renders.
`wails build` derives `build/windows/icon.ico` from it.

## Behaviour

- **Data** lives in the per-user configuration directory: `%AppData%\CK Karaoke`
  on Windows, `~/Library/Application Support/CK Karaoke` on macOS,
  `~/.config/CK Karaoke` on Linux. It holds `song.db`, `favorites.db` and
  `device.addr`.
- **Discovery** uses broadcast. The first run needs the unit switched on, to
  download the song list. Until a unit answers, the startup page keeps searching
  and says what to check, including macOS's Local Network permission. Later runs
  start offline on the last known address and connect when the unit comes back.
- **The device name** is the computer's host name.
- **Backups** are saved through the system's Save dialog (`server.Config.SaveFile`),
  because not every desktop web view handles downloads. The page stays where it
  was and a toast reports the result.
- **Closing the window** stops the server and waits for the device session to
  close, so the unit does not keep a slot for a client that has gone.
- `CK_DEBUG=1` logs every request to stderr. Build without `-H windowsgui` to see
  the log in a console on Windows.

## Known gaps

- Only the Windows build has been run. The macOS and Linux builds use the same
  code but are untested.
- Links that leave the server (the YouTube search on each song) are handled by
  the web view itself, not handed to the system browser: once the window has
  navigated to the server it is no longer on a Wails page, so Wails cannot
  intercept them. Their exact behaviour per platform is untested.
- The QR scanner needs a camera and a web view that grants it; where either is
  missing, the sync page's typed code works instead.
