package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInUse(t *testing.T) {
	dir := t.TempDir()
	if inUse(dir) {
		t.Fatal("an empty folder is not a home")
	}
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "php"), 0o755)
	if inUse(dir) {
		t.Fatal("empty folders a command made are not a home")
	}
	_ = os.MkdirAll(filepath.Join(dir, "php", "8.4"), 0o755)
	if !inUse(dir) {
		t.Fatal("a home with a PHP version is in use")
	}
}

func TestMoveSetsAsideUnusedTarget(t *testing.T) {
	base := t.TempDir()
	src, dst := filepath.Join(base, "old"), filepath.Join(base, "new")
	_ = os.MkdirAll(filepath.Join(src, "data", "pki"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "config.json"), []byte("{}"), 0o644)
	_ = os.MkdirAll(filepath.Join(dst, "logs"), 0o755)
	_ = os.WriteFile(filepath.Join(dst, "logs", "cli.log"), []byte("x"), 0o644)

	if err := move(src, dst); err != nil {
		t.Fatal(err)
	}
	if exists(src) || !exists(filepath.Join(dst, "config.json")) || !exists(filepath.Join(dst, "data", "pki")) {
		t.Fatal("the home wasn't moved whole")
	}
	b, _ := os.ReadFile(filepath.Join(dst, "moved-from.txt"))
	if strings.TrimSpace(string(b)) != src {
		t.Fatalf("moved-from.txt = %q", b)
	}
	aside, _ := filepath.Glob(dst + "-unused-*")
	if len(aside) != 1 || !exists(filepath.Join(aside[0], "logs", "cli.log")) {
		t.Fatal("the unused target should be set aside, not deleted")
	}
}

func TestMoveFailureKeepsOld(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "old")
	_ = os.MkdirAll(src, 0o755)
	if err := move(src, filepath.Join(base, "missing", "parent", "new")); err == nil {
		t.Fatal("expected an error")
	}
	if !exists(src) {
		t.Fatal("a failed move must leave the old home in place")
	}
}
