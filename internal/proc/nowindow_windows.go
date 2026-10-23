package proc

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// NoWindow keeps a console program from opening its own console window.
// LaraMonster's engine runs without a console, so Windows gives every
// console child (php, php-cgi, powershell, schtasks, databases…) a new
// window, or a Windows Terminal tab on Windows 11, unless told not to.
func NoWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
