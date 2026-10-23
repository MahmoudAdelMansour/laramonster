//go:build !windows

package paths

func defaultRoot() string { return configRoot() }

// MoveHome only moves homes on Windows.
func MoveHome() (string, error) { return "", nil }
