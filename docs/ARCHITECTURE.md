# Architecture

This document explains how CK Karaoke is put together and records the design
rules that keep it working. The wire protocol itself is in
[PROTOCOL.md](PROTOCOL.md).

## Layers

```
ports/android   ports/ios   ports/desktop   cmd/ck-karaoke-server
      \             |             |             /
       +------------+------ server -------------+
                            |
                          webui ---- favorites (+ qrshare, xmlbackup)
                            |
                 client ----+---- catalog
                    |
                 protocol
```

- `protocol` is the wire format and nothing else. It has two layers: `Frame` is
  the wire form and `Message` the meaning. Receiving goes
  `ParseFrame(bytes, fromDevice)` then `ParseMessage(frame)`; `ParseMessage`
  returns `(nil, nil)` for frames that decode but have no meaning. Sending goes
  the other way: every `Message` has a `Frame()`.
- One `CommandRequest` type covers every command, because commands differ only
  in id and string arguments. Adding a command is one line in
  `protocol/command.go` and, optionally, a typed method on `client.Client`.
- `client` owns one RakNet session. `catalog` is a read-only view of the song
  database the device sends. `favorites` is a separate SQLite file the app owns.
- `webui` serves htmx pages and one server-sent event stream, and holds the
  long-lived device session in `webui.Device`.
- `server` assembles everything from a `server.Config`.

## The server is assembled by `server.Run`, not by `main`

Every front end starts the server the same way: build a `server.Config` and
call `server.Run`. The debug CLI only parses flags and prints the LAN banner.
`Config.Ready` reports the listening address and the device address, which no
caller knows in advance because the app ports bind port 0 and the device usually
comes from discovery.

## Three ways to host the server

| Port | How the server runs | Why |
|---|---|---|
| Android | `cmd/ck-karaoke-server` shipped as `libckkaraoke.so`, run as a child process | the native library directory is the one place an app may execute a file |
| iOS | `ports/ios/bridge`, a `c-archive` linked into the app, on a goroutine | iOS forbids `fork` and `exec` |
| Desktop | `server.Run` in-process, on a goroutine | nothing to work around |

Any change to how the server starts has to work for all three. The phone ports
and the desktop bind `127.0.0.1:0`, so the page must reload when the server
restarts; the port moves every time.

## The device can be off

Browsing, searching and favorites need no device. `Config.LastAddrFile`
remembers where the device was last found. When discovery finds nothing **and** a
catalogue already exists, the server starts offline on that address and
`webui.Device` connects by itself when the unit comes back. With no catalogue
the device is required, because it is the only source of one.

With no catalogue, `Config.WaitForDevice` keeps searching instead of failing,
and `Config.Searching` tells the caller each time a search comes back empty. The
desktop and iOS apps set it. On their first launch the system blocks local
network traffic until the user answers the permission prompt, and a single
search ends long before that.

`webui.Device` dials with a timeout (`webui.DialTimeout`). A stale address does
not fail fast: nothing answers, and RakNet's MTU discovery would otherwise wait
forever. The timeout turns that into an ordinary error and the reconnect backoff.

The device's sign that a session is dead is silence: it pushes status about every
1.5 seconds, so `webui.Device` treats 15 seconds without status as a dropped
session and reconnects.

## Closing cleanly

A process that exits without sending RakNet's disconnect notification leaves the
unit holding a connection slot. Three things keep that from happening:

- `client.Client.Close` waits for the notification, which the RakNet library
  sends from a timer about a second after `Close` returns.
- `server.Run` waits for `webui.Device.Run` to return before returning itself.
  Cancelling the context only starts the close.
- `cmd/ck-karaoke-server` ignores `SIGPIPE`. Android stops the server with
  `Process.destroy()`, which sends SIGTERM and immediately closes the server's
  stdout and stderr; Go's default for a write to a closed pipe on fd 1 or 2 is to
  die by `SIGPIPE`. The symptom is exit code 141 instead of 0.

## Mobile lifecycles

**Android** keeps the session in a foreground service. The service never returns
`START_STICKY`: a service the system restarts comes back with the app in the
background, where it may not go foreground, so it would crash and be restarted
forever. Backgrounding the app starts a ten-minute countdown; when it expires
the service stops and takes the server with it. Stopping the service is the
suspend; Android needs no other control channel.

**iOS** has no foreground service. `AppDelegate` asks for background time and,
when it runs out, calls `webui.Device.Suspend` through the bridge, which closes
the session cleanly. Returning calls `Resume`. `webui/static/resume.js`
(`window.ckResume`) rebuilds the page's event stream after a suspension
without reloading, so the reader keeps their place in a long list.

**iOS discovers by unicast sweep**, not broadcast, because broadcast there needs
an entitlement Apple grants only on request. `client.Sweep` pings every host on
the local subnets, nearest first. Everything else uses `client.Discover`.

## Identities and names

Every program generates its own identity with `protocol.NewIdentity()`. Two
sessions with one UUID displace each other silently.

The device records who queued each song using the **session's** name, so one
server is one name. The debug CLI and the desktop use the host name; the phone
ports use the device model. Per-person attribution would need a session per
person.

## Favorites

Favorites are folders of song ids in their own SQLite file. Deleting the song
catalogue (for example with `-refresh-db`) never touches them.

**Sharing is a union, and that is the design.** Two phones cannot reach each
other's servers, so a folder travels as a QR code (`favorites/qrshare`) and
`favorites.DB.AddSongs`, an insert-or-ignore, is the only write. Merging is
therefore commutative and idempotent: no tombstones, no clocks, no conflicts,
and reading the same code twice changes nothing. Do not add a delete path to
make it "two-way"; the guarantee that sharing never loses anything is the
feature.

The QR payload is decimal digits only, because QR's numeric mode costs 3⅓ bits a
character against 8 for byte mode and the encoder uses one mode for the whole
string. The folder name travels as three digits per UTF-8 byte. A header fixes
the length and a mod-97 check digit catches the rest, so a partial scan fails
instead of importing half a folder. About 1,100 songs is where a folder
outgrows any QR code; past that the page offers the code as text.

**Backup is separate** (`favorites/xmlbackup`, `/favorites/backup`): the whole
database as one XML file, also add-only. Neither replaces the other: a QR code
cannot carry a large database, and a file is no use for handing one folder to
the person next to you. Both meet only at `favorites.DB.EnsureFolder` and
`AddSongs`. Unknown song ids are kept on export and dropped on import, so a
folder never claims more songs than it can list.

Sharing is reachable only from inside a folder and backup only from the folder
list, so neither screen asks a question its own location already answers.

## Camera, downloads and file pickers

The QR scanner decodes in the page: `webui/static/scan.js` drives
`getUserMedia` into a canvas and hands frames to a vendored `jsQR`.
`BarcodeDetector` is not in WebKit. The camera is never required; "Can't scan
it?" accepts a typed code.

Each wrapper has to open these up:

- **Android** grants `RESOURCE_VIDEO_CAPTURE` from its `WebChromeClient` after
  the system camera prompt. An unanswered permission request hangs
  `getUserMedia` forever, so every path answers.
- **iOS** grants the camera from its `WKUIDelegate`, only for a `127.0.0.1`
  origin, and declares `NSCameraUsageDescription`. Without the delegate WebKit
  asks again on every `getUserMedia` call.

The backup export is an ordinary loopback navigation answered with
`Content-Disposition: attachment`. Android's `DownloadListener` turns that into
`ACTION_CREATE_DOCUMENT`; iOS turns it into a `WKDownload` and a share sheet.
Do not change the export into a `blob:` URL: neither web view can save one. On
iOS, a download cancels its navigation and WebKit may report error 102, which
the app would otherwise read as the server dying; both failure callbacks filter
it.

The desktop port sets `server.Config.SaveFile` instead, so the export goes
through the system's Save dialog and answers `204 No Content`.

Importing needs no native code on iOS. The file input accepts `.xml`,
`text/xml` and `application/xml` together, because Android's picker filters on
MIME type and iOS maps the list to UTTypes.

## Time zones on the phones

Go's `time.Local` is UTC on Android and iOS, and neither reads `TZ`. Both
wrappers pass `CK_TZ` (the zone name) and `CK_TZ_OFFSET` (seconds east of UTC);
`server.LocalZone` builds a fixed zone from them and `server.Run` assigns
`time.Local`. The offset does the work, because neither platform gives Go a
zoneinfo database. On iOS the bridge reads them with `C.getenv`, because the Go
runtime copied the environment before any Swift ran.

## The song database download

The catalogue arrives in chunks whose header length varies. `catalog.Reassembler`
finds the boundaries from the SQLite header instead of a fixed offset, and
`catalog.Fetch` writes to a temporary file and moves it into place only after
`catalog.Verify` passes. A misassembled file opens and lists every song, so
only the integrity check catches it. See PROTOCOL.md, section 8.
