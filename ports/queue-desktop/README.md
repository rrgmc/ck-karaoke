# Queue manager tray app

The CK Karaoke queue manager in the system tray, for Windows, macOS and Linux.
It runs the same server as `cmd/ck-queue-manager`, through `manager.Run`, and
adds a tray menu:

- **Open configuration** opens the configuration and operator page in a
  window of its own. The page listens on loopback only.
  While the queue manager starts, the window waits for the page. A second
  click brings the open window to the front. If the window cannot run, the
  page opens in the browser instead.
- **Stop** and **Start** stop and start the queue manager. Stop waits until the
  session with the karaoke unit has closed.
- **Quit** stops the queue manager the same way and quits. The window closes
  with it.

## The configuration window

The window is a Wails web view: WebView2 on Windows, WKWebView on macOS and
WebKitGTK on Linux. The tray and the window each need the main thread's event
loop, so the window runs in a process of its own: the same executable,
started with `-window <url>`. It reads its standard input and closes at end of
file, so it never outlives the tray app. Wails' single instance lock keeps one
window per page.

On macOS the window process has its own menu bar, with an Edit menu, so the
copy and paste shortcuts work in the page.

The window's icon comes from `appicon.png` on Linux, from the app bundle on
macOS, and on Windows from the executable's resources:
`rsrc_windows_amd64.syso`, which `go generate` makes from `appicon.ico` (see
`internal/winres`). `go run ./internal/cmd/genicon` renders both images.

The data directory is `CK Karaoke Queue Manager` in the user configuration
directory. On Windows the app has no console, so it also logs to
`queue-manager.log` there.

## Building

It is its own Go module, like `ports/desktop`, so the library does not depend
on a tray or web view library. It repeats the RakNet fork `replace` from the
library's `go.mod`. Wails needs the `desktop` and `production` build tags, and
`webkit2_41` on Linux.

```bash
cd ports/queue-desktop
go build -tags desktop,production -ldflags=-H=windowsgui -o ck-queue-manager-tray.exe .   # Windows
go build -tags desktop,production -o ck-queue-manager-tray .                               # macOS
go build -tags desktop,production,webkit2_41 -o ck-queue-manager-tray .                    # Linux
```

`fyne.io/systray` and Wails need cgo on macOS. On Linux the tray talks to
D-Bus in pure Go, so the desktop needs a StatusNotifierItem host (most do;
GNOME needs the AppIndicator extension), and the window needs cgo with the
GTK 3 and WebKitGTK 4.1 development packages (`libgtk-3-dev`,
`libwebkit2gtk-4.1-dev`).

`package.sh` builds and packages it the way a release ships it, for the host
platform:

```bash
./ports/queue-desktop/package.sh dist
```

| Platform | File | What is in it |
|---|---|---|
| Windows | `ck-queue-manager-<version>-windows-amd64-setup.exe` | an NSIS installer (`installer.nsi`): the app, built for the GUI subsystem, a Start menu entry, an optional desktop shortcut and start with Windows, a firewall rule for private networks, and an uninstaller |
| macOS | `ck-queue-manager-<version>-macos-universal.dmg` | `CK Queue Manager.app`, universal (arm64 and x86_64), menu bar only (`LSUIElement`); signed and notarized when the `MACOS_*` variables are set, as in `ports/desktop/package.sh` |
| Linux | `ck-queue-manager-<version>-linux-amd64.deb` | the binary, which depends on GTK 3 and WebKitGTK 4.1, a desktop entry and the icon |

CI runs it on every push, unsigned; the release workflow runs it on a tag and
attaches the three files to the release.

The tray icons are rendered by `go run ./internal/cmd/genicon`.

## Firewall

The API listens on port 8177 on every interface, because phones on the Wi-Fi
connect to it. The Windows installer adds a firewall rule for private networks;
macOS asks whether to allow it the first time.
For a test on one computer, `-listen 127.0.0.1:8177` avoids the prompt;
`-data` picks another data directory.
