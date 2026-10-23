# Windows: moving the home to `%USERPROFILE%\.laramonster`

## Why

The Microsoft Store version runs in an MSIX package. Every write to
`%AppData%\laramonster` is silently redirected to
`%LocalAppData%\Packages\Laramonster.LaraMonster_<id>\LocalCache\Roaming\laramonster`.
Inside LaraMonster both paths work; outside it only the second one exists. That broke:

- the manual HTTPS step (`certutil … AppData\Roaming\…\root.crt` → `ERROR_PATH_NOT_FOUND`)
- the `php` / `composer` shims: the user PATH named a `bin` folder that terminals can't see
- anything that started `laramonster.exe` by its `WindowsApps` path (shims, the logon task): it ran
  outside the package and saw an empty home

The user profile itself is not redirected, so `%USERPROFILE%\.laramonster` looks the same to
LaraMonster, PowerShell, Explorer and every terminal. The plain `.exe` moves too, so both builds use
one location.

## What happens on the first start of the new version

All in `daemon.Run` (`internal/daemon/daemon.go`), after the "already running" check and before
logs, PHP or databases open any file:

1. `paths.MoveHome()` (`internal/paths/home_windows.go`):
   - skips when `LARAMONSTER_HOME` is set, the new home is already in use, or there is no old home
     ("in use" = has `config.json`, `data\pki` or a PHP version)
   - sets aside a new home that only holds empty folders (`.laramonster-unused-<time>`, never deleted)
   - renames the old home's **real** folder (`LocalCache\…` for the Store version) to the new home.
     One rename on the same drive: instant, and Windows refuses it while any file inside is open,
     so it moves whole or not at all
   - writes `moved-from.txt` in the new home
2. `core.AfterHomeMove(old)` (`internal/core/home.go`):
   - rewrites `extension_dir` in every `php\<version>\php.ini` (it holds the absolute old path)
   - rewrites the `php`, `composer`, `laramonster`, `node`, `npm`, `npx` shims
   - swaps the old `bin` for the new one in the user PATH
3. Both are logged in `logs\laramonster.log`.

Until the move succeeds, `paths.Root()` keeps returning the old home, so the CLI and the app work
as before. A failed move (a file still open, e.g. a database left running by a crash) is logged
and tried again next start.

## What stays the same

- Sites, parked folders, PHP versions, databases (MySQL, PostgreSQL, Redis data), Mailpit,
  account, settings: all inside the moved folder
- **The certificate authority** (`data\pki`): the same CA, so nobody has to trust HTTPS again
- Projects: they live in the user's own folders and aren't touched
- Linux and macOS: no change

## Not covered (by design)

- **Downgrading** after the move: an older version looks in `%AppData%` and starts empty. The data
  is safe in `%USERPROFILE%\.laramonster`; moving the folder back by hand restores it. Mention it
  in the release notes.
- **A home on another drive** (redirected profile folders): the rename fails, LaraMonster keeps
  the old home and logs why. Copying databases while they might be in use isn't worth the risk.
- Both an old `.exe` home and a Store home on the same PC: the Store version moves the one it was
  using (`LocalCache`); the other stays where it is.

## Test on real Windows before release

Run on Windows 11 and Windows 10, once with the Store package (`scripts/package-msix.ps1`,
sideloaded) and once with the `.exe`, each starting from an install of the **previous** release:

1. Previous release: finish setup, park a folder, install PHP 8.4 + an extension (e.g. Redis),
   start MySQL and create a database, trust HTTPS, set up the terminal (`php -v` works).
2. Update to the new build and start it.
3. Check:
   - [ ] `%USERPROFILE%\.laramonster` exists with `moved-from.txt`; the old folder is gone
   - [ ] the dashboard shows the same sites, PHP versions and settings; no setup screen
   - [ ] the database is still there; an HTTPS site has a padlock without trusting again
   - [ ] `php -m` in a **new** terminal lists the extension (php.ini rewritten, PATH updated)
   - [ ] `laramonster status` in a terminal shows the running engine
   - [ ] no console windows appear while the dashboard is open for a few minutes
4. Fresh install (no previous data): setup works, files are created in `%USERPROFILE%\.laramonster`.
5. Locked file: start the previous release, kill only the app (leave `mysqld.exe` running), start
   the new build: it keeps working from the old folder and logs the failed move; after a reboot it
   moves.
6. HTTPS: click **Trust**: Windows' Security Warning appears (no UAC, no PowerShell); **No** shows
   "choose Yes"; **Yes** marks it trusted.

## Release notes (Windows)

> LaraMonster now keeps its files in `%USERPROFILE%\.laramonster`. Your sites, PHP versions,
> databases and HTTPS certificate move there automatically on the first start; nothing to do.
> If you go back to an older version, move that folder back to `%AppData%\laramonster`.
> Also fixed: console windows popping up, and the Trust button doing nothing on Windows.
