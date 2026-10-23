# CK Karaoke for iOS

A WKWebView onto the same Go web server the other ports run, with
the server inside the app. Universal: iPhone and iPad.

```bash
./ports/ios/build.sh                # the Go server, then the Xcode project
open ports/ios/CKKaraoke.xcodeproj  # press Run
```

Re-run `build.sh` whenever Go code changes. It regenerates
`ports/ios/CKKaraoke.xcodeproj` from `ports/ios/project.yml`, so never edit the project in
Xcode expecting it to stick — edit the spec.

## Why this is not the Android design

The Android app ships `cmd/ck-karaoke-server` as a native library and forks it with
`ProcessBuilder`. **iOS forbids `fork` and `exec` from a sandboxed app**, so there
is no child process to spawn and no directory to spawn it from.

Instead `ports/ios/bridge/bridge.go` is built with `-buildmode=c-archive` and linked
into the app, so the server runs on a goroutine in the app's own process. Swift
calls seven C functions:

| Function | Purpose |
|---|---|
| `CKStart(dataDir, name, addr)` | starts the server, returns at once |
| `CKPort()` | 0 until listening, then the port |
| `CKProgress()` | catalogue download percent, or -1 |
| `CKError()` | why it stopped, or NULL |
| `CKSuspend()` / `CKResume()` | give the device session up and take it back |
| `CKStop()` | shut down, waiting for the session to close |

Two slices are built because **Go cannot tell an arm64 device from an arm64
simulator** — `GOARCH=arm64` covers both and the difference is the clang target
triple (`-target arm64-apple-ios15.0-simulator`). `build.sh` builds each and
combines them with `xcodebuild -create-xcframework`.

`CKError` returns a string Go owns and does not free until the next
`CKStart`. Swift must not free it; a `C.CString` handed out per call would
leak on every poll, and Swift polls at 10 Hz.

**Strings coming the other way have the opposite lifetime, and it is short.**
Swift's `withCString` frees its buffer as soon as the closure returns, so every
argument must be copied to a Go string *on entry* to `CKStart`. A
`C.GoString` left inside a closure that runs later, on the server goroutine, reads
freed memory, and that failure is worse than a crash: the bytes can read back as
a hand-configured device address, so discovery is skipped and the dial runs to a
timeout. The simulator can hide it, because its freed bytes often begin with a
NUL and read as the empty string, which is the "discover it" case.

## Discovery is a unicast sweep, not a broadcast

`client.Discover` broadcasts. **iOS 14+ requires the
`com.apple.developer.networking.multicast` entitlement for broadcast or
multicast**, and Apple grants it only after a manually reviewed request. Ordinary
unicast to a LAN address needs nothing beyond the local-network prompt.

So iOS uses `client.Sweep` (`client/sweep.go`), which pings every host on the
local subnet one at a time. Details that matter:

- **Outwards from our own address.** The unit is usually a few addresses away, so
  this finds it in tens of milliseconds rather than after a thousand pings.
  On a typical /22 this takes 20 to 90 ms.
- **`ResolveSweep` stops at the first answer.** Waiting out the timeout to see
  whether a second device exists is six seconds of splash screen.
- **Paced in batches of 64.** A thousand packets sent back to back looks like a
  port scan to a consumer access point, and the ones it drops are the device.
- **Refuses anything wider than a /20.** A /22 (1022 hosts) is fine; a /16
  would be 65534 pings.

`NSLocalNetworkUsageDescription` is mandatory. Without it every packet to the unit
is silently dropped — the app is not told, it simply never gets a reply.

## There is no foreground service

Android keeps the session alive with a `connectedDevice` foreground service. iOS
has nothing equivalent for a LAN UDP session, and `webui.Device`'s
`StaleAfter = 15s` means the session is dead well before the app is suspended.

`AppDelegate` therefore starts a `beginBackgroundTask` on entering the background
and calls `CKSuspend` **from its expiry handler**, not immediately. During a
background task the app is still running, so the device goes on pushing status and
the staleness watchdog never fires — glancing at another app and coming straight
back keeps the session. When the grace period runs out the session is closed
properly, because an abandoned one holds one of the unit's five client slots until
the device times it out by itself.

`webui.Device.Suspend`/`Resume` exist for this and nothing else. Resuming presents
the same identity again, which is safe *because* Suspend closed the session rather
than dropping it — the same reasoning the staleness path relies on.

A typical log, driving the app with `devicectl` (launch another app to background
this one, launch this one again to bring it back):

```
entered background, holding the session for now
background time expired, giving up the session      +26.6s
suspending the device session
returning to foreground
resuming the device session
device connected                                    +177ms
```

So the grace period is worth about 26 seconds in practice — switching apps and
coming straight back keeps the session, and a longer absence costs a fifth of a
second on return. Note that a debugger attached over `devicectl --console` does
not stop iOS enforcing the limit, which is what makes this testable at all.

The web view is not reloaded across any of this, so nothing re-requests the page.
If the event stream does drop, it repairs itself: `EventSource` reconnects on its
own and `webui/sse.go` replays the latest of each event to a new subscriber.

## Safe areas need no injection here

Android has to measure the window insets and push the bottom one into the page as
a CSS variable, because a plain WebView reports no safe areas at all. **WKWebView
does report them**, and `webui/templates/layout.html` already sets
`viewport-fit=cover`, so `app.css`'s default
`--safe-bottom: env(safe-area-inset-bottom)` works untouched.

What makes it work is how the web view is pinned in `RootViewController.showWeb`:
top and sides to `view.safeAreaLayoutGuide`, **bottom to `view.bottomAnchor`**,
with `contentInsetAdjustmentBehavior = .never`. The web view therefore overlaps
the home-indicator region, WKWebView reports a real inset to CSS, and the page
handles it. Pin the bottom to the safe area instead and the tab bar floats above a
blank strip.

## Where things live

- **Databases:** `Library/Application Support/ckkaraoke/`. Not `Documents` — the
  catalogue is a 2.2 MB copy of what the device already has, and `Documents` is
  backed up to iCloud and can be exposed in the Files app.
- **The address and name overrides:** `UserDefaults`, surfaced on the failure
  screen. That screen is the only settings UI, deliberately: the sweep normally
  finds the unit, and when it does not, that is the screen you are already looking
  at. Android has the same fallback with no UI at all.
- **Logs:** the server writes to stderr, which Xcode's console shows. There is no
  `os_log` bridge. The level is **Debug**, deliberately: `webui.logRequests` logs
  each request at Debug, and those lines are the only way to tell a web view that
  failed to load from one that loaded an empty page. The bridge also logs a
  `listening` line, the counterpart of the one the CLI prints for Android to
  scrape.
- **Queuer name:** `UIDevice.current.model`, so "iPhone" or "iPad".
  `UIDevice.name` has returned the model rather than the user's chosen name since
  iOS 16 without a special entitlement. The device tags each reserved song with
  the *session's* name, so one app is one name; override it on the failure screen.

The server binds `127.0.0.1:0`, as on Android: binding the WiFi interface would
put an unauthenticated remote control on the network. `NSAllowsLocalNetworking`
is what permits plain HTTP to it, and is the counterpart of Android's
`network_security_config.xml`.

## Build and run traps

- **Xcode needs the iOS platform installed**, separately from the SDK. With it
  missing, `xcodebuild` reports *"iOS 26.5 is not installed"* and marks every
  destination — simulators included — ineligible, so nothing iOS builds at all.
  Fix with `xcodebuild -downloadPlatform iOS`, or Xcode → Settings → Components.
  The Go side is unaffected: it only needs the SDK, which is why `build.sh` works
  regardless.
- **`xcode-select` may point at the Command Line Tools**, which have no
  `xcodebuild` and no iOS SDKs. `build.sh` exports `DEVELOPER_DIR` itself rather
  than requiring `sudo xcode-select -s`.
- **XcodeGen must be told to write a scheme.** Without a `schemes:` block there is
  no `.xcscheme` on disk, `xcodebuild` invents one whose buildable has no
  supported platforms, and every `-destination` is rejected as unmatched — with an
  error that blames the destination rather than the missing scheme.
- **A c-archive does not carry cgo's `LDFLAGS`.** A Go-built executable links
  `CoreFoundation` and `Security` automatically; here the consuming linker is
  Xcode, so `project.yml` names them, plus `-lresolv` for the resolver.
- **`-trimpath -ldflags="-s -w"` matters more than it looks.** Without them the
  archive is 35 MB per slice; with them it is 9.7 MB, and the linked app binary
  about 14 MB.
- **macOS may firewall the Go binary.** Each `go run` produces a new unsigned
  executable, and a blocked one fails unicast sends with `sendto: no route to
  host` while broadcast still works — which looks exactly like a bug in the sweep.
- **A first run looks like a hang.** The server does not open its listener until
  the catalogue is ready, so there is no port to load a page from before then. The
  startup screen now counts the download up rather than only spinning, which is
  what makes a slow run distinguishable from a broken one. `RootViewController`
  also implements `didFailProvisionalNavigation`, so a load that genuinely fails
  says so instead of leaving a white screen.
- **Most of a first run is spent waiting for silence, not for data.** A 2.2 MB
  transfer finishes in under two seconds, and the next 15 go to
  `client.DownloadOptions.Idle`, which is how the transfer learns it is over
  (see `docs/PROTOCOL.md`, section 8).
- **An offloaded app keeps its icon and loses its universal links.** The song
  rows' YouTube link opened Safari rather than the YouTube app, which reads as
  `decidePolicyFor` handing the URL out wrongly — it is not. Offloading deletes
  the binary, keeps the data, and leaves a home-screen icon whose cloud badge is
  easy to miss, and the association goes with the binary; `UIApplication.open`
  then finds nothing claiming the host and falls back to the browser, which is
  the behaviour it is supposed to have. Reinstalling the target app is the whole
  fix. The other cause presents identically: once iOS has been told to open a
  domain in Safari it keeps doing so, and tapping **Open** on the Smart App
  Banner at the top of the page restores the association. Rule the URL itself in
  or out first by reading the site's own
  `/.well-known/apple-app-site-association` — YouTube's is an exclusion list
  ending in a catch-all, so `/results` is covered. Do not reach for
  `devicectl device info apps` to check what is installed: it errors on
  iPadOS 17.7, and the attempt invalidates the tunnel any `--console` stream is
  running over.

## Xcode 26 puts the code in a dylib

A debug build's `CKKaraoke.app/CKKaraoke` is a ~40 KB launcher stub; the real binary
is `CKKaraoke.app/CKKaraoke.debug.dylib`, around 14 MB. Checking the wrong one for Go
symbols suggests the archive never linked. `nm -a CKKaraoke.app/CKKaraoke.debug.dylib
| grep _CKStart` is the check that means something.

## Saving a backup out, and reading one back in

Exporting favorites is the first thing this app hands to the rest of iOS, and the
two halves of it are lopsided: the way out needed real code, the way in needed
none.

**Out.** A `WKWebView` has no download UI. Left alone it does not render an
attachment and does not say so either, so the user is looking at the page they
were already on with no sign that anything happened. Three pieces in
`RootViewController`:

1. `decidePolicyFor navigationResponse:` returns `.download` when the response
   carries `Content-Disposition: attachment`. Decided by the header, not by
   naming the URL — the same rule `decidePolicyFor navigationAction:` uses for
   links, so anything the server marks as a file from now on is handled without
   this having to know about it.
2. `navigationResponse:didBecome download:` takes the `WKDownload`.
3. `WKDownloadDelegate` writes into a fresh `UUID`-named directory under
   `temporaryDirectory` — WebKit refuses a destination that already exists, and
   two backups saved on the same day would otherwise collide — then hands the
   file to a `UIActivityViewController`.

That sheet is what reaches **Save to Files, Dropbox and Drive**, which is the
same three destinations the Android app's document picker offers, arrived at by
the mechanism iOS actually has. Nothing is written to `Documents`, and no
`UIFileSharingEnabled`: the copy worth keeping is the one the sheet handed to
another app, and exposing this app's folder would undo the decision recorded
under *Where things live*.

Two traps, both of which only show up on a real device:

- **The popover anchor is required, not tidiness.** On iPad a
  `UIActivityViewController` with no `sourceView`/`sourceRect` has nowhere to
  point and raises instead of presenting.
- **A download cancels the navigation that became it, and WebKit can report the
  cancellation as an error.** `WebKitErrorDomain` 102, frame load interrupted by
  a policy change, arrives at `didFailProvisionalNavigation` — which this app
  reads as the server having died and *restarts it*. Saving a backup would then
  tear down the server that had just served it, with the startup screen
  appearing for no visible reason. Whether WebKit raises it for a `.download`
  decision or only for an outright `.cancel` has varied between versions, so
  both failure callbacks filter the code rather than depending on the answer.
  Nothing this app navigates to should ever be read as the server dying.

**In.** Nothing was needed. WKWebView handles `<input type="file">` natively on
iOS, offering Photo Library / Take Photo / Choose File, and Choose File opens the
document browser where Dropbox and Drive appear as File Provider extensions.
`runOpenPanelWith:` is the macOS equivalent and must not be added here — it is
never called on iOS. The input carries `.xml`, `text/xml` and `application/xml`
together because iOS maps that list to UTTypes while Android's picker filters on
the MIME type, and either alone hides the file on the other platform.

## The server does not know what time it is

The same gap as Android, reached from the other side: `zoneinfo_ios.go` defines
`initLocal` as `localLoc = *UTC`, and iOS has **no platform zoneinfo source at
all** (`var platformZoneSources []string // none on iOS`). Nothing reads `TZ`
either, so setting it accomplishes nothing.

`Server.passTimeZone()` sets two variables before `CKStart`:

```swift
setenv("CK_TZ", zone.identifier, 1)
setenv("CK_TZ_OFFSET", String(zone.secondsFromGMT()), 1)
```

`server.LocalZone` turns them into a `*time.Location` and `server.Run` assigns
`time.Local`. The offset does the work, because a name cannot be resolved without
a database to resolve it against; the name is carried so the zone prints
readably.

**The bridge reads these with `C.getenv`, not `os.Getenv`, and that is the whole
reason `cgoGetenv` exists.** The Go runtime copies the environment once when it
initialises, and in a c-archive that happens at process start — long before any
Swift runs. So a `setenv` from Swift is invisible to Go's cached copy and visible
only to C. Android sidesteps this entirely: there the variables go onto a
`ProcessBuilder` and the server is exec'd with them already in place.

Passing them through the environment rather than as further `CKStart`
arguments is deliberate: it keeps the C surface small, and the string-lifetime
trap documented above applies to every argument added to it.
