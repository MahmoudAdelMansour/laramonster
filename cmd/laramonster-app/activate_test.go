package main

import "testing"

func TestActivateURL(t *testing.T) {
	for args, want := range map[string]string{
		"laramonster://activate?key=LM-AAAA-BBBB-CCCC-DDDD&email=sara%40example.com": "/#account?email=sara%40example.com&key=LM-AAAA-BBBB-CCCC-DDDD",
		"laramonster://something-else": "/#account",
		"--background":                 "",
	} {
		if got := activateURL([]string{args}); got != want {
			t.Errorf("%q: got %q, want %q", args, got, want)
		}
	}
}
