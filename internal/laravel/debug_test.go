package laravel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceLinesStaysInProject(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.php"), []byte("one\ntwo\nthree\nfour\n"), 0o644)
	start, lines, err := SourceLines(dir, "a.php", 3, 1)
	if err != nil || start != 2 || len(lines) != 3 || lines[1] != "three" {
		t.Fatalf("got %d %q %v", start, lines, err)
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "sub/../../x"} {
		if _, _, err := SourceLines(dir, bad, 1, 1); err == nil {
			t.Errorf("%s: a file outside the project must be refused", bad)
		}
	}
}

func TestReadLog(t *testing.T) {
	f := filepath.Join(t.TempDir(), "laravel.log")
	os.WriteFile(f, []byte("[2026-10-16 10:00:00] local.INFO: Started {\"id\":1}\n"+
		"[2026-10-16 10:00:01] local.ERROR: Boom {\"exception\":\"[object] (RuntimeException(code: 0): Boom at /app/x.php:3)\n"+
		"[stacktrace]\n#0 {main}\n\"}\n"), 0o644)
	got, err := ReadLog(f, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].Level != "error" || got[0].Message != "Boom" || got[0].Detail == "" || got[1].Level != "info" || got[1].Message != "Started" {
		t.Errorf("parsed %+v", got)
	}
}
