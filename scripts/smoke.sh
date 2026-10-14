#!/usr/bin/env bash
# Smoke test on a clean machine (CI on Linux, macOS and Windows via Git Bash):
# install PHP, link a demo site, start the engine, load the site, stop.
# Fully isolated: its own LaraMonster home and ports, so it never touches a
# real install. Usage: scripts/smoke.sh path/to/laramonster [php-version]
set -euo pipefail
LM="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PHPV="${2:-8.4}"
WORK="$(mktemp -d)"
export LARAMONSTER_HOME="$WORK/home"
HTTP_PORT="${SMOKE_PORT:-18080}"
HTTPS_PORT=$((HTTP_PORT + 363))
trap '"$LM" stop >/dev/null 2>&1 || true; rm -rf "$WORK" 2>/dev/null || true' EXIT
step() { printf '\n== %s\n' "$*"; }
fail() {
	echo "SMOKE FAILED: $*"
	echo; echo "--- run dir"; ls -la "$LARAMONSTER_HOME/run" 2>&1 || true
	echo; echo "--- laramonster status"; LARAMONSTER_DEBUG=1 "$LM" status 2>&1 | head -20 || true
	for f in "$LARAMONSTER_HOME"/logs/*; do
		[ -f "$f" ] || continue
		echo; echo "--- $(basename "$f") (last 40 lines)"; tail -40 "$f"
	done
	"$LM" stop >/dev/null 2>&1 || true
	exit 1
}

step "version";       "$LM" --version
step "ports $HTTP_PORT / $HTTPS_PORT"; "$LM" port "$HTTP_PORT"; "$LM" port --https "$HTTPS_PORT"
step "install PHP $PHPV"; "$LM" php:install "$PHPV" || fail "php:install"
"$LM" php:list

step "demo site"
mkdir -p "$WORK/demo/public"
# What a Laravel app needs: the page says which of these PHP could not load.
cat > "$WORK/demo/public/index.php" <<'PHP'
<?php
$missing = array_filter(['mbstring', 'openssl', 'pdo_sqlite', 'curl', 'fileinfo', 'tokenizer', 'ctype'], fn ($e) => ! extension_loaded($e));
echo $missing ? 'missing-extensions: ' . implode(',', $missing) : 'lm-smoke-ok ' . PHP_VERSION;
PHP
(cd "$WORK/demo" && "$LM" link demo && "$LM" isolate "$PHPV") || fail "link / isolate"
"$LM" sites

step "start engine";  "$LM" start -b || fail "start"
body=""
for i in $(seq 1 30); do
	body="$(curl -s --max-time 5 --resolve "demo.localhost:$HTTP_PORT:127.0.0.1" "http://demo.localhost:$HTTP_PORT/" || true)"
	case "$body" in *lm-smoke-ok*) break ;; esac
	sleep 2
done
echo "response: $body"
case "$body" in *lm-smoke-ok\ "$PHPV"*) ;; *) fail "site did not answer with PHP $PHPV" ;; esac

step "stop";          "$LM" stop
echo; echo "SMOKE OK"
