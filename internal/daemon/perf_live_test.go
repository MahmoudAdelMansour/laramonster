package daemon

import (
	"os"
	"testing"

	"laramonster/internal/core"
	"laramonster/internal/sites"
)

// Read-only: LARAMONSTER_LIVE_PERF=/path/to/laravel go test ./internal/daemon/ -run LivePerf -v
func TestLivePerfCheckup(t *testing.T) {
	dir := os.Getenv("LARAMONSTER_LIVE_PERF")
	if dir == "" {
		t.Skip("set LARAMONSTER_LIVE_PERF to a Laravel project")
	}
	inst, err := core.PHPFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := perfCheckup(sites.Site{Name: "live", Path: dir}, inst)
	t.Logf("score %d · %v", c.Score, c.Facts)
	for _, k := range c.Checks {
		t.Logf("%-5s %-38s %s", k.Status, k.Title, k.Detail)
	}
}
