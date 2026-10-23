//go:build !windows

package paths

// OnDisk is p: only the Windows Store version redirects files.
func OnDisk(p string) string { return p }
