#!/usr/bin/env bash
# Builds the Android app: the Go server first, then the APK around it.
#
# The server is packaged as a native library because that is the one directory
# an app may still execute a file from; see Server.kt. Only arm64 is built —
# it is the sole Android target Go links without cgo.
set -euo pipefail
cd "$(dirname "$0")/../.."

JNI=ports/android/app/src/main/jniLibs/arm64-v8a
mkdir -p "$JNI"

echo "building the server for android/arm64"
GOOS=android GOARCH=arm64 CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w" -o "$JNI/libckkaraoke.so" ./cmd/ck-karaoke-server

# assembleDebug by default; "release" builds the shareable APK instead. Both are
# signed with the same (debug) key — see app/build.gradle.kts for why.
task=assembleDebug
out=debug/app-debug.apk
if [ "${1:-}" = "release" ]; then
    task=assembleRelease
    out=release/app-release.apk
    shift
fi

echo "building the APK ($task)"
cd ports/android
: "${JAVA_HOME:?set JAVA_HOME to a JDK 17-21 — see ports/android/README.md}"

# Gradle from PATH, or the newest 8.x any wrapper run has already cached
# (the Android plugin needs 8.13 or later).
GRADLE="${GRADLE:-$(command -v gradle || true)}"
if [ -z "$GRADLE" ]; then
    GRADLE=$(ls -d "$HOME"/.gradle/wrapper/dists/gradle-8.*/*/gradle-*/bin/gradle 2>/dev/null | sort -V | tail -1)
fi
: "${GRADLE:?no gradle found — set GRADLE to its path}"

"$GRADLE" "$task" "$@"

echo
echo "APK: ports/android/app/build/outputs/apk/$out"
