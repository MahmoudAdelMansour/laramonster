//go:build !darwin

package main

import "errors"

// Only macOS apps need moving into an Applications folder.
func offerMove([]string, bool) bool { return false }

func moveSelf([]string) error {
	return errors.New("only the macOS app moves itself into Applications")
}
