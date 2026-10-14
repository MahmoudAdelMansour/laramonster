package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"laramonster/internal/sites"
)

func TestRecommendations(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, envFile string, index bool) sites.Site {
		p := filepath.Join(dir, name)
		os.MkdirAll(p, 0o755)
		if index {
			os.WriteFile(filepath.Join(p, "index.php"), []byte("<?php"), 0o644)
		}
		if envFile != "" {
			os.WriteFile(filepath.Join(p, ".env"), []byte(envFile), 0o644)
		}
		return sites.Site{Name: name, Path: p, Root: p, PHP: "8.4", Enabled: true, Health: "up"}
	}
	st := Status{Sites: []sites.Site{
		mk("shop", "DB_CONNECTION=mysql\nCACHE_STORE=redis\n", true),
		mk("blog", "DB_CONNECTION=sqlite\n", true),
		mk("junk", "", false),
		func() sites.Site {
			s := mk("api", "", true)
			s.Proxy, s.Health = "http://127.0.0.1:3000", "down"
			return s
		}(),
	}}
	gb := uint64(1 << 30)
	rep := MonitorReport{
		Warm:   true,
		System: SystemStats{CPUs: 8, CPU: 30, MemTotal: 16 * gb, MemAvail: gb}, // 6% free
		Mem:    3 * gb,
		Groups: []Group{
			{ID: "svc-mysql", Kind: "service", Name: "MySQL / MariaDB", Ref: "mysql", Mem: 400 << 20, CPUAvg: 1},
			{ID: "svc-meilisearch", Kind: "service", Name: "Meilisearch", Ref: "meilisearch", Mem: 3 * gb, CPUAvg: 20}, // 1.6 cores
			{ID: "svc-redis", Kind: "service", Name: "Redis", Ref: "redis", Mem: 10 << 20},
		},
		Sites: []SiteLoad{
			{Name: "shop", CPU: 12, Requests: 40, URI: "/checkout"}, // ~1 core of 8
			{Name: "blog", CPU: 1, Running: 1, Longest: 95, URI: "/export"},
		},
	}
	recs := recommend(rep, st, map[string]*siteWin{}, time.Minute)
	got := map[string]Recommendation{}
	for _, r := range recs {
		got[r.ID] = r
	}
	for _, id := range []string{"low-memory", "busy-shop", "slow-blog", "busy-svc-meilisearch", "down", "no-index"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing recommendation %q; got %v", id, keys(got))
		}
	}
	// MySQL and Redis are used by shop; Meilisearch is unused but already flagged as busy.
	for _, id := range []string{"unused-mysql", "unused-redis", "unused-meilisearch"} {
		if _, ok := got[id]; ok {
			t.Errorf("unexpected %q", id)
		}
	}
	if !strings.Contains(got["no-index"].Title, "1 active folder has") || got["no-index"].Actions[0].Sites[0] != "junk" {
		t.Errorf("no-index: %+v", got["no-index"])
	}
	if a := got["low-memory"].Actions; len(a) != 1 || a[0].Service != "meilisearch" {
		t.Errorf("low memory should offer to stop the unused service: %+v", a)
	}

	// Calm machine, nothing unusual: no warnings.
	rep = MonitorReport{Warm: true, System: SystemStats{CPUs: 8, CPU: 5, MemTotal: 16 * gb, MemAvail: 10 * gb}}
	st = Status{Sites: []sites.Site{mk("shop", "DB_CONNECTION=mysql\n", true)}}
	if recs := recommend(rep, st, map[string]*siteWin{}, time.Minute); len(recs) != 0 {
		t.Errorf("expected no recommendations, got %+v", recs)
	}
}

func keys(m map[string]Recommendation) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
