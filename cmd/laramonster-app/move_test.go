package main

import "testing"

func TestInApplications(t *testing.T) {
	home := "/Users/sam"
	for app, want := range map[string]bool{
		"/Applications/LaraMonster.app":            true,
		"/Applications/Dev/LaraMonster.app":        true,
		"/Users/sam/Applications/LaraMonster.app":  true,
		"/Users/sam/Downloads/LaraMonster.app":     false,
		"/Users/sam/Desktop/LaraMonster.app":       false,
		"/Applications":                            false,
		"/ApplicationsOld/LaraMonster.app":         false,
		"/Volumes/LaraMonster/LaraMonster.app":     false,
		"/private/var/folders/x/AppTranslocation/": false,
	} {
		if got := inApplications(app, home); got != want {
			t.Errorf("inApplications(%q) = %v, want %v", app, got, want)
		}
	}
}

func TestMoveChoice(t *testing.T) {
	for out, want := range map[string]string{
		"button returned:Move to Applications\n":         "Move to Applications",
		"button returned:Don't Ask Again, gave up:false": "Don't Ask Again",
		"": "",
	} {
		if got := moveChoice(out); got != want {
			t.Errorf("moveChoice(%q) = %q, want %q", out, got, want)
		}
	}
}
