package proc

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Files that may call exec.Command directly: the program is never a
// Windows console helper (unix-only tools, the desktop app relaunching
// itself, the detached engine) or the user should see its window.
var rawExecAllowed = map[string]bool{
	"proc/proc.go":           true,
	"autostart/autostart.go": true, // the engine, detached with its own flags
	"core/ops.go":            true, // open/xdg-open and editors
	"daemon/api_ext.go":      true, // sh: unix only
	"daemon/api_pro.go":      true, // xdg-user-dir: Linux only
	"dns/setup.go":           true, // systemctl: Linux only
	"elevate/elevate.go":     true, // sh, sudo, osascript, pkexec: unix only
	"selfupdate/install.go":  true, // relaunching the app, macOS tools
}

// TestNoRawExec keeps console windows from popping up on Windows: a helper
// started with plain exec.Command from the console-less engine gets its own
// window. Use proc.Command, or add the file above with the reason.
func TestNoRawExec(t *testing.T) {
	raw := regexp.MustCompile(`\bexec\.Command(Context)?\(`)
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rawExecAllowed[filepath.ToSlash(rel)] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if raw.MatchString(line) {
				t.Errorf("%s:%d uses exec.Command: use proc.Command so Windows shows no console window", rel, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
