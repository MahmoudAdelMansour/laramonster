package php

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A php-cgi.exe whose DLL is missing (as when the Visual C++ runtime isn't
// installed) must be reported as needing the runtime, quickly, instead of
// hanging on Windows' "DLL not found" dialog or vanishing from the list.
func TestFindBrokenMissingDLL(t *testing.T) {
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("needs gcc (MinGW) to build an exe with a missing DLL")
	}
	home := t.TempDir()
	t.Setenv("LARAMONSTER_HOME", home)
	dir := filepath.Join(home, "php", "8.4")
	os.MkdirAll(dir, 0o755)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "dep.c"), []byte("__declspec(dllexport) int dep(void) { return 7; }\n"), 0o644)
	os.WriteFile(filepath.Join(src, "main.c"), []byte("__declspec(dllimport) int dep(void);\nint main(void) { return dep() - 7; }\n"), 0o644)
	run := func(args ...string) {
		cmd := exec.Command(gcc, args...)
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gcc %v: %v\n%s", args, err, out)
		}
	}
	run("-shared", "-o", "vcruntime_lm_test.dll", "dep.c")
	run("-o", filepath.Join(dir, "php-cgi.exe"), "main.c", "vcruntime_lm_test.dll")
	// The DLL stays behind in src: next to the exe it would be found.

	start := time.Now()
	got := FindBroken(nil)
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("FindBroken took %s: blocked on the error dialog?", took)
	}
	if len(got) != 1 || got[0].Version != "8.4" || !got[0].VCRedist || !strings.Contains(got[0].Reason, "Visual C++") {
		t.Fatalf("FindBroken = %+v", got)
	}
}
