#!/usr/bin/env bash
# Builds the desktop app for the host platform and packages it, the way a
# release ships it. CI runs it on every push so that packaging problems show up
# before a tag, and the release workflow runs it to make the files it publishes.
#
#   ./ports/desktop/package.sh <output dir> [version]
#
# The version defaults to version/VERSION.
#
# Output:
#   Windows  ck-karaoke-<version>-windows-amd64-setup.exe  (NSIS installer)
#   macOS    ck-karaoke-<version>-macos-universal.dmg
#   Linux    ck-karaoke-<version>-linux-amd64.deb
#
# Needs the Wails CLI on PATH, NSIS on Windows, and WebKitGTK on Linux; see
# ports/desktop/README.md.
#
# On macOS these variables sign and notarize the app. Without them the app is
# not signed.
#
#   MACOS_SIGN_IDENTITY    a Developer ID Application certificate in the
#                          keychain, by name or SHA-1 hash
#   MACOS_NOTARY_APPLE_ID  the Apple ID, an app-specific password for it, and
#   MACOS_NOTARY_PASSWORD  the team ID, for notarization. Signing without
#   MACOS_NOTARY_TEAM_ID   them still works, but macOS warns on first launch.
set -euo pipefail

out=$(mkdir -p "${1:?usage: package.sh <output dir> [version]}" && cd "$1" && pwd)
cd "$(dirname "$0")"
version=${2:-$(tr -d '[:space:]' < ../../version/VERSION)}

# The apps carry only the numeric part of the version: NSIS rejects anything
# but digits and dots, and so do macOS bundle versions. File names keep the
# whole version, suffix included.
numeric=${version%%-*}
[[ "$numeric" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "package.sh: bad version $version" >&2; exit 1; }

# Stamp the version into wails.json for this build only. Wails also rewrites
# the committed NSIS include on every Windows build, so put both back.
nsh=build/windows/installer/wails_tools.nsh
cp wails.json wails.json.orig
cp "$nsh" "$nsh.orig"
trap 'mv wails.json.orig wails.json; mv "$nsh.orig" "$nsh"' EXIT
jq --arg v "$numeric" '.info.productVersion = $v' wails.json.orig > wails.json

fail() { echo "package.sh: $*" >&2; exit 1; }

case "$(uname -s)" in
    MINGW* | MSYS* | CYGWIN*)
        wails build -clean -nsis -platform windows/amd64
        installer="build/bin/CK Karaoke-amd64-installer.exe"
        [ -f "$installer" ] || fail "no installer at $installer"
        cp "$installer" "$out/ck-karaoke-$version-windows-amd64-setup.exe"
        ;;

    Darwin)
        wails build -clean -platform darwin/universal
        app=$(find build/bin -maxdepth 1 -name '*.app' -print -quit)
        [ -n "$app" ] || { ls -la build/bin; fail "no .app bundle in build/bin"; }

        # The bundle must carry what macOS checks at run time. Without the
        # local network string the discovery broadcast is dropped silently.
        plist="$app/Contents/Info.plist"
        plutil -lint "$plist"
        for key in NSLocalNetworkUsageDescription NSCameraUsageDescription; do
            plutil -extract "$key" raw "$plist" >/dev/null || fail "$plist lacks $key"
        done
        got=$(plutil -extract CFBundleShortVersionString raw "$plist")
        [ "$got" = "$numeric" ] || fail "bundle version is $got, want $numeric"
        id=$(plutil -extract CFBundleIdentifier raw "$plist")
        [ "$id" = "com.rrgmc.ckkaraoke" ] || fail "bundle identifier is $id"
        exe="$app/Contents/MacOS/$(plutil -extract CFBundleExecutable raw "$plist")"
        archs=$(lipo -archs "$exe")
        case "$archs" in *x86_64*arm64* | *arm64*x86_64*) ;; *) fail "binary is $archs, not universal" ;; esac

        # The hardened runtime and a secure timestamp are what notarization
        # requires of every executable.
        if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
            codesign --force --options runtime --timestamp \
                --sign "$MACOS_SIGN_IDENTITY" "$app"
            codesign --verify --strict --verbose=2 "$app"
        fi

        dmg="$out/ck-karaoke-$version-macos-universal.dmg"
        hdiutil create -volname "CK Karaoke" -srcfolder "$app" -ov -format UDZO "$dmg"
        hdiutil verify "$dmg"

        if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
            codesign --force --timestamp --sign "$MACOS_SIGN_IDENTITY" "$dmg"
        fi
        # Notarizing the disk image covers the app inside it. Stapling attaches
        # the ticket, so the first launch needs no network to check it.
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
        wails build -clean -tags webkit2_41 -platform linux/amd64
        exe=build/bin/ck-karaoke
        [ -f "$exe" ] || fail "no binary at $exe"

        # Debian sorts "~" before anything, so 1.2.3~rc1 comes before 1.2.3.
        # A "-" would mark a Debian revision instead.
        debversion=${version/-/\~}

        # The libraries are named without the t64 suffix that Ubuntu 24.04
        # added; the renamed packages provide the old names, so one package
        # installs on both. The glibc floor is the newest symbol version the
        # binary links against.
        glibc=$(objdump -T "$exe" | grep -o 'GLIBC_[0-9.]*' | sed 's/GLIBC_//' | sort -V | tail -n 1)
        [ -n "$glibc" ] || fail "no glibc symbol versions in $exe"

        root=build/deb
        rm -rf "$root"
        install -Dm755 "$exe" "$root/usr/bin/ck-karaoke"
        install -Dm644 ../../webui/static/icon-512.png \
            "$root/usr/share/icons/hicolor/512x512/apps/ck-karaoke.png"
        install -d "$root/usr/share/applications" "$root/DEBIAN"
        cat > "$root/usr/share/applications/ck-karaoke.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=CK Karaoke
Comment=Remote control for compatible karaoke units
Exec=ck-karaoke
Icon=ck-karaoke
Terminal=false
Categories=AudioVideo;Audio;
EOF
        cat > "$root/DEBIAN/control" <<EOF
Package: ck-karaoke
Version: $debversion
Architecture: amd64
Maintainer: Rangel Reale <realerangel@gmail.com>
Installed-Size: $(du -sk --exclude=DEBIAN "$root" | cut -f1)
Depends: libc6 (>= $glibc), libgtk-3-0, libwebkit2gtk-4.1-0
Section: sound
Priority: optional
Homepage: https://github.com/rrgmc/ck-karaoke
Description: Remote control for compatible karaoke units
 Browse the song list, manage the queue and control playback on Videoke VSK,
 CooKara and RafKara karaoke units over the local network.
EOF
        chmod 644 "$root/usr/share/applications/ck-karaoke.desktop" "$root/DEBIAN/control"

        deb="$out/ck-karaoke-$version-linux-amd64.deb"
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
