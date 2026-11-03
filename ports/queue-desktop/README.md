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

The tray icons are rendered by `go run ./internal/cmd/genicon`.

## Firewall

The API listens on port 8177 on every interface, because phones on the Wi-Fi
connect to it. Windows and macOS ask whether to allow that the first time.
For a test on one computer, `-listen 127.0.0.1:8177` avoids the prompt;
`-data` picks another data directory.
