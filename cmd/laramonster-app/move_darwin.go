package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>
#include <dlfcn.h>
#include <limits.h>
#include <stdlib.h>

// SecTranslocateCreateOriginalPathForURL (Security.framework, macOS 10.12+)
// maps a translocated app back to where it really is, usually Downloads. It
// isn't in the public headers, so it's looked up at run time.
typedef CFURLRef (*lmOriginalFn)(CFURLRef, CFErrorRef *);

static char *lmOriginalPath(const char *path) {
	void *sec = dlopen("/System/Library/Frameworks/Security.framework/Security", RTLD_LAZY);
	if (!sec) return NULL;
	lmOriginalFn fn = (lmOriginalFn)dlsym(sec, "SecTranslocateCreateOriginalPathForURL");
	if (!fn) return NULL;
	char *res = NULL;
	@autoreleasepool {
		NSURL *in = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
		CFURLRef out = fn((CFURLRef)in, NULL);
		if (out) {
			char buf[PATH_MAX];
			if (CFURLGetFileSystemRepresentation(out, true, (UInt8 *)buf, sizeof buf)) res = strdup(buf);
			CFRelease(out);
		}
	}
	return res;
}

// lmTrash moves path to the Trash, so it can be put back from Finder.
static int lmTrash(const char *path) {
	@autoreleasepool {
		NSURL *u = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
		return [[NSFileManager defaultManager] trashItemAtURL:u resultingItemURL:nil error:nil] ? 0 : -1;
	}
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"laramonster/internal/paths"
	"laramonster/internal/selfupdate"
)

const (
	btnMove     = "Move to Applications"
	btnNotNow   = "Not Now"
	btnDontAsk  = "Don't Ask Again"
	moveFailMsg = "\n\nIn Finder, drag LaraMonster into your Applications folder and open it from there instead."
)

// offerMove asks, once per launch, to move LaraMonster into Applications
// when it runs from anywhere else. It returns true when the moved copy has
// been started: the caller quits.
func offerMove(args []string, background bool) bool {
	app := runningBundle()
	if app == "" || background || otherInstanceRunning() {
		return false
	}
	home, _ := os.UserHomeDir()
	if src := sourceBundle(app); src != "" && inApplications(src, home) {
		return false
	}
	if _, err := os.Stat(filepath.Join(paths.Root(), noMovePrompt)); err == nil {
		return false
	}
	msg := "LaraMonster works best from your Applications folder, where it can update itself.\n\nMove it there and restart?"
	if translocated(app) {
		msg = "macOS is running LaraMonster from a read-only copy because it was opened where it was downloaded, so it can't update itself.\n\nMove it to your Applications folder and restart?"
	}
	switch ask(app, msg, btnDontAsk, btnNotNow, btnMove) {
	case btnMove:
		if err := moveSelf(args); err != nil {
			ask(app, "LaraMonster couldn't move itself: "+err.Error()+moveFailMsg, "OK")
			return false
		}
		return true
	case btnDontAsk:
		_ = os.MkdirAll(paths.Root(), 0o755)
		_ = os.WriteFile(filepath.Join(paths.Root(), noMovePrompt), nil, 0o644)
	}
	return false
}

// moveSelf moves the running LaraMonster.app into Applications and starts
// the moved copy, which waits for this one to quit.
func moveSelf(args []string) error {
	app := runningBundle()
	if app == "" {
		return errors.New("this copy of LaraMonster isn't an app bundle")
	}
	src := sourceBundle(app)
	if src == "" {
		src = app // the read-only copy: it can still be copied
	}
	dst, err := moveToApplications(src)
	if err != nil {
		return err
	}
	return selfupdate.Launch(dst, args)
}

// moveToApplications puts src at Applications/LaraMonster.app and returns
// that path. An older copy there goes to the Trash, and so does src.
func moveToApplications(src string) (string, error) {
	home, _ := os.UserHomeDir()
	dir := ""
	for _, d := range applicationsDirs(home) {
		if os.MkdirAll(d, 0o755) == nil && writable(d) {
			dir = d
			break
		}
	}
	if dir == "" {
		return "", errors.New("no Applications folder is writable")
	}
	dst := filepath.Join(dir, "LaraMonster.app")
	if src == dst {
		return dst, nil
	}
	// Copy next to the destination first, so a failure leaves both copies as
	// they were and the final step is a rename.
	work, err := os.MkdirTemp(dir, ".laramonster-move-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	fresh := filepath.Join(work, "LaraMonster.app")
	if out, err := exec.Command("ditto", src, fresh).CombinedOutput(); err != nil {
		return "", fmt.Errorf("couldn't copy it to %s: %v: %s", dir, err, strings.TrimSpace(string(out)))
	}
	// Opening it already passed Gatekeeper (Open Anyway). Without the
	// quarantine flag the moved copy isn't translocated again.
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", fresh).Run()
	if _, err := os.Stat(dst); err == nil {
		if trash(dst) != nil {
			if err := os.RemoveAll(dst); err != nil {
				return "", fmt.Errorf("couldn't replace the LaraMonster already in %s: %w", dir, err)
			}
		}
	}
	if err := os.Rename(fresh, dst); err != nil {
		return "", err
	}
	if !translocated(src) {
		_ = trash(src) // best effort: a disk image or read-only folder keeps it
	}
	return dst, nil
}

// runningBundle is the LaraMonster.app this process runs from, or "".
func runningBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return selfupdate.AppBundle(exe)
}

func translocated(app string) bool { return strings.Contains(app, "/AppTranslocation/") }

// sourceBundle is where the app really is: app itself, or the original of a
// translocated copy ("" when macOS doesn't say).
func sourceBundle(app string) string {
	if !translocated(app) {
		return app
	}
	cp := C.CString(app)
	defer C.free(unsafe.Pointer(cp))
	out := C.lmOriginalPath(cp)
	if out == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoString(out)
}

func trash(p string) error {
	cp := C.CString(p)
	defer C.free(unsafe.Pointer(cp))
	if C.lmTrash(cp) != 0 {
		return fmt.Errorf("couldn't move %s to the Trash", p)
	}
	return nil
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".laramonster-probe-")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// otherInstanceRunning: another LaraMonster window is open (it gets this
// launch through SingleInstance), so this copy isn't the one in use.
func otherInstanceRunning() bool {
	out, _ := exec.Command("pgrep", "-x", "laramonster-app").Output()
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil && pid != os.Getpid() {
			return true
		}
	}
	return false
}

// ask shows a native dialog with LaraMonster's icon and returns the button
// clicked; the last button is the default. The texts go to osascript as
// arguments, so they need no AppleScript quoting.
func ask(app, msg string, buttons ...string) string {
	icon := "note"
	if _, err := os.Stat(filepath.Join(app, "Contents", "Resources", "icon.icns")); err == nil {
		icon = "(POSIX file (item 2 of argv))"
	}
	list := make([]string, len(buttons))
	for i := range buttons {
		list[i] = "item " + strconv.Itoa(i+3) + " of argv"
	}
	script := `on run argv
	activate
	set r to display dialog (item 1 of argv) with title "LaraMonster" buttons {` + strings.Join(list, ", ") + `} default button (item ` + strconv.Itoa(len(buttons)+2) + ` of argv) with icon ` + icon + `
	return button returned of r
end run`
	out, err := exec.Command("osascript", append([]string{"-e", script, msg, filepath.Join(app, "Contents", "Resources", "icon.icns")}, buttons...)...).Output()
	if err != nil {
		return "" // closed, or no window server (an SSH session)
	}
	return moveChoice(string(out))
}
