package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/laravel"
	"laramonster/internal/php"
	"laramonster/internal/proc"
	"laramonster/internal/sites"
	"laramonster/internal/webhook"
)

// PerfCheck is one line of the Performance checkup.
type PerfCheck struct {
	ID     string   `json:"id"`
	Group  string   `json:"group"`
	Title  string   `json:"title"`
	Status string   `json:"status"` // good, warn, bad, info
	Detail string   `json:"detail"` // what we found
	Why    string   `json:"why"`    // why it matters
	Items  []string `json:"items,omitempty"`
	Fix    *PerfFix `json:"fix,omitempty"`
	Undo   *PerfFix `json:"undo,omitempty"`
}

// PerfFix is a one-click change. Action names one of perfActions: nothing
// from the client is ever run as a command.
type PerfFix struct {
	Label  string `json:"label"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

type PerfCheckup struct {
	Score  int         `json:"score"` // 0..100
	Facts  []string    `json:"facts"`
	Checks []PerfCheck `json:"checks"`
}

// perfActions are the changes Performance can make in a project.
var perfActions = map[string]struct {
	title string
	run   func(ctx context.Context, s sites.Site, inst php.Install, j *core.Job) error
}{
	"optimize":          {"Cache config, routes, views and events", artisanAction("optimize")},
	"optimize-clear":    {"Clear Laravel's caches", artisanAction("optimize:clear")},
	"composer-optimize": {"Optimize the Composer autoloader", composerAction("dump-autoload", "--optimize")},
	"composer-default":  {"Restore the default Composer autoloader", composerAction("dump-autoload")},
	"debugbar-off":      {"Turn Debugbar off", envAction(map[string]string{"DEBUGBAR_ENABLED": "false"})},
	"debugbar-on":       {"Turn Debugbar on", envAction(map[string]string{"DEBUGBAR_ENABLED": "true"})},
	"telescope-off":     {"Turn Telescope off", envAction(map[string]string{"TELESCOPE_ENABLED": "false"})},
	"telescope-on":      {"Turn Telescope on", envAction(map[string]string{"TELESCOPE_ENABLED": "true"})},
	"queue-database":    {"Run queued jobs in the background", envAction(map[string]string{"QUEUE_CONNECTION": "database"})},
}

func artisanAction(cmd string) func(context.Context, sites.Site, php.Install, *core.Job) error {
	return func(ctx context.Context, s sites.Site, inst php.Install, j *core.Job) error {
		fmt.Fprintln(j, "php artisan "+cmd)
		out, code, err := laravel.Artisan(ctx, inst, s.Path, []string{cmd}, 5*time.Minute)
		fmt.Fprint(j, out)
		if err == nil && code != 0 {
			err = fmt.Errorf("php artisan %s failed (exit %d): see the details", cmd, code)
		}
		return err
	}
}

func composerAction(args ...string) func(context.Context, sites.Site, php.Install, *core.Job) error {
	return func(ctx context.Context, s sites.Site, _ php.Install, j *core.Job) error {
		fmt.Fprintln(j, "composer "+strings.Join(args, " "))
		cmd, err := core.ComposerCommand(s.Path, args...)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = j, j
		return cmd.Run()
	}
}

func envAction(vars map[string]string) func(context.Context, sites.Site, php.Install, *core.Job) error {
	return func(ctx context.Context, s sites.Site, inst php.Install, j *core.Job) error {
		if err := core.SetEnvVars(s.Path, vars); err != nil {
			return err
		}
		for k, v := range vars {
			fmt.Fprintf(j, "%s=%s in .env\n", k, v)
		}
		// A cached config wouldn't see the change.
		if fileExists(filepath.Join(s.Path, "bootstrap", "cache", "config.php")) {
			out, _, _ := laravel.Artisan(ctx, inst, s.Path, []string{"config:cache"}, time.Minute)
			fmt.Fprint(j, out)
		}
		return nil
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// perfCheckup reads a project's files and settings: no artisan run, so it
// is fast and works even when the app fails to boot.
func perfCheckup(s sites.Site, inst php.Install) PerfCheckup {
	dir := s.Path
	env := core.EnvValues(dir)
	lock := composerLock(dir)
	var checks []PerfCheck
	add := func(c PerfCheck) { checks = append(checks, c) }
	cache := filepath.Join(dir, "bootstrap", "cache")

	// --- Laravel's own caches ---
	type part struct {
		name, file string
		on         bool
	}
	parts := []part{
		{"Config", "config.php", fileExists(filepath.Join(cache, "config.php"))},
		{"Routes", "routes-v7.php", fileExists(filepath.Join(cache, "routes-v7.php"))},
		{"Events", "events.php", fileExists(filepath.Join(cache, "events.php"))},
	}
	blades, compiled := countFiles(filepath.Join(dir, "resources", "views"), ".blade.php"), countFiles(filepath.Join(dir, "storage", "framework", "views"), ".php")
	parts = append(parts, part{"Views", "", blades == 0 || compiled >= blades})
	on := 0
	var items []string
	for _, p := range parts {
		state := "off"
		if p.on {
			state = "on"
			on++
		}
		items = append(items, p.name+": "+state)
	}
	c := PerfCheck{ID: "caches", Group: "Laravel", Title: "Config, route, view and event caches", Items: items,
		Why: "Laravel reads every config file, registers every route and compiles Blade views on each request unless they're cached. Caching usually saves 10–50 ms per request; production should always run php artisan optimize."}
	switch on {
	case len(parts):
		c.Status, c.Detail = "good", "All four caches are on, like production."
		c.Undo = &PerfFix{Label: "Clear caches", Action: "optimize-clear", Note: "With the config cached, .env changes only apply after caching again. Clear the caches while you edit config."}
	case 0:
		c.Status, c.Detail = "warn", "None are cached: Laravel rebuilds them on every request. That's normal while developing; cache them to measure speed like production."
		c.Fix = &PerfFix{Label: "Cache them (optimize)", Action: "optimize", Note: "Runs php artisan optimize. While the config is cached, .env edits only apply after caching again."}
	default:
		c.Status, c.Detail = "warn", fmt.Sprintf("%d of %d are cached.", on, len(parts))
		c.Fix = &PerfFix{Label: "Cache all (optimize)", Action: "optimize", Note: "Runs php artisan optimize."}
		c.Undo = &PerfFix{Label: "Clear caches", Action: "optimize-clear"}
	}
	add(c)

	// --- Composer autoloader ---
	if optimizedAutoloader(dir) {
		add(PerfCheck{ID: "autoload", Group: "PHP", Title: "Composer autoloader", Status: "good", Detail: "Optimized: classes load from a class map.",
			Why:  "An optimized autoloader finds each class in one lookup instead of checking the file system.",
			Undo: &PerfFix{Label: "Restore default", Action: "composer-default", Note: "The default autoloader picks up new classes without regenerating."}})
	} else {
		add(PerfCheck{ID: "autoload", Group: "PHP", Title: "Composer autoloader", Status: "warn", Detail: "Not optimized: each class is found by checking folders.",
			Why: "composer dump-autoload --optimize builds a class map, so PHP finds classes in one lookup. Production installs should use it.",
			Fix: &PerfFix{Label: "Optimize autoloader", Action: "composer-optimize", Note: "New classes you add need composer dump-autoload again while it's optimized."}})
	}

	// --- PHP itself ---
	ini, iniErr := core.ProjectIni(inst, dir, []string{"opcache.enable"})
	if iniErr != nil {
		add(PerfCheck{ID: "opcache", Group: "PHP", Title: "OPcache", Status: "info", Detail: "Couldn't read PHP's settings: " + iniErr.Error(),
			Why: "Without OPcache PHP compiles every file on every request, often doubling response times."})
	} else if isOn(ini["opcache.enable"]) {
		add(PerfCheck{ID: "opcache", Group: "PHP", Title: "OPcache", Status: "good", Detail: "On: compiled PHP is kept in memory between requests.",
			Why: "Without OPcache PHP compiles every file on every request, often doubling response times."})
	} else {
		add(PerfCheck{ID: "opcache", Group: "PHP", Title: "OPcache", Status: "bad", Detail: "Off for this site.",
			Why: "Without OPcache PHP compiles every file on every request, often doubling response times. Turn it on in this site's PHP settings (OPcache)."})
	}
	if inst.HasExtension("xdebug") {
		add(PerfCheck{ID: "xdebug", Group: "PHP", Title: "Xdebug", Status: "warn", Detail: "Loaded in PHP " + inst.Full + ".",
			Why: "Xdebug slows every request, often by several times, even when you aren't debugging. Set xdebug.mode=off in this site's PHP settings when you don't need it."})
	}

	// --- packages that cost time on every request ---
	debug := isTrue(env["APP_DEBUG"])
	if _, ok := lock["barryvdh/laravel-debugbar"]; ok {
		enabled := env["DEBUGBAR_ENABLED"]
		active := isTrue(enabled) || enabled == "" && debug
		c := PerfCheck{ID: "debugbar", Group: "Packages", Title: "Laravel Debugbar",
			Why: "Debugbar collects queries, views and timing on every request, which often adds 50–200 ms and memory. Turn it off while measuring speed."}
		if active {
			c.Status, c.Detail = "warn", "Active on every request."
			c.Fix = &PerfFix{Label: "Turn off", Action: "debugbar-off", Note: "Sets DEBUGBAR_ENABLED=false in .env."}
		} else {
			c.Status, c.Detail = "good", "Installed but off."
			c.Undo = &PerfFix{Label: "Turn on", Action: "debugbar-on"}
		}
		add(c)
	}
	if _, ok := lock["laravel/telescope"]; ok {
		active := !strings.EqualFold(env["TELESCOPE_ENABLED"], "false")
		c := PerfCheck{ID: "telescope", Group: "Packages", Title: "Laravel Telescope",
			Why: "Telescope writes every request, query and job to the database, which adds time and grows the database. Keep it off unless you're using it."}
		if active {
			c.Status, c.Detail = "warn", "Recording every request."
			c.Fix = &PerfFix{Label: "Turn off", Action: "telescope-off", Note: "Sets TELESCOPE_ENABLED=false in .env."}
		} else {
			c.Status, c.Detail = "good", "Installed but off."
			c.Undo = &PerfFix{Label: "Turn on", Action: "telescope-on"}
		}
		add(c)
	}

	// --- drivers ---
	queue := firstNonEmpty(env["QUEUE_CONNECTION"], env["QUEUE_DRIVER"], "sync")
	if queue == "sync" {
		add(PerfCheck{ID: "queue", Group: "Drivers", Title: "Queue", Status: "warn", Detail: "sync: queued jobs run inside the request.",
			Why: "With the sync queue, every mail, notification and job runs before the page is sent, so the visitor waits for all of it. Use the database or Redis queue and run a worker (Jobs & Queues → Queues & workers).",
			Fix: &PerfFix{Label: "Use the database queue", Action: "queue-database", Note: "Sets QUEUE_CONNECTION=database. Start a worker in Jobs & Queues so jobs run."}})
	} else {
		add(PerfCheck{ID: "queue", Group: "Drivers", Title: "Queue", Status: "good", Detail: queue + ": jobs run in the background.",
			Why: "Jobs run in a worker, so pages don't wait for them. Keep a worker running (Jobs & Queues)."})
	}
	cacheStore := firstNonEmpty(env["CACHE_STORE"], env["CACHE_DRIVER"], "database")
	session := firstNonEmpty(env["SESSION_DRIVER"], "database")
	slowStores := map[string]bool{"database": true, "file": true}
	c = PerfCheck{ID: "stores", Group: "Drivers", Title: "Cache and sessions", Items: []string{"Cache: " + cacheStore, "Sessions: " + session},
		Why: "Database and file stores are fine to start. Under load Redis is faster and takes work off the database. The Services page runs Redis for you."}
	if slowStores[cacheStore] || slowStores[session] {
		c.Status, c.Detail = "info", "Using the "+map[bool]string{true: cacheStore, false: session}[slowStores[cacheStore]]+" store."
	} else {
		c.Status, c.Detail = "good", "Using fast stores."
	}
	add(c)

	// --- N+1 safety net ---
	if !preventsLazyLoading(dir) {
		add(PerfCheck{ID: "lazy", Group: "Laravel", Title: "Lazy-loading protection", Status: "info", Detail: "Not turned on.",
			Why: "Model::preventLazyLoading(! app()->isProduction()) in AppServiceProvider::boot() makes every N+1 query throw while you develop, so you see it right away. The Requests tab finds them too."})
	} else {
		add(PerfCheck{ID: "lazy", Group: "Laravel", Title: "Lazy-loading protection", Status: "good", Detail: "On: N+1 queries throw while you develop.",
			Why: "Lazy loading a relation outside production raises an error, so N+1 queries can't slip in."})
	}
	if debug {
		add(PerfCheck{ID: "debug", Group: "Laravel", Title: "Debug mode", Status: "info", Detail: "APP_DEBUG=true.",
			Why: "Expected while developing. Debug mode adds some work to each request; production must use APP_DEBUG=false (it also hides error details from visitors)."})
	}
	if lvl := strings.ToLower(env["LOG_LEVEL"]); lvl == "debug" || lvl == "" {
		add(PerfCheck{ID: "log", Group: "Laravel", Title: "Log level", Status: "info", Detail: "debug: everything is written to the log.",
			Why: "Fine while developing. In production LOG_LEVEL=warning or error writes less on each request."})
	}

	score, weight := 0, 0
	for _, c := range checks {
		w := map[string]int{"good": 1, "warn": 1, "bad": 2, "info": 0}[c.Status]
		weight += w
		if c.Status == "good" {
			score += w
		}
	}
	pct := 100
	if weight > 0 {
		pct = score * 100 / weight
	}
	facts := []string{"PHP " + inst.Full}
	if v := lock["laravel/framework"]; v != "" {
		facts = append([]string{"Laravel " + strings.TrimPrefix(v, "v")}, facts...)
	}
	facts = append(facts, "APP_ENV="+firstNonEmpty(env["APP_ENV"], "production"))
	return PerfCheckup{Score: pct, Facts: facts, Checks: checks}
}

func isOn(v string) bool {
	return v == "1" || strings.EqualFold(v, "on") || strings.EqualFold(v, "true")
}
func isTrue(v string) bool {
	return strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "(true)")
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func countFiles(dir, suffix string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, suffix) {
			n++
		}
		return nil
	})
	return n
}

// composerLock maps installed package names to versions.
func composerLock(dir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(dir, "composer.lock"))
	if err != nil {
		return out
	}
	var lock struct {
		Packages    []struct{ Name, Version string } `json:"packages"`
		PackagesDev []struct{ Name, Version string } `json:"packages-dev"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return out
	}
	for _, p := range append(lock.Packages, lock.PackagesDev...) {
		out[p.Name] = p.Version
	}
	return out
}

// optimizedAutoloader: an optimized class map lists the app's own classes.
func optimizedAutoloader(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "vendor", "composer", "autoload_classmap.php"))
	return err == nil && strings.Contains(string(b), `'App\\`)
}

var lazyGuard = regexp.MustCompile(`preventLazyLoading|shouldBeStrict`)

func preventsLazyLoading(dir string) bool {
	found := false
	_ = filepath.WalkDir(filepath.Join(dir, "app", "Providers"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".php") || found {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && lazyGuard.Match(b) {
			found = true
		}
		return nil
	})
	return found
}

// --- EXPLAIN a recorded query ---

const explainPHP = `<?php
error_reporting(E_ALL & ~E_DEPRECATED);
$in = json_decode(getenv('LARAMONSTER_EXPLAIN'), true);
require 'vendor/autoload.php';
$app = require 'bootstrap/app.php';
$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
$db = Illuminate\Support\Facades\DB::connection($in['conn'] ?: null);
$driver = $db->getDriverName();
$explain = $driver === 'sqlite' ? 'EXPLAIN QUERY PLAN ' : 'EXPLAIN ';
$out = ['driver' => $driver];
$db->beginTransaction();
try {
    $out['plan'] = array_map(fn ($r) => (array) $r, $db->select($explain.$in['sql'], $in['bindings']));
    $times = [];
    $rows = 0;
    for ($i = 0; $i < 5; $i++) {
        $t = microtime(true);
        $rows = count($db->select($in['sql'], $in['bindings']));
        $times[] = (microtime(true) - $t) * 1000;
    }
    sort($times);
    $out['ms'] = round($times[2], 2);
    $out['min_ms'] = round($times[0], 2);
    $out['rows'] = $rows;
} finally {
    $db->rollBack();
}
echo json_encode($out);
`

// ExplainResult is a query's plan and timing.
type ExplainResult struct {
	Driver   string           `json:"driver"`
	Plan     []map[string]any `json:"plan"`
	MS       float64          `json:"ms"`     // median of 5 runs
	MinMS    float64          `json:"min_ms"` //
	Rows     int              `json:"rows"`
	FullScan []string         `json:"full_scan"` // tables read without an index
}

var (
	selectOnly = regexp.MustCompile(`(?is)^\s*(select|with)\b`)
	writes     = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|create|replace|grant|lock)\b`)
)

func explainQuery(ctx context.Context, s sites.Site, inst php.Install, sql string, bindings []any, conn string) (ExplainResult, error) {
	var res ExplainResult
	if !selectOnly.MatchString(sql) || writes.MatchString(sql) {
		return res, errors.New("only SELECT queries can be explained and timed here")
	}
	if strings.Contains(sql, "…") {
		return res, errors.New("this query used a list of values (IN …) that isn't recorded: copy it from your code to test it")
	}
	if strings.Count(sql, "?") != len(bindings) {
		return res, errors.New("this query's values weren't all recorded, so it can't be run again")
	}
	in, _ := json.Marshal(map[string]any{"sql": sql, "bindings": bindings, "conn": conn})
	script := filepath.Join(os.TempDir(), fmt.Sprintf("laramonster-explain-%d.php", os.Getpid()))
	if err := os.WriteFile(script, []byte(explainPHP), 0o600); err != nil {
		return res, err
	}
	defer os.Remove(script)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, inst.CLI, script)
	cmd.Dir = s.Path
	cmd.Env = append(append(os.Environ(), inst.Env()...), "LARAMONSTER_EXPLAIN="+string(in))
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return res, fmt.Errorf("the query couldn't run: %s", firstNonEmpty(msg, err.Error()))
	}
	if i := strings.LastIndex(string(out), "{\"driver\""); i > 0 {
		out = out[i:] // anything the app printed while booting
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return res, fmt.Errorf("unexpected output: %.300s", out)
	}
	res.FullScan = fullScans(res.Driver, res.Plan)
	return res, nil
}

var sqliteScan = regexp.MustCompile(`^SCAN (\w+)`)

// fullScans lists tables the plan reads row by row.
func fullScans(driver string, plan []map[string]any) []string {
	var out []string
	for _, row := range plan {
		switch driver {
		case "sqlite":
			d, _ := row["detail"].(string)
			if m := sqliteScan.FindStringSubmatch(d); m != nil && !strings.Contains(d, "USING") {
				out = append(out, m[1])
			}
		case "mysql", "mariadb":
			if t, _ := row["type"].(string); strings.EqualFold(t, "ALL") {
				if tbl, _ := row["table"].(string); tbl != "" {
					out = append(out, tbl)
				}
			}
		case "pgsql":
			for _, v := range row {
				if line, ok := v.(string); ok && strings.Contains(line, "Seq Scan on ") {
					f := strings.Fields(line[strings.Index(line, "Seq Scan on ")+len("Seq Scan on "):])
					if len(f) > 0 {
						out = append(out, f[0])
					}
				}
			}
		}
	}
	return out
}

// registerPerf adds Performance (Pro): checkup, fixes, recorded requests,
// EXPLAIN and load tests.
func (rt *Runtime) registerPerf(pro func(string, apiFunc)) {
	pro("GET /api/pro/perf/checkup", func(r *http.Request) (any, error) {
		s, inst, err := laravelSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		return perfCheckup(s, inst), nil
	})
	pro("POST /api/pro/perf/fix", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, inst, err := laravelSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		a, ok := perfActions[b.str("action")]
		if !ok {
			return nil, fmt.Errorf("unknown change %q", b.str("action"))
		}
		title := a.title + " (" + s.Name + ")"
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			j.Set(-1, a.title)
			return a.run(context.Background(), s, inst, j)
		}, nil), nil
	})
	pro("GET /api/pro/perf/requests", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		traces, err := laravel.ReadTraces(laravel.TraceFile(s.Path), 1000)
		if err != nil {
			return nil, err
		}
		cfg, _ := config.Load()
		return map[string]any{
			"tracking": cfg.Tracking[s.Name],
			"report":   laravel.AnalyzePerf(traces, laravel.PackageResolver(s.Path)),
		}, nil
	})
	pro("POST /api/pro/perf/explain", func(r *http.Request) (any, error) {
		var b struct {
			Site     string `json:"site"`
			SQL      string `json:"sql"`
			Bindings []any  `json:"bindings"`
			Conn     string `json:"conn"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
			return nil, err
		}
		s, inst, err := laravelSite(b.Site)
		if err != nil {
			return nil, err
		}
		if b.Bindings == nil {
			b.Bindings = []any{}
		}
		return explainQuery(r.Context(), s, inst, b.SQL, b.Bindings, b.Conn)
	})
	// The pages a load test can open: the app's GET routes.
	pro("GET /api/pro/perf/routes", func(r *http.Request) (any, error) {
		s, inst, err := laravelSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		raw, err := rt.cachedRun(r.Context(), s, inst, "routes", 30*time.Second)
		if err != nil {
			return nil, err
		}
		var res struct {
			Routes []webhook.Route `json:"routes"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		type page struct {
			URI  string `json:"uri"`
			Name string `json:"name,omitempty"`
			Auth bool   `json:"auth"`
		}
		out := []page{}
		for _, rt := range res.Routes {
			if !slices.Contains(rt.Methods, "GET") || strings.HasPrefix(rt.URI, "/_") || strings.HasPrefix(rt.URI, "/storage/") || rt.URI == "/up" {
				continue
			}
			out = append(out, page{URI: rt.URI, Name: rt.Name, Auth: rt.Auth})
		}
		return out, nil
	})
	// Who a load test can sign in as: guards, roles and permissions.
	pro("GET /api/pro/perf/auth", func(r *http.Request) (any, error) {
		s, inst, err := laravelSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		return rt.cachedRun(r.Context(), s, inst, "auth-info", 30*time.Second)
	})
	// Check signs in and out again, to show who the test will be.
	pro("POST /api/pro/perf/auth/check", func(r *http.Request) (any, error) {
		var a struct {
			Site string `json:"site"`
			LoadAuth
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&a); err != nil {
			return nil, err
		}
		s, err := findSite(a.Site)
		if err != nil {
			return nil, err
		}
		res, cleanup, err := signIn(r.Context(), s, &a.LoadAuth)
		if err != nil {
			return nil, err
		}
		cleanup()
		mode := "session"
		if len(res.Headers) > 0 {
			mode = "token"
		}
		return map[string]string{"user": res.User.Label, "id": res.User.ID, "mode": mode}, nil
	})
	pro("POST /api/pro/perf/load", func(r *http.Request) (any, error) {
		var o LoadOptions
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&o); err != nil {
			return nil, err
		}
		if _, err := findSite(o.Site); err != nil {
			return nil, err
		}
		for _, j := range rt.jobs.Snapshot() {
			if strings.HasPrefix(j.Title, "Load test") && !j.Done {
				return nil, errors.New("a load test is already running: wait for it to finish")
			}
		}
		title := fmt.Sprintf("Load test: %d users on %s%s", min(max(o.Users, 1), 200), o.Site, o.Path)
		if o.As != nil {
			title += " (signed in)"
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			res, err := rt.runLoadTest(context.Background(), o, j)
			if err != nil {
				return err
			}
			j.SetResult(res)
			j.Set(1, fmt.Sprintf("%.0f requests/s · typical %.0f ms · 95%% within %.0f ms", res.RPS, res.P50, res.P95))
			return nil
		}, nil), nil
	})
}
