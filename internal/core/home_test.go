package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAfterHomeMoveRewritesPHPIni(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LARAMONSTER_HOME", home)
	old := `C:\Users\fares\AppData\Roaming\laramonster`
	ini := filepath.Join(home, "php", "8.4", "php.ini")
	_ = os.MkdirAll(filepath.Dir(ini), 0o755)
	_ = os.WriteFile(ini, []byte(`extension_dir = "`+old+`\php\8.4\ext"`+"\n"), 0o644)

	if errs := AfterHomeMove(old); len(errs) > 0 {
		t.Fatal(errs)
	}
	b, _ := os.ReadFile(ini)
	if strings.Contains(string(b), old) || !strings.Contains(string(b), home) {
		t.Fatalf("php.ini still names the old home: %s", b)
	}
}
