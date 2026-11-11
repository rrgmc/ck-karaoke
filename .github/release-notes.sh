#!/usr/bin/env bash
# Writes the download section of the release notes to stdout: one table per
# application, with the main file for each platform. The release workflow puts
# it above the notes GitHub generates.
#
#   .github/release-notes.sh <dist-dir> <version> <owner/repo> <tag>
#
# Every file a table names must be in <dist-dir>, so a renamed build output
# fails the release instead of dropping a row. The App Bundles (.aab) are for
# Google Play and stay out of the tables.
set -euo pipefail

if [ $# -ne 4 ]; then
    echo "usage: $0 <dist-dir> <version> <owner/repo> <tag>" >&2
    exit 2
fi
dist=$1 version=$2 repo=$3 tag=$4
base="https://github.com/$repo/releases/download/$tag"
readme="https://github.com/$repo/blob/$tag/README.md"

# row <platform> <file> <notes>
row() {
    local file=$2
    if [ ! -f "$dist/$file" ]; then
        echo "release-notes: $dist/$file is missing" >&2
        exit 1
    fi
    printf '| %s | [%s](%s/%s) | %s |\n' "$1" "$file" "$base" "$file" "$3"
}

header() {
    printf '\n### %s\n\n%s\n\n| Platform | Download | Notes |\n|---|---|---|\n' "$1" "$2"
}

echo "## Downloads"
echo
echo "Download the file for your platform. The [README]($readme#download) explains how to install each one."

ios_guide="https://github.com/$repo/blob/$tag/ports/ios/README.md#installing-the-release-ipa"
smartscreen="If SmartScreen warns you, choose **More info**, then **Run anyway**."
apt() { printf 'Install it with `sudo apt install ./%s`.' "$1"; }

header "CK Karaoke" "The remote control for the karaoke unit."
row "Android 8 or later" "ck-karaoke-$version-android.apk" \
    "Allow installing apps from your browser or file manager when Android asks."
row "Windows 10 or 11" "ck-karaoke-$version-windows-amd64-setup.exe" "$smartscreen"
row "macOS 10.13 or later" "ck-karaoke-$version-macos-universal.dmg" \
    "Drag the app to Applications."
row "iPhone and iPad, iOS 15 or later" "ck-karaoke-$version-ios-unsigned.ipa" \
    "You must sign it before iOS installs it; see [Installing the release IPA]($ios_guide)."
row "Linux: Ubuntu 22.04, Debian 12 or later" "ck-karaoke-$version-linux-amd64.deb" \
    "$(apt "ck-karaoke-$version-linux-amd64.deb") It also installs the libraries the app needs."

header "CK Queue Manager" "The server that keeps a fair song queue. Install it on one computer on the same network as the unit."
row "Windows 10 or 11" "ck-queue-manager-$version-windows-amd64-setup.exe" "$smartscreen"
row "macOS 11 or later" "ck-queue-manager-$version-macos-universal.dmg" \
    "Drag the app to Applications. It appears in the menu bar, not the Dock."
row "Linux" "ck-queue-manager-$version-linux-amd64.deb" \
    "$(apt "ck-queue-manager-$version-linux-amd64.deb") The desktop needs a tray."

header "CK Queue Remote" "The guest app for the queue manager."
row "Android 8 or later" "ck-queue-remote-$version-android.apk" "Installs beside CK Karaoke."
row "iPhone and iPad, iOS 15 or later" "ck-queue-remote-$version-ios-unsigned.ipa" \
    "Sign it as for CK Karaoke."

echo
echo "\`SHA256SUMS.txt\` lists the checksum of every file. The \`.aab\` files are for Google Play; you do not need them."
echo
