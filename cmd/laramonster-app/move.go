package main

import (
	"path/filepath"
	"strings"
)

// The "Move to Applications?" prompt on macOS. A Mac app opened where it was
// downloaded runs from a read-only copy (App Translocation), so it can't
// update itself: on launch LaraMonster offers to move itself into
// Applications and restart from there (move_darwin.go).

// noMovePrompt is the file, in LaraMonster's home, that "Don't Ask Again"
// leaves behind.
const noMovePrompt = ".no-move-prompt"

// inApplications reports whether the bundle at app is inside /Applications
// or ~/Applications (any depth, like /Applications/Dev/LaraMonster.app).
func inApplications(app, home string) bool {
	for _, dir := range applicationsDirs(home) {
		if rel, err := filepath.Rel(dir, app); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}

// applicationsDirs lists where LaraMonster moves itself, in order: the
// system folder, then the user's own when /Applications isn't writable.
func applicationsDirs(home string) []string {
	return []string{"/Applications", filepath.Join(home, "Applications")}
}

// moveChoice reads the button osascript reports ("button returned:…").
func moveChoice(out string) string {
	out = strings.TrimSpace(out)
	if i := strings.Index(out, "button returned:"); i >= 0 {
		out = out[i+len("button returned:"):]
		if j := strings.Index(out, ","); j >= 0 {
			out = out[:j]
		}
	}
	return out
}
