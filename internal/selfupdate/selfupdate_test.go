package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSumsURLSitsNextToTheDownload(t *testing.T) {
	got := SumsURL("https://github.com/o/r/releases/download/v0.2.0/LaraMonster-macos-universal.zip")
	if got != "https://github.com/o/r/releases/download/v0.2.0/SHA256SUMS" {
		t.Fatal(got)
	}
}

func TestKindOf(t *testing.T) {
	if KindOf("") != None {
		t.Fatal("no path must not update")
	}
	if runtime.GOOS == "windows" && KindOf(`C:\Program Files\WindowsApps\Laramonster.LaraMonster_0.1.0.0_x64__8tm22b862nq2m\LaraMonsterApp.exe`) != Store {
		t.Fatal("Store copy must be left to the Store")
	}
	if runtime.GOOS == "darwin" && KindOf("/usr/local/bin/laramonster-app") != None {
		t.Fatal("a bare macOS binary can't be swapped as an app")
	}
	if runtime.GOOS != "windows" {
		if got := appBundle("/Applications/LaraMonster.app/Contents/MacOS/laramonster-app"); got != "/Applications/LaraMonster.app" {
			t.Fatal(got)
		}
	}
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestVerifyRefusesADamagedOrUnlistedFile(t *testing.T) {
	body := []byte("the real app")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sum(body) + "  LaraMonster-linux-amd64.tar.gz\n" + sum([]byte("x")) + "  SHA256SUMS.sig\n"))
	}))
	defer srv.Close()
	dir := t.TempDir()

	good := filepath.Join(dir, "LaraMonster-linux-amd64.tar.gz")
	os.WriteFile(good, body, 0o644)
	if err := verify(good, srv.URL); err != nil {
		t.Fatalf("good file refused: %v", err)
	}
	os.WriteFile(good, []byte("tampered"), 0o644)
	if err := verify(good, srv.URL); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("tampered file accepted: %v", err)
	}
	other := filepath.Join(dir, "evil.exe")
	os.WriteFile(other, body, 0o644)
	if err := verify(other, srv.URL); err == nil {
		t.Fatal("a file missing from SHA256SUMS was accepted")
	}
}

func writeTarball(t *testing.T, p string, files map[string]string) {
	t.Helper()
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	f.Close()
}

func TestInstallLinuxReplacesTheAppAndTheCLINextToIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix layout")
	}
	bin, work := t.TempDir(), t.TempDir()
	app, cli := filepath.Join(bin, "laramonster-app"), filepath.Join(bin, "laramonster")
	os.WriteFile(app, []byte("old app"), 0o755)
	os.WriteFile(cli, []byte("old cli"), 0o700)
	tgz := filepath.Join(work, "LaraMonster-linux-amd64.tar.gz")
	writeTarball(t, tgz, map[string]string{
		"LaraMonster/bin/laramonster-app":       "new app",
		"LaraMonster/bin/laramonster":           "new cli",
		"LaraMonster/scripts/install-linux.sh":  "not this",
		"LaraMonster/build/linux/laramonster-x": "nor this",
	})
	if err := installLinux(app, tgz, work); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{app: "new app", cli: "new cli"} {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Fatalf("%s = %q", p, b)
		}
	}
	if fi, _ := os.Stat(cli); fi.Mode().Perm() != 0o700 {
		t.Fatalf("permissions not kept: %v", fi.Mode())
	}
	if _, err := os.Stat(app + ".old"); err == nil {
		t.Fatal("old copy left behind")
	}
}

func TestInstallLinuxRefusesATarballWithoutTheApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix layout")
	}
	bin, work := t.TempDir(), t.TempDir()
	app := filepath.Join(bin, "laramonster-app")
	os.WriteFile(app, []byte("old app"), 0o755)
	tgz := filepath.Join(work, "x.tar.gz")
	writeTarball(t, tgz, map[string]string{"LaraMonster/README": "hi"})
	if err := installLinux(app, tgz, work); err == nil {
		t.Fatal("accepted an update without the app")
	}
	if b, _ := os.ReadFile(app); string(b) != "old app" {
		t.Fatal("the app was touched")
	}
}

func TestWaitForPreviousWaitsUntilTheOldVersionExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sleep")
	}
	cmd := exec.Command("sleep", "0.6")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait() // reap it, so it doesn't linger as a zombie
	start := time.Now()
	WaitForPrevious([]string{"--background", afterUpdate + strconv.Itoa(cmd.Process.Pid)})
	if d := time.Since(start); d < 400*time.Millisecond || d > 5*time.Second {
		t.Fatalf("waited %v", d)
	}
}

func TestStartOnlyInstallsFromTheReleasesPage(t *testing.T) {
	if KindOf(executable()) != Self {
		t.Skip("test binary isn't updatable here")
	}
	if err := Start("9.9.9", "https://evil.example/LaraMonster-linux-amd64.tar.gz"); err == nil {
		t.Fatal("accepted a download from another site")
	}
	if Status().Phase != "idle" {
		t.Fatal("refused update still started")
	}
}

// The whole flow on Linux: download from the releases page, check the sum,
// swap the app and the CLI, then start the new app with the old one's PID.
func TestUpdateEndToEnd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux layout")
	}
	bin, srvDir := t.TempDir(), t.TempDir()
	marker := filepath.Join(bin, "started-with")
	app, cli := filepath.Join(bin, "laramonster-app"), filepath.Join(bin, "laramonster")
	os.WriteFile(app, []byte("#!/bin/sh\necho old\n"), 0o755)
	os.WriteFile(cli, []byte("#!/bin/sh\necho old\n"), 0o755)
	newApp := "#!/bin/sh\necho \"$@\" > '" + marker + "'\n"
	tgz := filepath.Join(srvDir, "LaraMonster-linux-amd64.tar.gz")
	writeTarball(t, tgz, map[string]string{"LaraMonster/bin/laramonster-app": newApp, "LaraMonster/bin/laramonster": "#!/bin/sh\necho new\n"})
	body, _ := os.ReadFile(tgz)
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/v0.2.0/LaraMonster-linux-amd64.tar.gz", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/dl/v0.2.0/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sum(body) + "  LaraMonster-linux-amd64.tar.gz\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldSelf, oldReleases := self, Releases
	self, Releases = app, srv.URL+"/dl/"
	defer func() { self, Releases = oldSelf, oldReleases }()
	set(func(s *State) { *s = State{Phase: "idle"} })

	if err := Start("0.2.0", srv.URL+"/dl/v0.2.0/LaraMonster-linux-amd64.tar.gz"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for Status().Phase != "ready" && Status().Phase != "error" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := Status(); st.Phase != "ready" || st.Version != "0.2.0" || st.Progress != 100 {
		t.Fatalf("state %+v", st)
	}
	if b, _ := os.ReadFile(cli); !strings.Contains(string(b), "new") {
		t.Fatal("the CLI wasn't updated")
	}
	if left, _ := filepath.Glob(filepath.Join(bin, ".laramonster-update-*")); len(left) > 0 {
		t.Fatalf("download left behind: %v", left)
	}

	if err := Restart([]string{"--background", "laramonster://activate?key=x"}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for deadline := time.Now().Add(5 * time.Second); len(got) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		got, _ = os.ReadFile(marker)
	}
	want := afterUpdate + strconv.Itoa(os.Getpid()) + " --background"
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("new app started with %q, want %q", got, want)
	}
}
