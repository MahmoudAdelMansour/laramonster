//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// runAttached replaces this process with cmd so signals, TTY and exit codes
// behave exactly as if PHP had been run directly.
func runAttached(cmd *exec.Cmd) error {
	return syscall.Exec(cmd.Path, cmd.Args, cmd.Env)
}

// detach starts cmd in its own session so closing the terminal doesn't end it.
func detach(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
