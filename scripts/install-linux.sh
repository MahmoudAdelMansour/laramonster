#!/bin/sh
# Installs LaraMonster for the current user: binaries in ~/.local/bin, an app
# menu entry and icon. Run `make install`, or pass --uninstall to remove it.
set -eu

BIN="${HOME}/.local/bin"
APPS="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
HICOLOR="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor"
DESKTOP_DIR="$(xdg-user-dir DESKTOP 2>/dev/null || echo "$HOME/Desktop")"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [ "${1:-}" = "--uninstall" ]; then
	"$BIN/laramonster" autostart off 2>/dev/null || true
	"$BIN/laramonster" stop 2>/dev/null || true
	rm -f "$BIN/laramonster" "$BIN/laramonster-app" "$APPS/laramonster.desktop" "$DESKTOP_DIR/laramonster.desktop"
	rm -f "$HICOLOR"/*/apps/laramonster.png
	echo "LaraMonster removed (your config and data in ~/.config/laramonster are kept)."
	exit 0
fi

mkdir -p "$BIN" "$APPS"
install -m 755 "$ROOT/bin/laramonster" "$BIN/laramonster"
[ -f "$ROOT/bin/laramonster-app" ] && install -m 755 "$ROOT/bin/laramonster-app" "$BIN/laramonster-app"

# Every standard icon size, so panels and menus never upscale a tiny bitmap.
for f in "$ROOT"/build/icons/laramonster-*.png; do
	size="${f##*-}"; size="${size%.png}"
	install -D -m 644 "$f" "$HICOLOR/${size}x${size}/apps/laramonster.png"
done
# A user hicolor dir without index.theme confuses some icon lookups.
[ -f "$HICOLOR/index.theme" ] || cp /usr/share/icons/hicolor/index.theme "$HICOLOR/" 2>/dev/null || true
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -f -t -q "$HICOLOR" || true

sed "s#^Exec=.*#Exec=$BIN/laramonster-app %u#" "$ROOT/build/linux/laramonster.desktop" > "$APPS/laramonster.desktop"
command -v update-desktop-database >/dev/null && update-desktop-database "$APPS" 2>/dev/null || true
# laramonster:// links ("Open in LaraMonster" in license emails) open the app.
command -v xdg-mime >/dev/null && xdg-mime default laramonster.desktop x-scheme-handler/laramonster 2>/dev/null || true
# Refresh KDE's menu/launcher cache so the new entry and icon show up now.
for k in kbuildsycoca6 kbuildsycoca5; do command -v $k >/dev/null && $k >/dev/null 2>&1 && break; done || true

if [ "${1:-}" = "--desktop-shortcut" ] && [ -d "$DESKTOP_DIR" ]; then
	install -m 755 "$APPS/laramonster.desktop" "$DESKTOP_DIR/laramonster.desktop"
	command -v gio >/dev/null && gio set "$DESKTOP_DIR/laramonster.desktop" metadata::trusted true 2>/dev/null || true
	echo "Desktop shortcut added."
fi

# A running login service still runs the old binary: restart it onto the new one.
if command -v systemctl >/dev/null && systemctl --user is-active --quiet laramonster.service 2>/dev/null; then
	systemctl --user restart laramonster.service && echo "Restarted the LaraMonster engine."
fi

echo "Installed to $BIN"
case ":$PATH:" in *":$BIN:"*) ;; *) echo "Note: add $BIN to your PATH." ;; esac
echo
echo "Optional, to serve sites on ports 80/443 instead of 8080/8443:"
echo "  sudo setcap cap_net_bind_service=+ep $BIN/laramonster $BIN/laramonster-app"
echo "  laramonster port 80 && laramonster port --https 443"
echo
echo "Next: run 'laramonster setup', or open LaraMonster from your app menu."
