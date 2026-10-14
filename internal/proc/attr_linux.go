//go:build linux

package proc

import (
	"os/exec"
	"syscall"
)

// Bind makes cmd receive SIGTERM if LaraMonster dies (even by SIGKILL), so
// php-fpm, databases and other children are never left orphaned.
func Bind(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}
