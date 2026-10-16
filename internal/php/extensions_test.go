package php

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtArgsAndIni(t *testing.T) {
	if got := strings.Join(ExtArgs(map[string]bool{"redis": true, "xdebug": true, "apcu": false}), " "); got != "-d extension=redis -d zend_extension=xdebug" {
		t.Errorf("ExtArgs = %q", got)
	}
	ini := filepath.Join(t.TempDir(), "php.ini")
	os.WriteFile(ini, []byte("memory_limit = 512M\nextension=curl\n;extension=gd\nextension=php_redis.dll\nextension=redis\n"), 0o644)
	if err := SetIniExtension(ini, "gd", true); err != nil {
		t.Fatal(err)
	}
	if err := SetIniExtension(ini, "redis", false); err != nil {
		t.Fatal(err)
	}
	if err := SetIniExtension(ini, "xdebug", true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ini)
	want := "memory_limit = 512M\nextension=curl\nextension=gd\n;extension=redis\nzend_extension=xdebug\n"
	if string(b) != want {
		t.Errorf("php.ini =\n%s\nwant\n%s", b, want)
	}
}

// LARAMONSTER_LIVE_SYSTEM_PHP=/usr/bin/php-fpm go test -run Live ./internal/php/
func TestLiveSystemExtensions(t *testing.T) {
	fpm := os.Getenv("LARAMONSTER_LIVE_SYSTEM_PHP")
	if fpm == "" {
		t.Skip("set LARAMONSTER_LIVE_SYSTEM_PHP to a php-fpm")
	}
	inst, ok := probe(fpm)
	if !ok {
		t.Fatal("probe failed")
	}
	v, err := Extensions(inst, map[string]bool{"redis": true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("kind=%s dir=%s", v.Kind, v.Dir)
	for _, e := range v.Exts {
		t.Logf("%-12s %-8s %-11s toggle=%v %s", e.Name, e.State, e.Source, e.Toggle, e.Why)
	}
	for _, c := range v.Catalog {
		t.Logf("catalog %-10s %-11s %s %s", c.Name, c.State, c.Pkg, c.Note)
	}
}
