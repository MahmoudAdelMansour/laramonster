//go:build !windows

package autostart

import "syscall"

func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
