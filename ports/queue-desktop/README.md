# Queue manager tray app

The CK Karaoke queue manager in the system tray, for Windows, macOS and Linux.
It runs the same server as `cmd/ck-queue-manager`, through `manager.Run`, and
adds a tray menu:

- **Open configuration** opens the configuration and operator page in the
  browser. The page listens on loopback only.
- **Stop** and **Start** stop and start the queue manager. Stop waits until the
  session with the karaoke unit has closed.
- **Quit** stops the queue manager the same way and quits.

The data directory is `CK Karaoke Queue Manager` in the user configuration
directory. On Windows the app has no console, so it also logs to
`queue-manager.log` there.

## Building

It is its own Go module, like `ports/desktop`, so the library does not depend
on a tray library. It repeats the RakNet fork `replace` from the library's
`go.mod`.

```bash
cd ports/queue-desktop
go build -ldflags=-H=windowsgui -o ck-queue-manager-tray.exe .   # Windows
go build -o ck-queue-manager-tray .                               # macOS, Linux
```

`fyne.io/systray` needs cgo on macOS. On Linux it talks to the tray over D-Bus,
so the desktop needs a StatusNotifierItem host (most do; GNOME needs the
AppIndicator extension).

`package.sh` builds and packages it the way a release ships it, for the host
platform:

```bash
./ports/queue-desktop/package.sh dist
```

| Platform | File | What is in it |
|---|---|---|
| Windows | `ck-queue-manager-<version>-windows-amd64-setup.exe` | an NSIS installer (`installer.nsi`): the app, built for the GUI subsystem, a Start menu entry, an optional desktop shortcut and start with Windows, a firewall rule for private networks, and an uninstaller |
| macOS | `ck-queue-manager-<version>-macos-universal.dmg` | `CK Queue Manager.app`, universal (arm64 and x86_64), menu bar only (`LSUIElement`); signed and notarized when the `MACOS_*` variables are set, as in `ports/desktop/package.sh` |
| Linux | `ck-queue-manager-<version>-linux-amd64.deb` | a static binary, a desktop entry and the icon |

CI runs it on every push, unsigned; the release workflow runs it on a tag and
attaches the three files to the release.

The tray icons are rendered by `go run ./internal/cmd/genicon`.

## Firewall

The API listens on port 8177 on every interface, because phones on the Wi-Fi
connect to it. The Windows installer adds a firewall rule for private networks;
macOS asks whether to allow it the first time.
For a test on one computer, `-listen 127.0.0.1:8177` avoids the prompt;
`-data` picks another data directory.
