package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Helper process: a copy of this test binary that keeps running.
func TestHelperKeepRunning(t *testing.T) {
	if os.Getenv("LM_HELPER") != "1" {
		t.Skip("helper")
	}
	time.Sleep(10 * time.Second)
}

// Windows can't overwrite a running .exe: it is renamed aside, replaced,
// and the old copy removed at the next start.
func TestReplaceFileWorksWhileTheExeRuns(t *testing.T) {
	dir := t.TempDir()
	self, _ := os.Executable()
	app := filepath.Join(dir, "LaraMonster.exe")
	b, _ := os.ReadFile(self)
	os.WriteFile(app, b, 0o755)
	cmd := exec.Command(app, "-test.run=TestHelperKeepRunning")
	cmd.Env = append(os.Environ(), "LM_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(300 * time.Millisecond)
	if !alive(cmd.Process.Pid) {
		t.Fatal("helper not running")
	}

	fresh := filepath.Join(dir, "new.exe")
	os.WriteFile(fresh, []byte("new version"), 0o755)
	if err := replaceFile(fresh, app); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(app); string(got) != "new version" {
		t.Fatal("not replaced")
	}
	if _, err := os.Stat(app + ".old"); err != nil {
		t.Fatal("the running copy should wait as .old")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if alive(cmd.Process.Pid) {
		t.Fatal("alive after exit")
	}
	if KindOf(`C:\Program Files\WindowsApps\Laramonster.LaraMonster_0.1.0.0_x64__8tm22b862nq2m\LaraMonsterApp.exe`) != Store {
		t.Fatal("Store copy must be left to the Store")
	}
}
