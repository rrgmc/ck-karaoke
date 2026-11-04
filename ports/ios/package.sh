#!/usr/bin/env bash
# Builds the iOS app for devices and packages it as an unsigned IPA, the way a
# release ships it. CI runs it on every push so that packaging problems show up
# before a tag, and the release workflow runs it to make the file it publishes.
#
#   ./ports/ios/package.sh <output dir> [version]
#
# The version defaults to version/VERSION.
#
# Output: ck-karaoke-<version>-ios-unsigned.ipa and
# ck-queue-remote-<version>-ios-unsigned.ipa
#
# The IPA is not signed, so iOS will not install it as it is. See "Installing
# the release IPA" in ports/ios/README.md for how to sign it.
#
# Needs what build.sh needs: Xcode and xcodegen.
set -euo pipefail

out=$(mkdir -p "${1:?usage: package.sh <output dir> [version]}" && cd "$1" && pwd)
cd "$(dirname "$0")"
version=${2:-$(tr -d '[:space:]' < ../../version/VERSION)}

# The app carries only the numeric part of the version: bundle versions accept
# nothing but digits and dots. The build number is the Android version code, so
# it rises with every release. File names keep the whole version.
numeric=${version%%-*}
[[ "$numeric" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || { echo "package.sh: bad version $version" >&2; exit 1; }
code=$(( BASH_REMATCH[1] * 10000 + BASH_REMATCH[2] * 100 + BASH_REMATCH[3] ))

XCODE="${XCODE:-/Applications/Xcode.app}"
export XCODE DEVELOPER_DIR="$XCODE/Contents/Developer"

./build.sh

derived=build/package
rm -rf "$derived"

# package <scheme> <file name prefix>: one app, one IPA.
package() {
    local scheme=$1 prefix=$2
    xcodebuild -project CKKaraoke.xcodeproj -scheme "$scheme" \
        -configuration Release -sdk iphoneos -destination 'generic/platform=iOS' \
        -derivedDataPath "$derived" \
        MARKETING_VERSION="$numeric" CURRENT_PROJECT_VERSION="$code" \
        CODE_SIGNING_ALLOWED=NO \
        build

    # An IPA is a zip with the app bundle in a Payload directory.
    local staging="$derived/ipa-$scheme"
    rm -rf "$staging"
    mkdir -p "$staging/Payload"
    cp -R "$derived/Build/Products/Release-iphoneos/$scheme.app" "$staging/Payload/"
    local ipa="$out/$prefix-$version-ios-unsigned.ipa"
    rm -f "$ipa"
    (cd "$staging" && zip -qry "$ipa" Payload)
    echo "$ipa"
}

# CK Karaoke, then CK Queue Remote; see the targets in project.yml.
package CKKaraoke ck-karaoke
package CKQueueRemote ck-queue-remote
