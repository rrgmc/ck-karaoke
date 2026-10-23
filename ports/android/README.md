# CK Karaoke for Android

The web UI, with the Go server running inside the phone.

```
APK
 ├─ lib/arm64-v8a/libckkaraoke.so   the Go server (cmd/ck-karaoke-server), static
 └─ MainActivity                    a WebView on http://127.0.0.1:<port>
```

The server is packaged as a **native library** on purpose. Only files named
`lib*.so` are extracted from an APK, and the native library directory is the one
place an app may execute a file from under the W^X rules Android enforces since
version 10. A copy in `filesDir` would not run.

The Go tree builds with cgo disabled, so none of this needs gomobile, the NDK or
JNI. Only `arm64` is built: it is the one Android target Go links without cgo,
and it covers current phones.

## Building

Requirements: Go, a JDK 17 to 21 (Temurin 21 is tested), Gradle on `PATH` or in
`$GRADLE`, and the Android SDK.

```bash
export JAVA_HOME=/path/to/jdk-21
./ports/android/build.sh            # debug APK
./ports/android/build.sh release    # release APK; see "Signing" below
./ports/android/build.sh bundle     # App Bundle for Google Play
adb install -r ports/android/app/build/outputs/apk/debug/app-debug.apk
```

Do not build with Android Studio's bundled runtime. It reports a malformed
version string (`25.0.2+-15348964-b329.117`) that the Kotlin DSL's version
parser rejects with a bare `IllegalArgumentException: 25.0.2`.
`settings.gradle.kts` lets Gradle download a proper JDK for compilation.

## Signing

A release build is signed with the key named by `CK_KEYSTORE_FILE`,
`CK_KEYSTORE_PASSWORD`, `CK_KEY_ALIAS` and `CK_KEY_PASSWORD` when
`CK_KEYSTORE_FILE` is set, and with the debug key otherwise. Android refuses to
upgrade an app across a change of signing key, and an uninstall deletes the
favorites, so every published APK must use the same release key. The release
workflow takes it from repository secrets; see
[docs/BUILDING.md](../../docs/BUILDING.md#continuous-integration-and-releases).

The version name and code come from `version/VERSION` (the code is
`MAJOR×10000 + MINOR×100 + PATCH`). `-PversionName` and `-PversionCode`
override them.

## Google Play

Google Play takes an Android App Bundle (`.aab`), not an APK. `build.sh bundle`
writes it to `app/build/outputs/bundle/release/app-release.aab`, signed like the
release APK, and the release workflow attaches it to each GitHub release.

**Play App Signing.** Play re-signs every app with an app signing key that
Google keeps. When creating the app in Play Console, choose to use an existing
key and upload the current release key with the PEPK tool that Play Console
offers. Then installs from Play and from GitHub share one key and upgrade into
each other. If Play generates a new key instead, a user who switches source has
to uninstall first, which deletes the favorites.

**Native library.** Play builds APKs from the bundle for each device. Those
APKs keep `libckkaraoke.so` compressed, as `useLegacyPackaging` asks, so Android
still extracts it to disk where it can run. The server's ELF segments are
aligned at 64 KB, which meets Play's 16 KB page size requirement. To check a
bundle before uploading it, install the APKs Play would make:

```bash
bundletool build-apks --bundle app-release.aab --output app.apks --connected-device
bundletool install-apks --apks app.apks
```

**Play Console forms.** These answers follow from how the app works:

| Form | Answer |
|---|---|
| Privacy policy | [docs/PRIVACY.md](../../docs/PRIVACY.md) on GitHub |
| Data safety | no data collected, no data shared |
| Foreground service | `connectedDevice`: keeps the session with the karaoke unit on the local network open while the app is in use; needs a short video of the app controlling a unit |
| Ads | none |
| Target audience | adults; not directed at children |

The listing must not suggest an official app: say that CK Karaoke is unofficial
and not affiliated with the makers of the units.

## How it runs

`ServerService`, a foreground service of type `connectedDevice`, starts the
server with `ProcessBuilder`:

```
libckkaraoke.so -data <filesDir> -listen 127.0.0.1:0 [-addr <device>]
```

The server binds **loopback only**, so nothing else on the WiFi can drive the
karaoke unit through the phone. It prints `listening 127.0.0.1:<port>`, which
`Server.kt` reads before `MainActivity` points the WebView at it. The port
changes on every start.

Favorites live in `filesDir`, so they are per phone. Folders move between phones
by QR code or by backup file.

A `device_address` value in the app's shared preferences, if set, is passed as
`-addr`. Without it the server finds the unit by broadcast.

## Lifecycle rules

- **Never return `START_STICKY`.** A service the system restarts comes back with
  the app in the background, where `startForeground` throws
  `ForegroundServiceStartNotAllowedException`. With `START_STICKY` the system
  retries forever. `onStartCommand` returns `START_NOT_STICKY`, and `onCreate`
  catches that exception by class name (it is API 31; minSdk is 26) and stops
  quietly.
- **Backgrounding starts a ten-minute countdown.** `MainActivity.onStop` starts
  it and `onStart` cancels it. On expiry the service stops and takes the server
  with it, which closes the device session. Returning to the app after that
  starts a fresh server and a fresh WebView.
- **Stopping waits for a clean exit.** `Server.stop()` sends SIGTERM through
  `Process.destroy()`, which also closes the server's stdout and stderr at once.
  The server ignores `SIGPIPE` so that its next log line does not kill it, and
  `server.Run` waits for the device session to close. A clean stop takes about
  four seconds and exits with code 0. **Exit code 141** (SIGPIPE) or an instant
  stop means one of those two safeguards is missing.
- The Activity talks to the service through companion functions in the same
  process. A `startService` call from `onStop` would itself be a background start.

To check the restart policy on a device, run
`adb shell dumpsys activity services com.rrgmc.ckkaraoke`: `startCommandResult=1`
is `START_STICKY`, `2` is `START_NOT_STICKY`. A stuck restart record survives a
reinstall; clear it with `adb shell am force-stop com.rrgmc.ckkaraoke`.

## Permissions

The `connectedDevice` service type requires `FOREGROUND_SERVICE_CONNECTED_DEVICE`
**and** one permission from a fixed list of device-connection permissions.
`ACCESS_WIFI_STATE` is not on that list. Without a qualifying one, the service
throws `SecurityException` and the app dies on launch.
`CHANGE_WIFI_MULTICAST_STATE` is declared because finding the unit means
broadcasting on the local network.

The camera is optional. It is requested only when the QR scanner opens, and the
sync page also accepts a typed code.

## Camera and files

A WebView does none of these by itself:

| Action | Handled by |
|---|---|
| Scan a QR code | `WebChromeClient.onPermissionRequest` grants `RESOURCE_VIDEO_CAPTURE` after the system camera prompt |
| Save a backup | `setDownloadListener` opens `ACTION_CREATE_DOCUMENT` |
| Restore a backup | `onShowFileChooser` opens `ACTION_OPEN_DOCUMENT` |

The pickers list on-device storage, Drive and Dropbox side by side, and need no
storage permission and no `FileProvider`: on a save the app copies the server's
response straight into the stream the picker returns.

Rules that keep these working:

- **Answer every callback.** A `PermissionRequest` left unanswered hangs
  `getUserMedia` forever, and a file-chooser `ValueCallback` left uncalled
  leaves the file input dead for the life of the page. Cancellation calls
  `onReceiveValue(null)`.
- **Open with `*/*`.** Drive and Dropbox report MIME types of their own choosing,
  so filtering on `text/xml` can hide a backup that is plainly there.
- **Keep the export a plain navigation.** The download listener fires on the
  `Content-Disposition: attachment` header of a loopback navigation. The WebView
  cannot render a `blob:` URL, so a JavaScript blob download would do nothing.

## Time zone

Go's `time.Local` is always UTC on Android, and nothing reads `TZ`. So
`Server.passTimeZone` sets two variables on the `ProcessBuilder`:

```
CK_TZ=America/Sao_Paulo
CK_TZ_OFFSET=-10800
```

`server.LocalZone` turns them into a fixed zone and `server.Run` assigns
`time.Local`. The offset does the work, because Go cannot find Android's zoneinfo
database; the name is only for display. A fixed offset does not follow a DST
change, which is acceptable because the server restarts far more often than
that.
