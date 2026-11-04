#!/usr/bin/env bash
# Builds the Android app: the Go server first, then the APK or bundle around it.
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

# Both apps carry the same binary; the flavor tells it which backend to run
# (-backend unit or queue, from BuildConfig.BACKEND).
#
# assembleDebug by default. "release" builds the shareable APKs and "bundle" the
# App Bundles that Google Play takes. Both are signed with the release key when
# CK_KEYSTORE_FILE is set; see app/build.gradle.kts.
#
# A second argument, "unit" (CK Karaoke) or "queue" (CK Queue Remote), builds
# one app; without it both are built.
kind=Debug
step=assemble
case "${1:-}" in
release)
    kind=Release
    shift
    ;;
bundle)
    kind=Release
    step=bundle
    shift
    ;;
debug)
    shift
    ;;
esac
flavors="unit queue"
case "${1:-}" in
unit | queue)
    flavors=$1
    shift
    ;;
esac
tasks=()
outs=()
for f in $flavors; do
    F="$(tr '[:lower:]' '[:upper:]' <<<"${f:0:1}")${f:1}"
    tasks+=("$step$F$kind")
    lower=$(tr '[:upper:]' '[:lower:]' <<<"$kind")
    if [ "$step" = bundle ]; then
        outs+=("bundle/$f$kind/app-$f-$lower.aab")
    else
        outs+=("apk/$f/$lower/app-$f-$lower.apk")
    fi
done
task="${tasks[*]}"

echo "building the app ($task)"
cd ports/android
: "${JAVA_HOME:?set JAVA_HOME to a JDK 17-21 — see ports/android/README.md}"

# Gradle from PATH, or the newest 8.x any wrapper run has already cached
# (the Android plugin needs 8.13 or later).
GRADLE="${GRADLE:-$(command -v gradle || true)}"
if [ -z "$GRADLE" ]; then
    GRADLE=$(ls -d "$HOME"/.gradle/wrapper/dists/gradle-8.*/*/gradle-*/bin/gradle 2>/dev/null | sort -V | tail -1)
fi
: "${GRADLE:?no gradle found — set GRADLE to its path}"

"$GRADLE" "${tasks[@]}" "$@"

echo
for out in "${outs[@]}"; do
    echo "output: ports/android/app/build/outputs/$out"
done
