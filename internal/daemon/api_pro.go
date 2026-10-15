package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"laramonster/internal/account"
	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/laravel"
	"laramonster/internal/php"
	"laramonster/internal/sites"
	"laramonster/internal/update"
)

// errProRequired is answered with 402 and the account state, so the UI can
// say why and offer sign-in / plans instead of showing an error.
var errProRequired = errors.New("pro_required")

// registerPro adds the account endpoints and the Pro features (Event
// Tracker, Jobs & Queues).
func (rt *Runtime) registerPro(handle func(string, apiFunc), mux *http.ServeMux) {
	// ── account (always available) ──
	handle("GET /api/account", func(*http.Request) (any, error) { return rt.acct.Status(), nil })
	// Newest release vs this build (cached 6h): the sidebar's "Update available".
	// ?refresh=1: "Check for updates" in Settings asks again now.
	handle("GET /api/update", func(r *http.Request) (any, error) {
		if r.URL.Query().Get("refresh") == "1" {
			return update.Refresh(), nil
		}
		return update.Check(), nil
	})
	handle("POST /api/account/sign-in", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		replaced, err := rt.acct.SignIn(b.str("email"), b.str("password"), b.str("key"))
		if err != nil {
			var p *account.ProblemError
			if errors.As(err, &p) {
				return map[string]any{"ok": false, "code": p.Code, "message": p.Message, "account": rt.acct.Status()}, nil
			}
			return nil, err
		}
		_ = rt.Reload()
		return map[string]any{"ok": true, "replaced": replaced, "account": rt.acct.Status()}, nil
	})
	handle("POST /api/account/check", func(*http.Request) (any, error) {
		rt.checkLicense()
		return rt.acct.Status(), nil
	})
	handle("POST /api/account/sign-out", func(*http.Request) (any, error) {
		rt.acct.SignOut()
		_ = rt.Reload()
		return rt.acct.Status(), nil
	})

	pro := func(pattern string, fn apiFunc) {
		handle(pattern, func(r *http.Request) (any, error) {
			if !rt.acct.Pro() {
				return nil, errProRequired
			}
			return fn(r)
		})
	}

	rt.registerPerf(pro)

	// ── sites the Pro tools work on ──
	pro("GET /api/pro/sites", func(*http.Request) (any, error) {
		cfg, list, err := core.Sites()
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, s := range list {
			if !s.Laravel || s.Proxy != "" {
				continue
			}
			out = append(out, map[string]any{"name": s.Name, "path": s.Path, "url": s.URL, "php": s.PHP, "enabled": s.Enabled,
				"tracking": cfg.Tracking[s.Name], "ready": laravel.IsLaravel(s.Path)})
		}
		return out, nil
	})
	pro("POST /api/pro/tracking", rt.mutating(func(b body) error {
		s, err := findSite(b.str("site"))
		if err != nil {
			return err
		}
		if b.bool("on") && !laravel.IsLaravel(s.Path) {
			return errors.New(s.Name + " isn't ready: run composer install in it first")
		}
		return core.Mutate(func(c *config.Config) error {
			if b.bool("on") {
				c.Tracking[s.Name] = true
			} else {
				delete(c.Tracking, s.Name)
			}
			return nil
		})
	}))

	// ── Event Tracker ──
	pro("GET /api/pro/events", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		file := laravel.TraceFile(s.Path)
		_ = laravel.TrimTraces(file)
		traces, err := laravel.ReadTraces(file, 300)
		if err != nil {
			return nil, err
		}
		summaries := make([]laravel.TraceSummary, 0, len(traces))
		for _, t := range traces {
			summaries = append(summaries, t.Summary())
		}
		cfg, _ := config.Load()
		return map[string]any{"tracking": cfg.Tracking[s.Name], "traces": summaries}, nil
	})
	pro("GET /api/pro/events/trace", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		t, ok := laravel.FindTrace(laravel.TraceFile(s.Path), r.URL.Query().Get("id"))
		if !ok {
			return nil, errors.New("that trace is no longer kept; newer ones replaced it")
		}
		return t, nil
	})
	pro("POST /api/pro/events/clear", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, laravel.ClearTraces(laravel.TraceFile(s.Path))
	})
	pro("GET /api/pro/events/catalog", func(r *http.Request) (any, error) {
		s, inst, err := laravelSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		raw, err := rt.cachedRun(r.Context(), s, inst, "events", 20*time.Second)
		if err != nil {
			return nil, err
		}
		var res struct {
			OK     bool              `json:"ok"`
			Events []laravel.Event   `json:"events"`
			Error  *laravel.RunError `json:"error"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		if !res.OK && res.Error != nil {
			return nil, fmt.Errorf("the project failed while loading: %s", res.Error.Error())
		}
		type withFrom struct {
			laravel.Event
			From []laravel.Location `json:"from"`
		}
		out := []withFrom{}
		for _, e := range res.Events {
			out = append(out, withFrom{e, laravel.FindReferences(s.Path, e.Class, e.File)})
		}
		return out, nil
	})
	pro("POST /api/pro/events/dispatch", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, inst, err := laravelSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		raw, err := laravel.Run(r.Context(), inst, s.Path, "dispatch-event", map[string]any{"class": b.str("class"), "args": b["args"], "raw": b["raw"]}, 2*time.Minute)
		if err != nil {
			return nil, err
		}
		rt.saveRunTrace(s, raw, "Dispatched from LaraMonster")
		return raw, nil
	})

	// ── Jobs & Queues ──
	pro("GET /api/pro/jobs", func(r *http.Request) (any, error) {
		s, inst, err := laravelSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		raw, err := rt.cachedRun(r.Context(), s, inst, "jobs", 20*time.Second)
		if err != nil {
			return nil, err
		}
		return analyzeJobs(s, raw)
	})
	pro("POST /api/pro/jobs/run", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, inst, err := laravelSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		mode := b.str("mode")
		if mode != "queue" {
			mode = "sync"
		}
		raw, err := laravel.Run(r.Context(), inst, s.Path, "run-job", map[string]any{
			"class": b.str("class"), "args": b["args"], "raw": b["raw"], "mode": mode,
			"connection": b.str("connection"), "queue": b.str("queue"), "delay": int(b.num("delay")),
		}, 10*time.Minute)
		if err != nil {
			return nil, err
		}
		if mode == "sync" {
			rt.saveRunTrace(s, raw, "Run from LaraMonster")
		}
		return raw, nil
	})
	pro("POST /api/pro/artisan", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, inst, err := laravelSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		args, err := splitArgs(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(b.str("command"), "php artisan")), "artisan")))
		if err != nil || len(args) == 0 {
			return nil, errors.New("type an artisan command, like queue:retry all or schedule:list")
		}
		if args[0] == "tinker" || args[0] == "serve" || args[0] == "queue:work" || args[0] == "queue:listen" || args[0] == "schedule:work" || args[0] == "pail" {
			return nil, fmt.Errorf("%s keeps running; start workers from the Workers section, or run it in a terminal", args[0])
		}
		out, code, err := laravel.Artisan(r.Context(), inst, s.Path, args, 5*time.Minute)
		if err != nil {
			return nil, err
		}
		return map[string]any{"output": out, "exit_code": code}, nil
	})
	pro("GET /api/pro/queues", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		s, inst, err := laravelSite(q.Get("site"))
		if err != nil {
			return nil, err
		}
		var queues []string
		for _, name := range strings.Split(q.Get("queues"), ",") {
			if name = strings.TrimSpace(name); name != "" {
				queues = append(queues, name)
			}
		}
		raw, err := laravel.Run(r.Context(), inst, s.Path, "queues", map[string]any{"connection": q.Get("connection"), "queues": queues}, time.Minute)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": raw, "workers": rt.workers.List(s.Name)}, nil
	})
	pro("POST /api/pro/queues/action", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, inst, err := laravelSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		return laravel.Run(r.Context(), inst, s.Path, "queue-action", map[string]any{"action": b.str("action"), "id": b.str("id"), "queue": b.str("queue"), "connection": b.str("connection")}, 2*time.Minute)
	})
	pro("POST /api/pro/workers", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		if b.str("action") == "stop" {
			rt.workers.Stop(b.str("id"))
			return rt.workers.List(""), nil
		}
		s, inst, err := laravelSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		if _, err := rt.workers.Start(s.Name, inst, s.Path, b.str("connection"), b.str("queue"), int(b.num("tries"))); err != nil {
			return nil, err
		}
		return rt.workers.List(s.Name), nil
	})
	pro("GET /api/pro/workers/log", func(r *http.Request) (any, error) {
		return map[string]any{"lines": rt.workers.Tail(r.URL.Query().Get("id"), 200)}, nil
	})

	// ── PDF guides ──
	pro("POST /api/pro/export", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		return rt.export(r.Context(), b)
	})
	pro("POST /api/pro/reveal", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		p := filepath.Clean(b.str("path"))
		if !strings.HasPrefix(p, exportDir()+string(filepath.Separator)) || filepath.Ext(p) != ".pdf" {
			return nil, errors.New("only exported guides can be opened")
		}
		if b.bool("folder") {
			return map[string]bool{"ok": true}, core.Open(filepath.Dir(p))
		}
		return map[string]bool{"ok": true}, core.Open(p)
	})
	_ = mux
}

// checkLicense asks monstersgate.com and reloads when Pro turned on or off
// (event tracking follows the license).
func (rt *Runtime) checkLicense() {
	before := rt.acct.Pro()
	_ = rt.acct.Check()
	if rt.acct.Pro() != before {
		_ = rt.Reload()
	}
}

func (rt *Runtime) licenseLoop(stop <-chan struct{}) {
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			rt.checkLicense()
			t.Reset(account.CheckEvery)
		}
	}
}

func laravelSite(name string) (sites.Site, php.Install, error) {
	s, err := findSite(name)
	if err != nil {
		return s, php.Install{}, err
	}
	if !laravel.IsLaravel(s.Path) {
		return s, php.Install{}, errors.New(s.Name + " isn't a Laravel project with its dependencies installed (run composer install)")
	}
	inst, err := core.PHPFor(s.Path)
	if err != nil {
		return s, php.Install{}, err
	}
	return s, inst, nil
}

var (
	runCacheMu sync.Mutex
	runCache   = map[string]struct {
		at  time.Time
		raw json.RawMessage
	}{}
)

// cachedRun keeps inspection results briefly: opening a page shouldn't
// boot the project twice.
func (rt *Runtime) cachedRun(ctx context.Context, s sites.Site, inst php.Install, command string, ttl time.Duration) (json.RawMessage, error) {
	key := s.Path + "|" + command
	runCacheMu.Lock()
	if c, ok := runCache[key]; ok && time.Since(c.at) < ttl {
		runCacheMu.Unlock()
		return c.raw, nil
	}
	runCacheMu.Unlock()
	raw, err := laravel.Run(ctx, inst, s.Path, command, map[string]any{}, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	runCacheMu.Lock()
	runCache[key] = struct {
		at  time.Time
		raw json.RawMessage
	}{time.Now(), raw}
	runCacheMu.Unlock()
	return raw, nil
}

// saveRunTrace adds what a dispatch/run did to the site's trace list, so it
// can be reopened and exported like any other trace.
func (rt *Runtime) saveRunTrace(s sites.Site, raw json.RawMessage, title string) {
	var res struct {
		Trace laravel.Trace `json:"trace"`
		Event string        `json:"event"`
		Job   string        `json:"job"`
		Error json.RawMessage
	}
	if json.Unmarshal(raw, &res) != nil || res.Trace.ID == "" {
		return
	}
	t := res.Trace
	t.Kind = "laramonster"
	t.Title = strings.TrimSpace(title + ": " + shortName(res.Event+res.Job))
	if len(res.Error) > 0 && string(res.Error) != "null" {
		t.Exception = res.Error
		t.Status = "failed"
	} else {
		t.Status = "done"
	}
	line, err := json.Marshal(t)
	if err != nil {
		return
	}
	if f, err := os.OpenFile(laravel.TraceFile(s.Path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.Write(append(line, '\n'))
		f.Close()
	}
}

type analyzedJob struct {
	laravel.Job
	Analysis laravel.JobAnalysis `json:"analysis"`
}

func analyzeJobs(s sites.Site, raw json.RawMessage) (any, error) {
	var res struct {
		OK                bool                    `json:"ok"`
		Jobs              []laravel.Job           `json:"jobs"`
		Schedule          []laravel.ScheduleEntry `json:"schedule"`
		DefaultConnection string                  `json:"default_connection"`
		Timezone          string                  `json:"timezone"`
		Error             *laravel.RunError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if !res.OK && res.Error != nil {
		return nil, fmt.Errorf("the project failed while loading: %s", res.Error.Error())
	}
	loc, _ := time.LoadLocation(res.Timezone)
	jobs := []analyzedJob{}
	queues := map[string]bool{}
	for _, j := range res.Jobs {
		a := laravel.AnalyzeJob(j, res.Schedule, laravel.FindReferences(s.Path, j.Class, j.File), res.DefaultConnection, loc)
		jobs = append(jobs, analyzedJob{j, a})
		if q, ok := j.Settings["queue"].(string); ok && q != "" {
			queues[q] = true
		}
		if m := strings.Index(j.ConstructorSource, "onQueue("); m >= 0 {
			rest := j.ConstructorSource[m+8:]
			if end := strings.IndexAny(rest, ")"); end > 0 {
				queues[strings.Trim(rest[:end], `'" `)] = true
			}
		}
	}
	names := []string{}
	for q := range queues {
		names = append(names, q)
	}
	return map[string]any{"jobs": jobs, "schedule": res.Schedule, "default_connection": res.DefaultConnection, "timezone": res.Timezone, "queues": names}, nil
}

func exportDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Downloads")
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
			if d := strings.TrimSpace(string(out)); d != "" && d != home {
				dir = d
			}
		}
	}
	return filepath.Join(dir, "LaraMonster")
}

// export writes a PDF guide (event, job or trace) to Downloads/LaraMonster.
func (rt *Runtime) export(ctx context.Context, b body) (any, error) {
	s, inst, err := laravelSite(b.str("site"))
	if err != nil {
		return nil, err
	}
	var trace *laravel.Trace
	if id := b.str("trace"); id != "" {
		if t, ok := laravel.FindTrace(laravel.TraceFile(s.Path), id); ok {
			trace = &t
		}
	}
	var pdf []byte
	var name string
	switch b.str("kind") {
	case "event":
		raw, err := rt.cachedRun(ctx, s, inst, "events", 20*time.Second)
		if err != nil {
			return nil, err
		}
		var res struct {
			Events []laravel.Event `json:"events"`
		}
		_ = json.Unmarshal(raw, &res)
		var ev *laravel.Event
		for i := range res.Events {
			if res.Events[i].Class == b.str("class") {
				ev = &res.Events[i]
			}
		}
		if ev == nil {
			return nil, errors.New("that event class wasn't found in the project")
		}
		if trace == nil {
			trace = latestTraceWith(s.Path, ev.Class)
		}
		pdf, err = laravel.EventGuide(s.Name, *ev, trace, laravel.FindReferences(s.Path, ev.Class, ev.File))
		if err != nil {
			return nil, err
		}
		name = shortName(ev.Class) + " event guide"
	case "job":
		raw, err := rt.cachedRun(ctx, s, inst, "jobs", 20*time.Second)
		if err != nil {
			return nil, err
		}
		jobs, err := analyzeJobs(s, raw)
		if err != nil {
			return nil, err
		}
		var job *analyzedJob
		for _, j := range jobs.(map[string]any)["jobs"].([]analyzedJob) {
			if j.Class == b.str("class") {
				job = &j
				break
			}
		}
		if job == nil {
			return nil, errors.New("that job class wasn't found in the project")
		}
		pdf, err = laravel.JobGuide(s.Name, job.Job, job.Analysis, trace)
		if err != nil {
			return nil, err
		}
		name = shortName(job.Class) + " job guide"
	case "trace":
		if trace == nil {
			return nil, errors.New("that trace is no longer kept")
		}
		pdf, err = laravel.TraceReport(s.Name, *trace)
		if err != nil {
			return nil, err
		}
		name = "trace " + strings.NewReplacer("/", "-", " ", "-", ":", "-").Replace(trace.Title)
	default:
		return nil, errors.New("unknown export")
	}
	if err := os.MkdirAll(exportDir(), 0o755); err != nil {
		return nil, err
	}
	file := filepath.Join(exportDir(), fmt.Sprintf("%s - %s - %s.pdf", s.Name, name, time.Now().Format("2006-01-02 1504")))
	if err := os.WriteFile(file, pdf, 0o644); err != nil {
		return nil, err
	}
	return map[string]string{"path": file, "name": filepath.Base(file), "folder": exportDir()}, nil
}

func latestTraceWith(projectDir, class string) *laravel.Trace {
	traces, _ := laravel.ReadTraces(laravel.TraceFile(projectDir), 300)
	for _, t := range traces {
		for _, e := range t.Events {
			if e.Name == class {
				return &t
			}
		}
	}
	return nil
}

func shortName(class string) string {
	if i := strings.LastIndex(class, `\`); i >= 0 {
		return class[i+1:]
	}
	return class
}

// splitArgs splits a command line on spaces, honouring quotes.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote rune
	inArg := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
