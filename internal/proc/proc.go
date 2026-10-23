// Package proc holds small process helpers shared by the managers.
package proc

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// Terminate asks a child process to exit: SIGTERM on unix (MariaDB, Postgres
// and php-fpm all shut down cleanly on it), Kill on Windows.
func Terminate(p *os.Process) {
	if runtime.GOOS == "windows" {
		_ = p.Kill()
		return
	}
	_ = p.Signal(syscall.SIGTERM)
}

// Command is exec.Command for a program LaraMonster runs in the background:
// it never opens a console window. Use exec.Command only for programs the
// user should see, like a terminal.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	NoWindow(cmd)
	return cmd
}

// CommandContext is Command with a context.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	NoWindow(cmd)
	return cmd
}
