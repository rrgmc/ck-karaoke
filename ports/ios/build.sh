#!/usr/bin/env bash
# Builds the iOS app: the Go server first, then the Xcode project around it.
#
# The server is linked in as a static archive rather than shipped as a separate
# executable the way the Android app does it, because iOS forbids exec — see
# ports/ios/bridge/bridge.go. Both an arm64 device slice and an arm64 simulator slice
# are built, because Go cannot tell them apart from GOARCH alone: the difference
# is the clang target triple.
#
# Run this once, and again whenever any Go code changes. Installing the app is
# then a matter of pressing Run in Xcode.
set -euo pipefail
cd "$(dirname "$0")/../.."

# Xcode, not the Command Line Tools: xcodebuild and the iOS SDKs only exist in
# the full install, and xcode-select may well be pointing at the other one.
XCODE="${XCODE:-/Applications/Xcode.app}"
if [ ! -d "$XCODE" ]; then
    echo "no Xcode at $XCODE — set XCODE to its path" >&2
    exit 1
fi
export DEVELOPER_DIR="$XCODE/Contents/Developer"

MIN_IOS=15.0
OUT=ports/ios/Frameworks
BUILD=ports/ios/build/lib
rm -rf "$OUT" "$BUILD"
mkdir -p "$OUT" "$BUILD/device/inc" "$BUILD/sim/inc"

# build_slice <sdk> <clang flags> <output dir>
build_slice() {
    local sdk=$1 flags=$2 dir=$3
    local sysroot
    sysroot=$(xcrun --sdk "$sdk" --show-sdk-path)
    echo "building the server for $sdk"
    CGO_ENABLED=1 GOOS=ios GOARCH=arm64 \
        CC="$(xcrun --sdk "$sdk" -f clang) $flags -isysroot $sysroot" \
        go build -buildmode=c-archive -trimpath -ldflags="-s -w" \
        -o "$dir/libckkaraoke.a" ./ports/ios/bridge
    # The bridging header includes it under this name.
    mv "$dir/libckkaraoke.h" "$dir/inc/ckkaraoke.h"
}

build_slice iphoneos "-arch arm64 -mios-version-min=$MIN_IOS" "$BUILD/device"
build_slice iphonesimulator "-target arm64-apple-ios$MIN_IOS-simulator" "$BUILD/sim"

echo "assembling the xcframework"
xcodebuild -create-xcframework \
    -library "$BUILD/device/libckkaraoke.a" -headers "$BUILD/device/inc" \
    -library "$BUILD/sim/libckkaraoke.a" -headers "$BUILD/sim/inc" \
    -output "$OUT/CKKaraoke.xcframework" >/dev/null

echo "generating the Xcode project"
# project.yml takes the version from these, through XcodeGen's ${...}
# expansion. The build number is the Android version code, so it rises with
# every release.
CK_VERSION=$(tr -d '[:space:]' < version/VERSION)
CK_VERSION=${CK_VERSION%%-*}
IFS=. read -r major minor patch <<< "$CK_VERSION"
CK_BUILD=$(( major * 10000 + minor * 100 + patch ))
export CK_VERSION CK_BUILD
XCODEGEN="${XCODEGEN:-$(command -v xcodegen || true)}"
: "${XCODEGEN:?no xcodegen found — brew install xcodegen, or set XCODEGEN}"
(cd ports/ios && "$XCODEGEN" generate --quiet)

echo
echo "open ports/ios/CKKaraoke.xcodeproj and press Run"
