#!/usr/bin/env bash
# Builds the queue manager's tray app for the host platform and packages it,
# the way a release ships it. CI runs it on every push so that packaging
# problems show up before a tag, and the release workflow runs it to make the
# files it publishes.
#
#   ./ports/queue-desktop/package.sh <output dir> [version]
#
# The version defaults to version/VERSION.
#
# Output:
#   Windows  ck-queue-manager-<version>-windows-amd64-setup.exe  (NSIS installer)
#   macOS    ck-queue-manager-<version>-macos-universal.dmg
#   Linux    ck-queue-manager-<version>-linux-amd64.deb
#
# Needs Go; NSIS on Windows; on macOS also Xcode's command line tools (cgo, lipo,
# iconutil); on Linux a C compiler and the GTK 3 and WebKitGTK 4.1 development
# packages, for the configuration window.
#
# On macOS the same variables as ports/desktop/package.sh sign and notarize
# the app. Without them the app is not signed.
#
#   MACOS_SIGN_IDENTITY    a Developer ID Application certificate in the
#                          keychain, by name or SHA-1 hash
#   MACOS_NOTARY_APPLE_ID  the Apple ID, an app-specific password for it, and
#   MACOS_NOTARY_PASSWORD  the team ID, for notarization.
#   MACOS_NOTARY_TEAM_ID
set -euo pipefail

out=$(mkdir -p "${1:?usage: package.sh <output dir> [version]}" && cd "$1" && pwd)
cd "$(dirname "$0")"
version=${2:-$(tr -d '[:space:]' < ../../version/VERSION)}

# Bundle versions take only the numeric part; file names keep the whole
# version, suffix included.
numeric=${version%%-*}
[[ "$numeric" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "package.sh: bad version $version" >&2; exit 1; }

fail() { echo "package.sh: $*" >&2; exit 1; }

# The configuration window is a Wails web view, which needs these tags.
tags=desktop,production

build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT

case "$(uname -s)" in
    MINGW* | MSYS* | CYGWIN*)
        # The GUI subsystem, so no console window opens beside the tray icon.
        exe="$build/ck-queue-manager-tray.exe"
        GOOS=windows GOARCH=amd64 go build -trimpath -tags "$tags" -ldflags=-H=windowsgui -o "$exe" .
        # An NSIS installer, as for the CK Karaoke app; see installer.nsi.
        makensis=$(command -v makensis || echo "/c/Program Files (x86)/NSIS/makensis.exe")
        [ -x "$makensis" ] || fail "makensis not found; install NSIS"
        setup="$out/ck-queue-manager-$version-windows-amd64-setup.exe"
        "$makensis" -V2 -DVERSION="$version" -DNUMERIC="$numeric" \
            -DEXE="$(cygpath -w "$exe")" -DOUTFILE="$(cygpath -w "$setup")" installer.nsi
        [ -f "$setup" ] || fail "no installer at $setup"
        ;;

    Darwin)
        # fyne.io/systray and Wails need cgo on macOS, so each architecture
        # is built natively and the two are joined.
        for arch in arm64 amd64; do
            CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -tags "$tags" -o "$build/tray-$arch" .
        done
        app="$build/CK Queue Manager.app"
        install -d "$app/Contents/MacOS" "$app/Contents/Resources"
        lipo -create -output "$app/Contents/MacOS/ck-queue-manager-tray" "$build/tray-arm64" "$build/tray-amd64"
        archs=$(lipo -archs "$app/Contents/MacOS/ck-queue-manager-tray")
        case "$archs" in *x86_64*arm64* | *arm64*x86_64*) ;; *) fail "binary is $archs, not universal" ;; esac

        # The icon, from the same artwork as the CK Karaoke app.
        iconset="$build/icon.iconset"
        mkdir "$iconset"
        for size in 16 32 128 256 512; do
            sips -z $size $size ../desktop/build/appicon.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
            sips -z $((size * 2)) $((size * 2)) ../desktop/build/appicon.png \
                --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
        done
        iconutil -c icns "$iconset" -o "$app/Contents/Resources/icon.icns"

        # LSUIElement keeps the tray out of the Dock: it lives in the menu
        # bar. The configuration window's process makes itself a regular app,
        # so the window has a Dock icon and a menu bar while it is open. The
        # local network string is what macOS shows before the app may find
        # the unit and serve the phones; NSBonjourServices names the service
        # it announces.
        cat > "$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleName</key><string>CK Queue Manager</string>
    <key>CFBundleDisplayName</key><string>CK Queue Manager</string>
    <key>CFBundleExecutable</key><string>ck-queue-manager-tray</string>
    <key>CFBundleIdentifier</key><string>com.rrgmc.ckqueuemanager</string>
    <key>CFBundleIconFile</key><string>icon</string>
    <key>CFBundleVersion</key><string>$numeric</string>
    <key>CFBundleShortVersionString</key><string>$numeric</string>
    <key>LSMinimumSystemVersion</key><string>11.0</string>
    <key>LSUIElement</key><true/>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSLocalNetworkUsageDescription</key>
    <string>The queue manager finds the karaoke unit on your network and lets phones queue songs on it.</string>
    <key>NSBonjourServices</key>
    <array><string>_karaokemachine._tcp</string></array>
</dict>
</plist>
EOF
        plutil -lint "$app/Contents/Info.plist"

        # The hardened runtime and a secure timestamp are what notarization
        # requires of every executable.
        if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
            codesign --force --options runtime --timestamp --sign "$MACOS_SIGN_IDENTITY" "$app"
            codesign --verify --strict --verbose=2 "$app"
        fi

        dmg="$out/ck-queue-manager-$version-macos-universal.dmg"
        # The image holds the app and a link to /Applications, so the user can
        # drag one onto the other. ditto keeps the app's signature intact.
        stage=$(mktemp -d)
        ditto "$app" "$stage/$(basename "$app")"
        ln -s /Applications "$stage/Applications"
        hdiutil create -volname "CK Queue Manager" -srcfolder "$stage" -ov -format UDZO "$dmg"
        rm -rf "$stage"
        hdiutil verify "$dmg"
        if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
            codesign --force --timestamp --sign "$MACOS_SIGN_IDENTITY" "$dmg"
        fi
        if [ -n "${MACOS_NOTARY_APPLE_ID:-}" ]; then
            [ -n "${MACOS_SIGN_IDENTITY:-}" ] || fail "notarization needs MACOS_SIGN_IDENTITY"
            notary=(--apple-id "$MACOS_NOTARY_APPLE_ID"
                --password "${MACOS_NOTARY_PASSWORD:?MACOS_NOTARY_PASSWORD is not set}"
                --team-id "${MACOS_NOTARY_TEAM_ID:?MACOS_NOTARY_TEAM_ID is not set}")
            # notarytool exits 0 for a rejected submission too, so read the
            # status, and print Apple's log when it is not Accepted.
            result=$(xcrun notarytool submit "$dmg" --wait --output-format json "${notary[@]}")
            status=$(plutil -extract status raw - <<<"$result")
            if [ "$status" != "Accepted" ]; then
                xcrun notarytool log "$(plutil -extract id raw - <<<"$result")" "${notary[@]}" || true
                fail "notarization status is $status"
            fi
            xcrun stapler staple "$dmg"
            xcrun stapler validate "$dmg"
            spctl --assess --type open --context context:primary-signature --verbose=2 "$dmg"
        fi
        ;;

    Linux)
        # The tray talks D-Bus in pure Go, but the configuration window links
        # GTK and WebKitGTK through cgo.
        exe="$build/ck-queue-manager-tray"
        CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -tags "$tags,webkit2_41" -o "$exe" .

        # As in ports/desktop/package.sh: the library names without Ubuntu
        # 24.04's t64 suffix, which the renamed packages provide, and the
        # newest glibc symbol version the binary links against.
        glibc=$(objdump -T "$exe" | grep -o 'GLIBC_[0-9.]*' | sed 's/GLIBC_//' | sort -V | tail -n 1)
        [ -n "$glibc" ] || fail "no glibc symbol versions in $exe"

        # Debian sorts "~" before anything, so 1.2.3~rc1 comes before 1.2.3.
        debversion=${version/-/\~}
        root="$build/deb"
        install -Dm755 "$exe" "$root/usr/bin/ck-queue-manager-tray"
        install -Dm644 ../../webui/static/icon-512.png \
            "$root/usr/share/icons/hicolor/512x512/apps/ck-queue-manager.png"
        install -d "$root/usr/share/applications" "$root/DEBIAN"
        cat > "$root/usr/share/applications/ck-queue-manager.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=CK Queue Manager
Comment=Fair song queue for compatible karaoke units, controlled from KM Remote
Exec=ck-queue-manager-tray
Icon=ck-queue-manager
StartupWMClass=ck-queue-manager
Terminal=false
Categories=AudioVideo;Audio;
EOF
        cat > "$root/DEBIAN/control" <<EOF
Package: ck-queue-manager
Version: $debversion
Architecture: amd64
Maintainer: Rangel Reale <realerangel@gmail.com>
Installed-Size: $(du -sk --exclude=DEBIAN "$root" | cut -f1)
Depends: libc6 (>= $glibc), libgtk-3-0, libwebkit2gtk-4.1-0
Section: sound
Priority: optional
Homepage: https://github.com/rrgmc/ck-karaoke
Description: Fair song queue for compatible karaoke units
 Holds the session with a Videoke VSK, CooKara or RafKara unit, keeps the
 song queue with an optional fair order, and serves KaraokeMachine's API so
 that KM Remote queues songs on the unit. Runs in the system tray.
EOF
        chmod 644 "$root/usr/share/applications/ck-queue-manager.desktop" "$root/DEBIAN/control"

        deb="$out/ck-queue-manager-$version-linux-amd64.deb"
        dpkg-deb --root-owner-group -Zxz --build "$root" "$deb"
        dpkg-deb --info "$deb"
        dpkg-deb --contents "$deb"
        got=$(dpkg-deb --field "$deb" Version)
        [ "$got" = "$debversion" ] || fail "package version is $got, want $debversion"
        ;;

    *)
        fail "unsupported host $(uname -s)"
        ;;
esac

echo
ls -la "$out"
