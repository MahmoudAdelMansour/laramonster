//go:build !windows

package core

import "errors"

func userPath() (string, error) { return "", errors.New("only on Windows") }

func replaceInUserPath(string, string) error { return nil }
