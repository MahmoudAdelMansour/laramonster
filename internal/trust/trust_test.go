package trust

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Safari and Chrome on macOS use the system keychain: without Firefox there
// is no browser store to fill, so "Trust" must not stay unfinished.
func TestBrowsersFollowTheSystemWithoutFirefoxOnMac(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	switch runtime.GOOS {
	case "darwin":
		if NeedsBrowserStores() || BrowserStatus() != "system" {
			t.Fatalf("no Firefox: needs=%v status=%s", NeedsBrowserStores(), BrowserStatus())
		}
		os.MkdirAll(filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles", "abc.default-release"), 0o755)
		if !NeedsBrowserStores() {
			t.Fatal("Firefox has its own store")
		}
	case "linux":
		if !NeedsBrowserStores() {
			t.Fatal("Chrome on Linux has its own store")
		}
	case "windows":
		if BrowserStatus() != "system" {
			t.Fatal("Windows browsers use the system store")
		}
	}
}
