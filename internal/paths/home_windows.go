package paths

import (
	"os"
	"path/filepath"
	"sync/atomic"
)

// newRoot is the Windows home: %USERPROFILE%\.laramonster. The old one,
// %AppData%\laramonster, is redirected for the Microsoft Store version
// (into %LocalAppData%\Packages\…\LocalCache), so PowerShell, certutil and
// a `php` shim run from a terminal didn't see LaraMonster's files. The
// user profile itself isn't redirected: every program sees the same files.
func newRoot() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".laramonster")
}

// atNew is set once the new home is in use: it stays the home from then on.
var atNew atomic.Bool

// defaultRoot is the new home, or the old one until MoveHome has moved it,
// so nothing that runs before the engine starts sees an empty home.
func defaultRoot() string {
	h := newRoot()
	if h == "" {
		return configRoot()
	}
	if atNew.Load() {
		return h
	}
	if inUse(h) {
		atNew.Store(true)
		return h
	}
	if inUse(configRoot()) {
		return configRoot()
	}
	return h
}

// MoveHome moves an old %AppData% home to %USERPROFILE%\.laramonster and
// returns the old path ("" when there was nothing to move). It is a single
// rename: Windows refuses it while any file inside is open (a running
// engine, PHP, a database), so the home is moved whole or not at all, and
// LaraMonster keeps using the old one until the next start.
func MoveHome() (string, error) {
	if os.Getenv("LARAMONSTER_HOME") != "" {
		return "", nil
	}
	h, old := newRoot(), configRoot()
	if h == "" || inUse(h) || !inUse(old) {
		return "", nil
	}
	if err := move(OnDisk(old), h); err != nil { // the Store version's files are really in LocalCache
		return "", err
	}
	atNew.Store(true)
	return old, nil
}
