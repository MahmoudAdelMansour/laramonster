package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// install puts the downloaded release file in place of the running copy.
func install(exe, file, work string) error {
	switch runtime.GOOS {
	case "darwin":
		return installMac(exe, file, work)
	case "windows":
		// The download is the app itself.
		return replaceFile(file, exe)
	default:
		return installLinux(exe, file, work)
	}
}

// installMac swaps LaraMonster.app for the one in the release zip. The new
// bundle is downloaded by LaraMonster, not a browser, so macOS doesn't
// quarantine it and it opens without the "can't be opened" warning.
func installMac(exe, file, work string) error {
	app := AppBundle(exe)
	unz := filepath.Join(work, "unzipped")
	if out, err := exec.Command("ditto", "-x", "-k", file, unz).CombinedOutput(); err != nil {
		return fmt.Errorf("couldn't unpack the update: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fresh := filepath.Join(unz, "LaraMonster.app")
	if _, err := os.Stat(filepath.Join(fresh, "Contents", "MacOS", filepath.Base(exe))); err != nil {
		return errors.New("the update doesn't contain LaraMonster.app")
	}
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", fresh).Run()
	old := filepath.Join(work, "previous.app")
	if err := os.Rename(app, old); err != nil {
		return permissionHint(err)
	}
	if err := os.Rename(fresh, app); err != nil {
		_ = os.Rename(old, app)
		return permissionHint(err)
	}
	return nil
}

// installLinux replaces laramonster-app and, when it sits next to it, the
// laramonster command with the ones in the release tarball.
func installLinux(exe, file, work string) error {
	want := map[string]string{"laramonster-app": exe}
	if cli := filepath.Join(filepath.Dir(exe), "laramonster"); cli != exe {
		if _, err := os.Stat(cli); err == nil {
			want["laramonster"] = cli
		}
	}
	got, err := untar(file, work, want)
	if err != nil {
		return err
	}
	if _, ok := got["laramonster-app"]; !ok {
		return errors.New("the update doesn't contain laramonster-app")
	}
	for name, src := range got {
		if err := replaceFile(src, want[name]); err != nil {
			return err
		}
	}
	return nil
}

// untar extracts the bin/<name> entries listed in want into dir.
func untar(file, dir string, want map[string]string) (map[string]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("couldn't unpack the update: %w", err)
	}
	got := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return got, nil
		}
		if err != nil {
			return nil, fmt.Errorf("couldn't unpack the update: %w", err)
		}
		name := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || filepath.Base(filepath.Dir(h.Name)) != "bin" {
			continue
		}
		if _, ok := want[name]; !ok {
			continue
		}
		dst := filepath.Join(dir, name)
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(out, tr)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		got[name] = dst
	}
}

// afterUpdate is the flag the new version starts with: the process ID of the
// old one, which it waits for (single instance, engine ports) before running.
const afterUpdate = "--after-update="

// Restart starts the freshly installed version; the caller quits right after.
func Restart(args []string) error {
	exe := executable()
	pass := []string{afterUpdate + strconv.Itoa(os.Getpid())}
	for _, a := range args {
		if !strings.HasPrefix(a, afterUpdate) && !strings.HasPrefix(a, "laramonster://") {
			pass = append(pass, a)
		}
	}
	if app := AppBundle(exe); runtime.GOOS == "darwin" && app != "" {
		return openApp(app, pass)
	}
	cmd := exec.Command(exe, pass...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Launch opens the macOS app bundle at app (a moved copy of this one); it
// waits for this process to exit, like a restart after an update. The caller
// quits right after.
func Launch(app string, args []string) error {
	pass := []string{afterUpdate + strconv.Itoa(os.Getpid())}
	for _, a := range args {
		if !strings.HasPrefix(a, afterUpdate) {
			pass = append(pass, a)
		}
	}
	return openApp(app, pass)
}

func openApp(app string, args []string) error {
	cmd := exec.Command("open", append([]string{"-n", app, "--args"}, args...)...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Wait() // open returns once the app is launched
}

// WaitForPrevious blocks, at most 30 seconds, until the version that
// started this one after an update has exited.
func WaitForPrevious(args []string) {
	for _, a := range args {
		if pid, err := strconv.Atoi(strings.TrimPrefix(a, afterUpdate)); strings.HasPrefix(a, afterUpdate) && err == nil {
			for deadline := time.Now().Add(30 * time.Second); alive(pid) && time.Now().Before(deadline); {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
}
