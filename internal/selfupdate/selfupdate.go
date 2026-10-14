// Package selfupdate installs a newer LaraMonster over the running one: it
// downloads the release file for this system, checks it against the
// release's SHA256SUMS, and swaps the files in place. The new version runs
// after a restart (Restart starts it and the caller quits).
//
// Microsoft Store copies are updated by the Store, never here.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Kind says how this copy of LaraMonster gets updates.
type Kind string

const (
	Self  Kind = "self"  // replaced in place by this package
	Store Kind = "store" // the Microsoft Store updates it
	None  Kind = "none"  // a development build, or a layout we don't know
)

// Releases is where every update is downloaded from; nothing else is run.
var Releases = "https://github.com/MahmoudAdelMansour/laramonster-releases/releases/download/"

// StoreURL opens LaraMonster's page in the Microsoft Store app.
const StoreURL = "ms-windows-store://pdp/?productid=9NRLB8PVVP9X"

// State is what the dashboard shows while an update runs.
type State struct {
	Kind     Kind   `json:"kind"`
	Phase    string `json:"phase"` // idle, downloading, installing, ready, error
	Version  string `json:"version,omitempty"`
	Progress int    `json:"progress"` // 0-100 while downloading
	Error    string `json:"error,omitempty"`
}

var (
	mu    sync.Mutex
	state = State{Phase: "idle"}
)

// Status is the current state.
func Status() State {
	mu.Lock()
	defer mu.Unlock()
	s := state
	s.Kind = KindOf(executable())
	return s
}

func set(fn func(*State)) {
	mu.Lock()
	fn(&state)
	mu.Unlock()
}

// self is the app's path, read once at start: after the swap, Linux reports
// the replaced file as "… (deleted)" and Windows the renamed ".old" one.
var self = findExecutable()

func executable() string { return self }

func findExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe
}

// KindOf tells how the copy at exe (the running app) is updated.
func KindOf(exe string) Kind {
	switch {
	case exe == "":
		return None
	case runtime.GOOS == "windows" && strings.Contains(strings.ToLower(exe), `\windowsapps\`):
		return Store
	case runtime.GOOS == "darwin" && appBundle(exe) == "":
		return None // a bare binary, not LaraMonster.app
	}
	return Self
}

// appBundle is the .app folder around a macOS binary, or "".
func appBundle(exe string) string {
	dir := filepath.Dir(exe) // .../LaraMonster.app/Contents/MacOS
	if filepath.Base(dir) != "MacOS" || filepath.Base(filepath.Dir(dir)) != "Contents" {
		return ""
	}
	app := filepath.Dir(filepath.Dir(dir))
	if !strings.HasSuffix(app, ".app") {
		return ""
	}
	return app
}

// Start downloads and installs version from downloadURL in the background.
// It does nothing while an update is already running or installed.
func Start(version, downloadURL string) error {
	exe := executable()
	switch KindOf(exe) {
	case Store:
		return errors.New("the Microsoft Store updates this copy of LaraMonster")
	case None:
		return errors.New("this copy of LaraMonster can't update itself: download the new version instead")
	}
	if !strings.HasPrefix(downloadURL, Releases) {
		return errors.New("updates are only installed from LaraMonster's releases page: download the new version instead")
	}
	mu.Lock()
	if state.Phase == "downloading" || state.Phase == "installing" || (state.Phase == "ready" && state.Version == version) {
		mu.Unlock()
		return nil
	}
	state = State{Phase: "downloading", Version: version}
	mu.Unlock()

	go func() {
		err := run(exe, downloadURL)
		set(func(s *State) {
			if err != nil {
				s.Phase, s.Error = "error", err.Error()
				return
			}
			s.Phase, s.Progress = "ready", 100
		})
	}()
	return nil
}

func run(exe, downloadURL string) error {
	work, err := os.MkdirTemp(workDir(exe), ".laramonster-update-")
	if err != nil {
		return fmt.Errorf("couldn't prepare the update: %w", err)
	}
	defer os.RemoveAll(work)

	file := filepath.Join(work, path.Base(mustPath(downloadURL)))
	if err := download(downloadURL, file, func(p int) { set(func(s *State) { s.Progress = p }) }); err != nil {
		return err
	}
	if err := verify(file, SumsURL(downloadURL)); err != nil {
		return err
	}
	set(func(s *State) { s.Phase = "installing" })
	return install(exe, file, work)
}

// workDir keeps the download on the same disk as the app, so the final
// swap is a rename.
func workDir(exe string) string {
	dir := filepath.Dir(exe)
	if app := appBundle(exe); app != "" {
		dir = filepath.Dir(app)
	}
	if f, err := os.CreateTemp(dir, ".laramonster-probe-"); err == nil {
		f.Close()
		os.Remove(f.Name())
		return dir
	}
	return "" // the system temp dir
}

func mustPath(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	return p.Path
}

// SumsURL is the SHA256SUMS file published next to a release file.
func SumsURL(downloadURL string) string {
	i := strings.LastIndex(downloadURL, "/")
	if i < 0 {
		return ""
	}
	return downloadURL[:i+1] + "SHA256SUMS"
}

var client = &http.Client{Timeout: 30 * time.Minute}

func download(u, dest string, progress func(int)) error {
	res, err := client.Get(u)
	if err != nil {
		return fmt.Errorf("couldn't download the update: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("couldn't download the update: %s", res.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	w := &counter{total: res.ContentLength, progress: progress}
	if _, err := io.Copy(f, io.TeeReader(res.Body, w)); err != nil {
		return fmt.Errorf("couldn't download the update: %w", err)
	}
	return f.Close()
}

type counter struct {
	n, total int64
	last     int
	progress func(int)
}

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.total > 0 {
		if pct := int(c.n * 100 / c.total); pct != c.last {
			c.last = pct
			c.progress(pct)
		}
	}
	return len(p), nil
}

// verify checks a downloaded file against the release's SHA256SUMS. A file
// missing from the list, or a different hash, is refused.
func verify(file, sumsURL string) error {
	res, err := client.Get(sumsURL)
	if err != nil {
		return fmt.Errorf("couldn't check the download: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("couldn't check the download: %s", res.Status)
	}
	sums, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == filepath.Base(file) {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return fmt.Errorf("%s isn't in the release's checksums", filepath.Base(file))
	}
	got, err := hashFile(file)
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("the download is damaged (checksum mismatch): try again later")
	}
	return nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// replaceFile swaps dst for src. The running binary is renamed aside first
// (Windows can't overwrite a running .exe but can rename it); Cleanup
// removes the old copy at the next start.
func replaceFile(src, dst string) error {
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(dst); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(src, mode); err != nil {
		return err
	}
	old := dst + ".old"
	_ = os.Remove(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return permissionHint(err)
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(old, dst)
		return permissionHint(err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Remove(old) // unix keeps running the open file
	}
	return nil
}

func permissionHint(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("LaraMonster can't replace its own files here (%v): download the new version instead", err)
	}
	return err
}

// Cleanup removes what a previous update left behind (Windows keeps the old
// .exe until it stops running).
func Cleanup() {
	exe := executable()
	if exe == "" {
		return
	}
	_ = os.Remove(exe + ".old")
	if cli := filepath.Join(filepath.Dir(exe), cliName()); cli != exe {
		_ = os.Remove(cli + ".old")
	}
}

func cliName() string {
	if runtime.GOOS == "windows" {
		return "laramonster.exe"
	}
	return "laramonster"
}
