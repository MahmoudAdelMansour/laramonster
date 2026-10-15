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
	os.WriteFile(filepath.Join(dir, "php8.dll"), []byte("present"), 0o644) // PHP's own DLL is there

	start := time.Now()
	got := FindBroken(nil)
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("FindBroken took %s: blocked on the error dialog?", took)
	}
	if len(got) != 1 || got[0].Version != "8.4" || !got[0].VCRedist || !strings.Contains(got[0].Reason, "Visual C++") {
		t.Fatalf("FindBroken = %+v", got)
	}
}

// The real thing: LaraMonster installs PHP from windows.php.net, Discover
// lists it (the "installed but doesn't appear" bug), and a PHP that can't
// start is explained instead of vanishing. Downloads ~30 MB:
//
//	LARAMONSTER_LIVE_PHP=8.4 go test ./internal/php/ -run LiveWindows -v
func TestLiveWindowsInstallDiscoverAndBroken(t *testing.T) {
	version := os.Getenv("LARAMONSTER_LIVE_PHP")
	if version == "" {
		t.Skip("set LARAMONSTER_LIVE_PHP to a PHP version")
	}
	t.Setenv("LARAMONSTER_HOME", t.TempDir())
	rel, err := InstallVersion(version, nil)
	if err != nil {
		t.Fatal(err)
	}
	installs := Discover()
	inst, ok := Find(installs, version)
	if !ok || !inst.Managed || inst.CLI == "" {
		t.Fatalf("PHP %s installed but Discover doesn't list it: %+v (FindBroken: %+v)", rel.Full, installs, FindBroken(installs))
	}
	for _, ext := range []string{"intl", "pdo_sqlite", "pdo_mysql", "mbstring", "openssl", "curl"} {
		if !inst.HasExtension(ext) {
			t.Errorf("PHP %s is missing %s", inst.Full, ext)
		}
	}
	if b := FindBroken(installs); len(b) != 0 {
		t.Errorf("a working PHP is reported broken: %+v", b)
	}

	// Break it the way an interrupted download would: PHP's own DLL gone.
	dlls, _ := filepath.Glob(filepath.Join(ManagedDir(version), "php*.dll"))
	for _, d := range dlls {
		if err := os.Remove(d); err != nil {
			t.Fatal(err)
		}
	}
	installs = Discover()
	if _, ok := Find(installs, version); ok {
		t.Fatal("a PHP without php8.dll is still listed as working")
	}
	b := FindBroken(installs)
	if len(b) != 1 || b[0].Version != Normalize(version) || b[0].VCRedist || !strings.Contains(b[0].Reason, "incomplete") {
		t.Fatalf("FindBroken = %+v", b)
	}
	t.Logf("broken PHP %s explained: %s", b[0].Version, b[0].Reason)
}
