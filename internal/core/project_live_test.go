package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testReporter struct{ strings.Builder }

func (r *testReporter) Set(p float64, d string) { fmt.Fprintf(&r.Builder, "[%3.0f%%] %s\n", p*100, d) }

// TestCreateProjectLive creates a real Laravel project with Composer, as
// the wizard does. It needs the network and a LaraMonster home with PHP:
//
//	LARAMONSTER_HOME=/tmp/lm LARAMONSTER_LIVE_PROJECT=/tmp/projects go test ./internal/core/ -run Live -v
func TestCreateProjectLive(t *testing.T) {
	dir := os.Getenv("LARAMONSTER_LIVE_PROJECT")
	if dir == "" {
		t.Skip("set LARAMONSTER_LIVE_PROJECT to a folder to create projects in")
	}
	db := os.Getenv("LARAMONSTER_LIVE_DB")
	if db == "" {
		db = "mysql"
	}
	env := Env{
		Installed:    func(string) bool { return false },
		StartService: func(name string) (int, error) { return 0, errors.New("port 3306 is already in use") },
	}
	kit, pkgs := os.Getenv("LARAMONSTER_LIVE_KIT"), []string{"laravel/sanctum"}
	if kit == "" {
		kit = "laravel"
	}
	if v := os.Getenv("LARAMONSTER_LIVE_PACKAGES"); v != "" {
		pkgs = strings.Fields(v)
	}
	r := &testReporter{}
	target, warnings, err := CreateProject(ProjectOptions{
		Name: "live-demo", Dir: dir, Kit: kit, Database: db, Packages: pkgs,
	}, env, r)
	t.Log(r.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Errorf("warnings: %q", warnings)
	}
	if _, err := os.Stat(filepath.Join(target, "routes", "api.php")); err != nil {
		t.Error("install:api didn't run:", err)
	}
}
