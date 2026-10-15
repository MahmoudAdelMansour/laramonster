//go:build !windows

package php

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindBroken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LARAMONSTER_HOME", home)
	mk := func(v, script string) {
		dir := filepath.Join(home, "php", v)
		os.MkdirAll(dir, 0o755)
		if script != "" {
			os.WriteFile(filepath.Join(dir, "php-fpm"), []byte("#!/bin/sh\n"+script+"\n"), 0o755)
		}
	}
	mk("8.3", "")                                               // download interrupted
	mk("8.4", "echo 'cannot open shared object' >&2; exit 127") // won't start
	mk(".install-8.5-x", "")                                    // in progress: ignored

	got := FindBroken(nil)
	if len(got) != 2 {
		t.Fatalf("FindBroken = %+v", got)
	}
	if got[0].Version != "8.3" || !strings.Contains(got[0].Reason, "incomplete") {
		t.Errorf("8.3: %+v", got[0])
	}
	if got[1].Version != "8.4" || !strings.Contains(got[1].Reason, "cannot open shared object") {
		t.Errorf("8.4: %+v", got[1])
	}
}
