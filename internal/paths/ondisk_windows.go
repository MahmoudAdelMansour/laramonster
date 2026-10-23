package paths

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var getCurrentPackageFamilyName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentPackageFamilyName")

// packageFamily is the Microsoft Store package LaraMonster runs in, or ""
// for the plain .exe.
func packageFamily() string {
	if getCurrentPackageFamilyName.Find() != nil {
		return ""
	}
	n := uint32(256)
	buf := make([]uint16, n)
	if r, _, _ := getCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return "" // APPMODEL_ERROR_NO_PACKAGE: not packaged
	}
	return syscall.UTF16ToString(buf)
}

// OnDisk is where a file LaraMonster wrote really is, for programs outside
// LaraMonster (PowerShell, certutil, Explorer). The Store version's writes
// to %AppData% land in %LocalAppData%\Packages\<package>\LocalCache: inside
// LaraMonster both paths work, outside only this one does.
func OnDisk(p string) string {
	pfn := packageFamily()
	if pfn == "" {
		return p
	}
	cache := filepath.Join(os.Getenv("LOCALAPPDATA"), "Packages", pfn, "LocalCache")
	for env, dir := range map[string]string{"APPDATA": "Roaming", "LOCALAPPDATA": "Local"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		rel, err := filepath.Rel(base, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, `..\`) {
			continue
		}
		if real := filepath.Join(cache, dir, rel); exists(real) {
			return real
		}
	}
	return p
}
