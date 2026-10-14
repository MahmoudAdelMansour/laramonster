//go:build !linux

package proc

import "os/exec"

// Bind is a no-op outside Linux (no parent-death signal there).
func Bind(cmd *exec.Cmd) {}
