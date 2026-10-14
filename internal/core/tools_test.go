//go:build !windows

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A new Mac has no ~/.zshrc: the terminal setup must create it, not succeed
// without changing anything.
func TestAddToPathCreatesTheShellProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("LARAMONSTER_HOME", filepath.Join(home, "lm"))

	if PathConfigured() {
		t.Fatal("configured before setup")
	}
	changed, err := AddToPath()
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || filepath.Base(changed[0]) != ".zshrc" {
		t.Fatalf("changed %v, want ~/.zshrc", changed)
	}
	if !PathConfigured() {
		t.Fatal("not configured after setup")
	}
	// Running it again adds nothing.
	if _, err := AddToPath(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
	if n := strings.Count(string(b), "# LaraMonster"); n != 1 {
		t.Fatalf("PATH line written %d times", n)
	}
}

// Existing profiles get the line too, and the user's shell's own profile is
// created: zsh never reads ~/.bashrc.
func TestAddToPathCoversExistingProfilesAndTheUsersShell(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("LARAMONSTER_HOME", filepath.Join(home, "lm"))
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddToPath(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	if !strings.Contains(string(b), "# mine") || !strings.Contains(string(b), "# LaraMonster") {
		t.Fatalf(".bashrc = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".zshrc")); !strings.Contains(string(b), "# LaraMonster") {
		t.Fatal("~/.zshrc not set up for a zsh user")
	}
	if _, err := os.Stat(filepath.Join(home, ".profile")); err == nil {
		t.Fatal("created a profile the user's shell doesn't read")
	}
}
