#!/usr/bin/env bash
# Builds the desktop app for the host platform and packages it, the way a
# release ships it. CI runs it on every push so that packaging problems show up
# before a tag, and the release workflow runs it to make the files it publishes.
#
#   ./ports/desktop/package.sh <version> <output dir>
#
# Output:
#   Windows  ck-karaoke-<version>-windows-amd64-setup.exe  (NSIS installer)
#            ck-karaoke-<version>-windows-amd64.exe        (portable)
#   macOS    ck-karaoke-<version>-macos-universal.dmg
#   Linux    ck-karaoke-<version>-linux-amd64.tar.gz       (CI check only)
#
# Needs the Wails CLI on PATH, NSIS on Windows, and WebKitGTK on Linux; see
# ports/desktop/README.md.
set -euo pipefail

version=${1:?usage: package.sh <version> <output dir>}
out=$(mkdir -p "${2:?usage: package.sh <version> <output dir>}" && cd "$2" && pwd)
cd "$(dirname "$0")"

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
        cp build/bin/ck-karaoke.exe "$out/ck-karaoke-$version-windows-amd64.exe"
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

        dmg="$out/ck-karaoke-$version-macos-universal.dmg"
        hdiutil create -volname "CK Karaoke" -srcfolder "$app" -ov -format UDZO "$dmg"
        hdiutil verify "$dmg"
        ;;

    Linux)
        wails build -clean -tags webkit2_41 -platform linux/amd64
        tar -czf "$out/ck-karaoke-$version-linux-amd64.tar.gz" -C build/bin ck-karaoke
        ;;

    *)
        fail "unsupported host $(uname -s)"
        ;;
esac

echo
ls -la "$out"
