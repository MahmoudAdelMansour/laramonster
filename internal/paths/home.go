package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// inUse reports whether dir holds a LaraMonster home, not just the empty
// folders a command creates on its way: a config, a certificate
// authority or a PHP version.
func inUse(dir string) bool {
	if exists(filepath.Join(dir, "config.json")) || exists(filepath.Join(dir, "data", "pki")) {
		return true
	}
	php, _ := os.ReadDir(filepath.Join(dir, "php"))
	return len(php) > 0
}

// move renames the home src to dst in one step: the OS refuses it while
// a file inside is open, so it moves whole or not at all. A dst that isn't
// in use (empty folders a command made) is set aside, never deleted.
func move(src, dst string) error {
	if exists(dst) {
		if err := os.Rename(dst, fmt.Sprintf("%s-unused-%d", dst, time.Now().Unix())); err != nil {
			return fmt.Errorf("couldn't make room for LaraMonster's files in %s (still using %s): %w", dst, src, err)
		}
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("couldn't move LaraMonster's files from %s to %s (still using the old folder, trying again next start): %w", src, dst, err)
	}
	_ = os.WriteFile(filepath.Join(dst, "moved-from.txt"), []byte(src+"\n"), 0o644)
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
