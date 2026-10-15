package core

import (
	"testing"

	"laramonster/internal/config"
	"laramonster/internal/sites"
)

func TestMergedIniLayers(t *testing.T) {
	c := config.Default()
	c.PHPSettings = map[string]map[string]string{
		"global":    {"memory_limit": "256M", "date.timezone": "UTC"},
		"8.4":       {"memory_limit": "512M"},
		"site:shop": {"memory_limit": "1G", "upload_max_filesize": "64M"},
	}
	if got := MergedIni(c, "8.4", "shop"); got["memory_limit"] != "1G" || got["date.timezone"] != "UTC" || got["upload_max_filesize"] != "64M" {
		t.Errorf("site: %v", got)
	}
	if got := MergedIni(c, "8.4", ""); got["memory_limit"] != "512M" || got["upload_max_filesize"] != "" {
		t.Errorf("version: %v", got)
	}
	if got := MergedIni(c, "8.3", "blog"); got["memory_limit"] != "256M" {
		t.Errorf("other version, plain site: %v", got)
	}

	ini, own := PoolIni(c, []sites.Site{{Name: "shop", PHP: "8.4"}, {Name: "blog", PHP: "8.4"}, {Name: "old", PHP: "8.3"}})
	if own["shop"] != "8.4@shop" || len(own) != 1 {
		t.Errorf("own pools: %v", own)
	}
	if ini["8.4@shop"]["memory_limit"] != "1G" || ini["8.4"]["memory_limit"] != "512M" || ini["8.3"]["memory_limit"] != "256M" {
		t.Errorf("pool settings: %v", ini)
	}
}

func TestCheckIni(t *testing.T) {
	for _, ok := range [][2]string{{"memory_limit", "1G"}, {"opcache.enable", "0"}, {"xdebug.mode", "debug,develop"}, {"error_reporting", "E_ALL & ~E_DEPRECATED"}} {
		if err := checkIni(ok[0], ok[1]); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range [][2]string{{"memory limit", "1G"}, {"extension", "foo.so"}, {"auto_prepend_file", "/x"}, {"memory_limit", "1G\nextension=evil.so"}, {"", "x"}} {
		if err := checkIni(bad[0], bad[1]); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
