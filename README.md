# LaraMonster

A local PHP & Laravel development environment, in the spirit of Laravel Herd, for **Linux, macOS and Windows**.
The UI follows the [monstersgate.com](https://monstersgate.com) look: royal blue on deep navy, Inter + condensed display type, pill buttons.

One binary gives you:

- **Sites**: park a folder and every subfolder becomes `<folder>.localhost`, or link single projects. Laravel `public/` is detected.
- **Embedded web server**: Caddy runs inside LaraMonster and hot-reloads on every change. No nginx to install.
- **Multiple PHP versions**: one-click installs of static PHP builds (static-php.dev on Linux/macOS, windows.php.net on Windows). There's a default version plus per-site pinning, and system PHP installs are detected too.
- **`php` / `composer` shims** that follow the PHP version of the site you're in.
- **HTTPS**: a local certificate authority, per-site `secure`, and HTTP→HTTPS redirects.
- **`.test` domains**: a built-in DNS server, wired up via `/etc/resolver` (macOS) or systemd-resolved (Linux), or a managed hosts-file block as the fallback.
- **Services**: MySQL/MariaDB, PostgreSQL, Redis/Valkey, Mailpit and Meilisearch. Data lives in LaraMonster's home, and each service shows a ready-made `.env` snippet.
- **New-project wizard** (dashboard or `laramonster new`): starter kit (Laravel, React, Vue, Livewire, blank), custom domain, PHP version, database (created for you), Mail/Redis/HTTPS wiring, curated packages with **AI** support (Laravel Boost, Laravel AI, MCP, Prism…), live **Packagist search**, migrations, `npm build` and a first Git commit.
- **Mail inbox**: every email your apps send (SMTP or PHP `mail()`) shows up in the dashboard, with HTML, text and source views (Mailpit under the hood).
- **Tools**: one-click installs for Composer, Node.js, PostgreSQL, Mailpit and Meilisearch (portable downloads), plus MariaDB, Redis/Valkey, Git and certutil through your package manager (pacman, apt, dnf, zypper, brew, winget, scoop).
- **Desktop app** with a tray icon. Every color pair meets WCAG AA in light and dark mode. An optional **browser dashboard** (`laramonster dashboard on`) serves the same UI at `http://dashboard.laramonster.localhost:8080` for setups without the app; it works on phones and tablets too.
- **Autostart** at login (systemd user unit, launchd agent, Windows logon task).

## LaraMonster Pro

Two extra tools for Laravel sites, unlocked by a license from [monstersgate.com/laramonster](https://monstersgate.com/laramonster) (sign in under **Account** with your monstersgate.com email, or a license key):

- **Events**: turn on recording for a site and every request, command and queued job is traced: the events it fired (nested, timed), their payloads, which listeners ran and what they returned, and errors. Dispatch any event from a form built from its constructor (or raw PHP), and export an event guide, job guide or trace as a PDF for your team.
- **Jobs & Queues**: each job explained in plain words (what `handle()` does, its schedule, where it's dispatched, retries and timeouts), run it now or queue it (form, raw PHP, or any artisan command), see queue sizes and failed jobs (retry, forget), and start workers that LaraMonster supervises.

Recording uses a small PHP agent loaded with `auto_prepend_file` only by pools that serve a tracked site; it never changes what the app does. One computer at a time: signing in elsewhere signs this one out (the owner gets an email). Offline, Pro keeps working for 3 days after the last license check. `LARAMONSTER_ACCOUNT_URL` points the app at another server for testing.

## Build

```sh
make cli        # bin/laramonster      (pure Go, cross-compiles anywhere)
make app        # bin/laramonster-app  (desktop app; needs webkit2gtk-4.1 on Linux)
make test
make release    # dist/: CLI for linux/darwin/windows × amd64/arm64 + Windows app
make install    # Linux: ~/.local/bin + app menu entry
```

`make install DESKTOP=1` also adds a desktop shortcut. Icons are installed in every size, and the icon and KDE menu caches are refreshed.

Desktop app requirements: Linux needs `gtk3` + `webkit2gtk-4.1` (or build with `LINUX_APP_TAGS=` for GTK4/webkitgtk-6.0). macOS needs Xcode command line tools. Windows needs WebView2, which is preinstalled on Windows 10/11.

## Quick start

Open the LaraMonster app. On first launch a short setup picks your projects folder, installs PHP, adds `php`/`composer`/`laramonster` to your terminal, turns on start-at-login and trusts local HTTPS. You can run it again from Settings.

Or from the terminal:

```sh
laramonster setup               # shims on PATH, autostart, start the engine
laramonster php:install 8.4     # optional: system PHP is used if present
laramonster park ~/Code         # ~/Code/blog -> http://blog.localhost:8080
laramonster trust               # trust the local CA once (asks for your password)
laramonster dashboard           # open the app
```

Ports default to 8080/8443 so no root is needed. For 80/443 on Linux:

```sh
sudo setcap cap_net_bind_service=+ep $(readlink -f $(which laramonster))
laramonster port 80 && laramonster port --https 443
```

For `.test` instead of `.localhost`:

```sh
laramonster tld test && laramonster dns setup
```

## Commands

| | |
|---|---|
| **Engine** | `start [-b]` `stop` `restart` `reload` `status [--json]` `dashboard [on\|off] [--browser]` `logs [name] [-f]` |
| **Sites** | `park [dir]` `forget [dir]` `paths` `link [name]` `unlink [name]` `sites` `open [name]` `edit [name]` `secure [name]` `unsecure [name]` `isolate <ver>` `unisolate` `new <name> [-t laravel\|blank]` |
| **PHP** | `php [args]` `composer [args]` `php:list` `php:available` `php:install <ver…>` `php:uninstall <ver>` `use <ver>` `which` |
| **Services** | `services` · `service start\|stop\|restart\|enable\|disable\|install\|env\|port <name> [port]` · `tools` · `tools install <id…>` |
| **System** | `setup` `dns status\|setup\|remove` `trust` `untrust` `autostart [on\|off]` `tld [tld]` `port [port] [--https]` |

## How it works

```
 laramonster (CLI) ──┐                        ┌── Caddy (embedded) ── *.localhost / *.test
 desktop app ────────┼─ unix socket ─ engine ─┼── php-fpm pool per PHP version (php-cgi on Windows)
 dashboard (browser)─┘   /api + UI            ├── services: mariadb, postgres, redis, mailpit, meilisearch
                                              └── DNS server for *.<tld>
```

- **Config**: `~/.config/laramonster/config.json` (macOS: `~/Library/Application Support/laramonster`, Windows: `%AppData%\laramonster`). Override it with `LARAMONSTER_HOME`.
- **Managed files**: everything else lives under the same home directory: `php/<ver>`, `services/<name>/{bin,data}`, `data/` (CA and certificates), `logs/`, `run/` (sockets) and `bin/` (shims).
- **Dashboard and API**: the desktop app serves the UI itself and talks to the engine's socket directly. The optional browser dashboard (off by default) is served at `dashboard.laramonster.<tld>` and the Mailpit UI at `mail.laramonster.<tld>`. Site names can't contain dots, so these never collide with a project folder. Caddy proxies it to the engine's socket. API calls must carry an `X-LaraMonster` header, which a website in your browser can't add, so other sites can't control LaraMonster through localhost.

## Notes

- **PHP builds**: they come from static-php-cli's "common" set, which has every PDO driver Laravel needs (sqlite, mysql, pgsql) but no `intl`, `sodium` or `opcache`. Filament needs `intl`, so use a system PHP for it.
- **Port conflicts**: LaraMonster refuses to start when its ports are already taken, for example by a second LaraMonster, nginx or Apache.
- **Child processes**: if the engine dies, its children (php-fpm, databases) are stopped with it on Linux.

## Code layout

```
cmd/laramonster        CLI
cmd/laramonster-app    desktop app (Wails v3: window + tray)
internal/daemon        engine runtime + HTTP API
internal/core          shared operations (sites, PHP, composer, new project, jobs)
internal/server        Caddyfile generation + embedded Caddy
internal/php           PHP discovery, installer, php-fpm / php-cgi pools
internal/services      service definitions and process manager
internal/dns           DNS server, per-OS resolver setup, hosts file
internal/trust         local CA trust store install
internal/elevate       sudo / pkexec / osascript / UAC
internal/tools         tool catalog: portable downloads + package managers
internal/autostart     background start + login items
internal/ui            dashboard (HTML/CSS/Alpine.js, embedded)
```

## Credits

The mascot and visual style come from MonstersGate. PHP builds are from [static-php-cli](https://github.com/crazywhalecc/static-php-cli) and [windows.php.net](https://windows.php.net). Built on [Caddy](https://caddyserver.com) and [Wails](https://wails.io).
