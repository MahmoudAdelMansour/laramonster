//go:build windows

package main

import (
	"os"
	"os/exec"
)

func runAttached(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func detach(cmd *exec.Cmd) error { return cmd.Start() }
