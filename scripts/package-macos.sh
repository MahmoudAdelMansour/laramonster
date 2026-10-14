#!/bin/sh
# macOS only: builds a universal LaraMonster.app (arm64 + x86_64) and zips it
# to dist/LaraMonster-macos-universal.zip. Unsigned until a Developer ID is set up.
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${VERSION:-0.1.0-dev}"
LDFLAGS="-s -w -X laramonster/internal/core.Version=$VERSION"
WORK="$(mktemp -d)"
cd "$ROOT"
for arch in arm64 amd64; do
	cc_arch=$([ "$arch" = amd64 ] && echo x86_64 || echo arm64)
	CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $cc_arch" \
		go build -tags production -ldflags "$LDFLAGS" -o "$WORK/app-$arch" ./cmd/laramonster-app
	CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -ldflags "$LDFLAGS" -o "$WORK/cli-$arch" ./cmd/laramonster
done
APP="$WORK/LaraMonster.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/laramonster-app" "$WORK/app-arm64" "$WORK/app-amd64"
# The CLI ships next to the app binary (case-insensitive APFS: never name
# both "LaraMonster"/"laramonster"); the app finds it there (cliExecutable).
lipo -create -output "$APP/Contents/MacOS/laramonster" "$WORK/cli-arm64" "$WORK/cli-amd64"
sed "s/__VERSION__/${VERSION#v}/g" build/darwin/Info.plist > "$APP/Contents/Info.plist"
ICONSET="$WORK/icon.iconset"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
	sips -z $s $s build/appicon-1024.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
	d=$((s * 2)); sips -z $d $d build/appicon-1024.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/icon.icns"
mkdir -p dist
(cd "$WORK" && ditto -c -k --keepParent LaraMonster.app "$ROOT/dist/LaraMonster-macos-universal.zip")
echo "dist/LaraMonster-macos-universal.zip"
