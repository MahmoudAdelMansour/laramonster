// Package proc holds small process helpers shared by the managers.
package proc

import (
	"os"
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
