package core

import (
	"os"
	"path/filepath"
	"strings"

	"laramonster/internal/paths"
)

// AfterHomeMove fixes what still names the old home after paths.MoveHome:
// the extension_dir in each PHP version's php.ini, the php/composer shims
// and the bin directory on the user PATH.
func AfterHomeMove(old string) []error {
	var errs []error
	home := paths.Root()
	inis, _ := filepath.Glob(filepath.Join(paths.PHP(), "*", "php.ini"))
	for _, ini := range inis {
		b, err := os.ReadFile(ini)
		if err != nil || !strings.Contains(string(b), old) {
			continue
		}
		if err := os.WriteFile(ini, []byte(strings.ReplaceAll(string(b), old, home)), 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	if ShimsInstalled() {
		if _, err := InstallShims(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := replaceInUserPath(filepath.Join(old, "bin"), paths.Bin()); err != nil {
		errs = append(errs, err)
	}
	return errs
}
