//go:build !windows

package selfupdate

import (
	"os/exec"
	"syscall"
)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// detach starts the new version in its own session, so it outlives this one.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
