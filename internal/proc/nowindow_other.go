//go:build !windows

package proc

import "os/exec"

// NoWindow only matters on Windows (no console windows to hide elsewhere).
func NoWindow(*exec.Cmd) {}
