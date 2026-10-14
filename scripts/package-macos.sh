#!/bin/sh
# macOS only: builds a universal LaraMonster.app (arm64 + x86_64) and zips it
# to dist/LaraMonster-macos-universal.zip. Signed + notarised when the Apple
# secrets are set (see .github/workflows/release.yml), unsigned otherwise.
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
if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
	# Developer ID signing (hardened runtime, the CLI inside first), then
	# Apple notarisation and stapling, so Gatekeeper opens it without warnings.
	codesign --force --timestamp --options runtime --sign "$MACOS_SIGN_IDENTITY" "$APP/Contents/MacOS/laramonster"
	codesign --force --timestamp --options runtime --sign "$MACOS_SIGN_IDENTITY" "$APP"
	codesign --verify --strict --verbose=2 "$APP"
	(cd "$WORK" && ditto -c -k --keepParent LaraMonster.app notarize.zip)
	xcrun notarytool submit "$WORK/notarize.zip" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" --wait
	xcrun stapler staple "$APP"
	spctl --assess --type execute --verbose "$APP"
else
	# No Developer ID: sign ad hoc, so the bundle is intact. macOS then says it
	# "can't verify" the app and offers Open Anyway in Privacy & Security
	# (README → Install). Unsigned bundles are reported as "damaged" instead.
	echo "MACOS_SIGN_IDENTITY not set: signing ad hoc (first launch needs Open Anyway)."
	codesign --force --sign - "$APP/Contents/MacOS/laramonster"
	codesign --force --sign - "$APP"
	codesign --verify --strict --verbose=2 "$APP"
fi
(cd "$WORK" && ditto -c -k --keepParent LaraMonster.app "$ROOT/dist/LaraMonster-macos-universal.zip")
echo "dist/LaraMonster-macos-universal.zip"
