package elevate

import (
	"errors"
	"testing"
)

func TestOsascriptError(t *testing.T) {
	base := errors.New("exit status 1")
	for _, c := range []struct{ stderr, want string }{
		{"0:84: execution error: User canceled. (-128)\n", "the password prompt was cancelled"},
		{"0:120: execution error: SecTrustSettingsSetTrustSettings: The authorization was denied since no user interaction was possible. (1)\n",
			"SecTrustSettingsSetTrustSettings: The authorization was denied since no user interaction was possible."},
		{"", "exit status 1"},
	} {
		if got := osascriptError(c.stderr, base).Error(); got != c.want {
			t.Errorf("osascriptError(%q) = %q, want %q", c.stderr, got, c.want)
		}
	}
}
