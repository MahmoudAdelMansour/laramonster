package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The .app is swapped for the one in the release zip, in place.
func TestInstallMacSwapsTheAppBundle(t *testing.T) {
	apps, work, build := t.TempDir(), t.TempDir(), t.TempDir()
	mk := func(root, body string) string {
		app := filepath.Join(root, "LaraMonster.app")
		macos := filepath.Join(app, "Contents", "MacOS")
		os.MkdirAll(macos, 0o755)
		os.WriteFile(filepath.Join(macos, "laramonster-app"), []byte(body), 0o755)
		os.WriteFile(filepath.Join(macos, "laramonster"), []byte(body), 0o755)
		return app
	}
	installed := mk(apps, "old")
	fresh := mk(build, "new")
	zip := filepath.Join(work, "LaraMonster-macos-universal.zip")
	if out, err := exec.Command("ditto", "-c", "-k", "--keepParent", fresh, zip).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	exe := filepath.Join(installed, "Contents", "MacOS", "laramonster-app")
	if err := installMac(exe, zip, work); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"laramonster-app", "laramonster"} {
		if b, _ := os.ReadFile(filepath.Join(installed, "Contents", "MacOS", f)); string(b) != "new" {
			t.Fatalf("%s = %q", f, b)
		}
	}
	if KindOf(exe) != Self {
		t.Fatal("an installed .app must update itself")
	}
}

func TestKindOfTranslocatedApp(t *testing.T) {
	exe := "/private/var/folders/mj/3blc6/T/AppTranslocation/3630-4FCD-83C5/d/LaraMonster.app/Contents/MacOS/laramonster-app"
	if got := KindOf(exe); got != Translocated {
		t.Errorf("KindOf(translocated) = %q", got)
	}
	if got := KindOf("/Applications/LaraMonster.app/Contents/MacOS/laramonster-app"); got != Self {
		t.Errorf("KindOf(/Applications) = %q", got)
	}
}
