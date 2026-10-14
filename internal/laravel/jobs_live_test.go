package laravel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"laramonster/internal/php"
)

// Run with LARAMONSTER_LIVE_JOBS=/srv/http to load the jobs of every Laravel
// project in that folder, as Jobs & Queues does.
func TestLiveJobs(t *testing.T) {
	root := os.Getenv("LARAMONSTER_LIVE_JOBS")
	if root == "" {
		t.Skip("set LARAMONSTER_LIVE_JOBS to a folder of Laravel projects")
	}
	installs := php.Discover()
	if len(installs) == 0 {
		t.Skip("no PHP found")
	}
	inst := installs[0]
	for _, i := range installs {
		if i.Version > inst.Version {
			inst = i
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "*", "artisan"))
	for _, a := range dirs {
		dir := filepath.Dir(a)
		if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
			continue
		}
		raw, err := Run(context.Background(), inst, dir, "jobs", nil, 60*time.Second)
		if err != nil {
			t.Logf("%-28s run: %v", filepath.Base(dir), err)
			continue
		}
		var res struct {
			Jobs []Job `json:"jobs"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Errorf("%-28s decode: %v", filepath.Base(dir), err)
			continue
		}
		t.Logf("%-28s %d jobs", filepath.Base(dir), len(res.Jobs))
	}
}
