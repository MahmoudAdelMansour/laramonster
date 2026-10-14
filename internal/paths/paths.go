// Package paths resolves where laramonster keeps its config, sockets, logs and data.
package paths

import (
	"os"
	"path/filepath"
)

// Root is laramonster's home directory. LARAMONSTER_HOME overrides the default
// (~/.config/laramonster on Linux, ~/Library/Application Support/laramonster on macOS,
// %AppData%\laramonster on Windows).
func Root() string {
	if v := os.Getenv("LARAMONSTER_HOME"); v != "" {
		return v
	}
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
