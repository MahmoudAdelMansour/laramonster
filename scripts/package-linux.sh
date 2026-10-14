#!/bin/sh
# Packs the Linux build (bin/laramonster + bin/laramonster-app) into
# dist/LaraMonster-linux-amd64.tar.gz: unpack it and run ./install.sh.
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ARCH="${1:-amd64}"
STAGE="$(mktemp -d)/LaraMonster"
mkdir -p "$STAGE/bin" "$STAGE/build" "$STAGE/scripts" "$ROOT/dist"
cp "$ROOT/bin/laramonster" "$ROOT/bin/laramonster-app" "$STAGE/bin/"
cp -r "$ROOT/build/icons" "$ROOT/build/linux" "$STAGE/build/"
cp "$ROOT/scripts/install-linux.sh" "$STAGE/scripts/"
cat > "$STAGE/install.sh" <<'SH'
#!/bin/sh
# Installs LaraMonster for this user (no sudo): ~/.local/bin, app menu entry, icons.
exec "$(dirname "$0")/scripts/install-linux.sh" "$@"
SH
chmod +x "$STAGE/install.sh" "$STAGE/scripts/install-linux.sh"
tar -C "$(dirname "$STAGE")" -czf "$ROOT/dist/LaraMonster-linux-$ARCH.tar.gz" LaraMonster
echo "dist/LaraMonster-linux-$ARCH.tar.gz"
