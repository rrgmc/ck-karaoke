# Building CK Karaoke

This document is for developers. For installing and using the apps, see the
[README](../README.md). For using the Go packages in your own program, see
[LIBRARY.md](LIBRARY.md).

Every version of CK Karaoke is the same web UI served by the same Go server,
wrapped in a native app.

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
docs/                  protocol specification, architecture notes, this file
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
| Android | `./ports/android/build.sh` | [ports/android/README.md](../ports/android/README.md) |
| iOS | `./ports/ios/build.sh` (on a Mac) | [ports/ios/README.md](../ports/ios/README.md) |
| Windows, macOS, Linux | `cd ports/desktop && wails build` | [ports/desktop/README.md](../ports/desktop/README.md) |

The app ports bind the server to `127.0.0.1` only, so nothing else on the
network can drive the unit through them.

## Development

The [Taskfile](../Taskfile.yml) holds the common commands; it needs
[go-task](https://taskfile.dev). `task --list` shows them all, for example:

```bash
task check            # gofmt check, vet (library and desktop) and tests
task server -- -v     # the debug server
task android:install  # build the debug APK and install it over adb
task desktop:package  # package the desktop app into dist/ like a release
```

Without go-task, the plain commands are:

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

[CI](../.github/workflows/ci.yml) runs on every push to `master` and every pull
request. It tests the library on Linux, Windows and macOS, vets and builds the
desktop app on all three, builds the Android APK, and builds the iOS app for
the simulator. It produces no downloads.

The version lives in [version/VERSION](../version/VERSION). The Go binaries
embed it (package `version`; `ck-karaoke-server -version` prints it), and the
Android, iOS and desktop builds take their version from it. The Android version
code and the iOS build number are `MAJOR×10000 + MINOR×100 + PATCH`.

Binaries are built only for a release. To make one, set the new version in
`version/VERSION`, commit and push it, then push the matching tag. The tag
starts the [release workflow](../.github/workflows/release.yml), which checks
that it matches `version/VERSION`, runs CI, then publishes a GitHub release with
the Android APK, the Windows installer, the macOS disk image, the Linux `.deb`
package, an unsigned iOS IPA and a `SHA256SUMS.txt`:

```bash
git tag v1.2.3
git push origin v1.2.3
```

The version must be `MAJOR.MINOR.PATCH`, with minor and patch below 100; a
suffix such as `1.2.3-rc1` publishes a pre-release.

The APK is signed with the release key from these repository secrets:

| Secret | Value |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | the keystore file, base64-encoded |
| `ANDROID_KEYSTORE_PASSWORD` | the keystore password |
| `ANDROID_KEY_ALIAS` | the key alias |
| `ANDROID_KEY_PASSWORD` | the key password |

Without them the APK is signed with a throwaway debug key, and the next
release will not install over it.

The macOS disk image is signed with a Developer ID certificate and notarized by
Apple, so macOS opens it without a warning. Notarization means Apple scans the
app and records that it is safe to run. It uses these secrets:

| Secret | Value |
|---|---|
| `MACOS_CERTIFICATE_BASE64` | the Developer ID Application certificate and its private key, as a `.p12` file, base64-encoded |
| `MACOS_CERTIFICATE_PASSWORD` | the password of the `.p12` file |
| `MACOS_NOTARY_APPLE_ID` | the Apple ID of the developer account |
| `MACOS_NOTARY_PASSWORD` | an app-specific password for that Apple ID |
| `MACOS_NOTARY_TEAM_ID` | the team ID, `5XV6P36QZR` |

To create them:

1. In Keychain Access, open **My Certificates**, select the
   "Developer ID Application" certificate, and choose **File**, **Export
   Items**. Save it as `certificate.p12` with a password.
2. Create an app-specific password at [account.apple.com](https://account.apple.com),
   under **Sign-In and Security**, **App-Specific Passwords**.
3. Store the secrets, then delete `certificate.p12`:

   ```bash
   base64 -i certificate.p12 | gh secret set MACOS_CERTIFICATE_BASE64
   gh secret set MACOS_CERTIFICATE_PASSWORD
   gh secret set MACOS_NOTARY_APPLE_ID
   gh secret set MACOS_NOTARY_PASSWORD
   gh secret set MACOS_NOTARY_TEAM_ID --body 5XV6P36QZR
   ```

The release fails if any of them is missing. CI's build on every push is not
signed.

To sign and notarize a local build, set `MACOS_SIGN_IDENTITY` to the name of
the certificate in your keychain, and the three `MACOS_NOTARY_*` values, before
running `ports/desktop/package.sh`:

```bash
MACOS_SIGN_IDENTITY="Developer ID Application: Rangel Reale (5XV6P36QZR)" \
MACOS_NOTARY_APPLE_ID=... MACOS_NOTARY_PASSWORD=... MACOS_NOTARY_TEAM_ID=5XV6P36QZR \
  ./ports/desktop/package.sh dist
```

The Windows installer is not code-signed. The iOS IPA is not signed either, and
iOS installs only signed apps: see [Installing the release IPA](../ports/ios/README.md#installing-the-release-ipa)
for how to sign it.

## Screenshots

The screenshots in [screenshots/](screenshots/) come from the debug server
connected to a real unit, captured in a 390×844 phone viewport at 2× scale.

## Further reading

- [PROTOCOL.md](PROTOCOL.md): the device protocol.
- [ARCHITECTURE.md](ARCHITECTURE.md): how the server and the ports fit together,
  and the design rules that keep them working.
