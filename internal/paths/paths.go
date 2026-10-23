// Package paths resolves where laramonster keeps its config, sockets, logs and data.
package paths

import (
	"os"
	"path/filepath"
)

// Root is laramonster's home directory. LARAMONSTER_HOME overrides the default
// (~/.config/laramonster on Linux, ~/Library/Application Support/laramonster on macOS,
// %USERPROFILE%\.laramonster on Windows, see home_windows.go).
func Root() string {
	if v := os.Getenv("LARAMONSTER_HOME"); v != "" {
		return v
	}
	return defaultRoot()
}

// configRoot is the home under the OS config directory: the default outside
// Windows, and where Windows homes used to live (see MoveHome).
func configRoot() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "laramonster")
}

func Config() string { return filepath.Join(Root(), "config.json") }
func Run() string    { return filepath.Join(Root(), "run") }
func Logs() string   { return filepath.Join(Root(), "logs") }
func Data() string   { return filepath.Join(Root(), "data") }
func PHP() string    { return filepath.Join(Root(), "php") }
func Bin() string    { return filepath.Join(Root(), "bin") }
func Socket() string { return filepath.Join(Run(), "laramonster.sock") }

// Ensure creates every directory laramonster writes to.
func Ensure() error {
	for _, d := range []string{Root(), Run(), Logs(), Data(), PHP(), Bin()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
