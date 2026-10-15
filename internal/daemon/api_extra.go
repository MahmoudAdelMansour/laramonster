package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/services"
	"laramonster/internal/sites"
	"laramonster/internal/tools"
)

// registerExtra adds projects, packages, tools and mail endpoints.
func (rt *Runtime) registerExtra(handle func(string, apiFunc), mux *http.ServeMux) {
	// --- projects ---
	handle("GET /api/projects/catalog", func(*http.Request) (any, error) {
		return map[string]any{
			"kits":       core.StarterKits,
			"packages":   core.Packages,
			"categories": core.PackageCategories,
		}, nil
	})
	handle("POST /api/projects/new", func(r *http.Request) (any, error) {
		var o core.ProjectOptions
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&o); err != nil {
			return nil, err
		}
		if sites.Slug(o.Name) == "" {
			return nil, errors.New("enter a project name")
		}
		title := "Create " + sites.Slug(o.Name)
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			target, warnings, err := core.CreateProject(o, rt.projectEnv(), j)
			if err != nil {
				return err
			}
			j.SetResult(map[string]any{"path": target, "warnings": warnings})
			if len(warnings) > 0 {
				j.Set(1, fmt.Sprintf("created with %d warning(s)", len(warnings)))
			}
			return nil
		}, rt.reloadQuiet), nil
	})

	// --- packagist ---
	handle("GET /api/packages/search", func(r *http.Request) (any, error) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			return rt.topPackages()
		}
		return packagist("https://packagist.org/search.json?per_page=20&q=" + url.QueryEscape(q))
	})

	// --- packages in an existing project ---
	handle("GET /api/sites/packages", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("name"))
		if err != nil {
			return nil, err
		}
		dir, err := core.ProjectDir(s.Path)
		if err != nil {
			return nil, err
		}
		installed, err := core.ComposerPackages(dir)
		if err != nil {
			return nil, err
		}
		inst, err := core.PHPFor(dir)
		if err != nil {
			return nil, err
		}
		return map[string]any{"installed": installed, "php": inst}, nil
	})
	handle("POST /api/sites/packages", func(r *http.Request) (any, error) {
		var b struct {
			Name     string   `json:"name"`
			Packages []string `json:"packages"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
			return nil, err
		}
		if len(b.Packages) == 0 {
			return nil, errors.New("choose at least one package")
		}
		s, err := findSite(b.Name)
		if err != nil {
			return nil, err
		}
		dir, err := core.ProjectDir(s.Path)
		if err != nil {
			return nil, err
		}
		title := "Install packages in " + s.Name
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			inst, err := core.PHPFor(dir)
			if err != nil {
				return err
			}
			j.Set(-1, "Installing "+strings.Join(b.Packages, ", "))
			installed, warnings := core.InstallPackages(inst, dir, b.Packages, j)
			j.SetResult(map[string]any{"path": dir, "site": s.Name, "installed": installed, "warnings": warnings})
			if len(installed) == 0 {
				return errors.New("no package was installed: see the warnings")
			}
			j.Set(1, fmt.Sprintf("installed %d package(s)", len(installed)))
			return nil
		}, nil), nil
	})
	// A warning's one-click fix (create the database, run migrations again…).
	handle("POST /api/projects/fix", func(r *http.Request) (any, error) {
		var b struct {
			Name string   `json:"name"`
			Fix  core.Fix `json:"fix"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
			return nil, err
		}
		s, err := findSite(b.Name)
		if err != nil {
			return nil, err
		}
		title := b.Fix.Label + " (" + s.Name + ")"
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			warnings, err := core.RunFix(s.Path, b.Fix, rt.projectEnv(), j)
			j.SetResult(map[string]any{"path": s.Path, "site": s.Name, "warnings": warnings})
			if err == nil && len(warnings) > 0 {
				j.Set(1, fmt.Sprintf("finished with %d warning(s)", len(warnings)))
			}
			return err
		}, rt.reloadQuiet), nil
	})

	// --- tools ---
	handle("GET /api/tools", func(r *http.Request) (any, error) {
		if r.URL.Query().Get("fresh") != "" {
			tools.Invalidate()
		}
		pm, _ := tools.DetectManager()
		return map[string]any{"tools": tools.States(), "manager": pm.Name}, nil
	})
	handle("POST /api/tools/{id}/install", func(r *http.Request) (any, error) {
		t, ok := tools.Get(r.PathValue("id"))
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", r.PathValue("id"))
		}
		title := "Install " + t.Name
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			if err := tools.Install(t, j.Bytes, j); err != nil {
				return err
			}
			fmt.Fprintf(j, "%s installed", t.Name)
			return nil
		}, rt.reloadQuiet), nil
	})

	// --- mail (Mailpit) ---
	handle("GET /api/mail/messages", func(r *http.Request) (any, error) {
		q := url.Values{"limit": {"50"}, "start": {r.URL.Query().Get("start")}}
		path := "/api/v1/messages"
		if s := strings.TrimSpace(r.URL.Query().Get("q")); s != "" {
			path, q["query"] = "/api/v1/search", []string{s}
		}
		return rt.mailpitJSON("GET", path+"?"+q.Encode(), nil)
	})
	handle("GET /api/mail/messages/{id}", func(r *http.Request) (any, error) {
		return rt.mailpitJSON("GET", "/api/v1/message/"+url.PathEscape(r.PathValue("id")), nil)
	})
	handle("GET /api/mail/messages/{id}/raw", func(r *http.Request) (any, error) {
		b, err := rt.mailpit("GET", "/api/v1/message/"+url.PathEscape(r.PathValue("id"))+"/raw", nil)
		return map[string]string{"raw": string(b)}, err
	})
	handle("POST /api/mail/delete", func(r *http.Request) (any, error) {
		var body struct{ IDs []string }
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		var payload any
		if len(body.IDs) > 0 {
			payload = map[string]any{"IDs": body.IDs}
		}
		_, err := rt.mailpit("DELETE", "/api/v1/messages", payload)
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /api/mail/read", func(r *http.Request) (any, error) {
		var body struct {
			IDs  []string
			Read bool
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		_, err := rt.mailpit("PUT", "/api/v1/messages", map[string]any{"IDs": body.IDs, "Read": body.Read})
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /api/mail/setup", func(*http.Request) (any, error) {
		d, _ := services.Get("mailpit")
		if rt.jobs.Busy("Set up Mail") {
			return nil, errors.New("mail setup is already running")
		}
		return rt.jobs.Run("Set up Mail", func(j *core.Job) error {
			if d.Find() == "" {
				fmt.Fprintln(j, "downloading Mailpit")
				if err := rt.svc.Install(d, j.Bytes); err != nil {
					return err
				}
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			sc := cfg.Service(d.Name, d.DefaultPort)
			sc.Enabled = true
			if err := cfg.Save(); err != nil {
				return err
			}
			if err := rt.svc.Start(d, sc.Port); err != nil {
				return err
			}
			fmt.Fprintln(j, "Mail inbox ready")
			return nil
		}, rt.reloadQuiet), nil
	})

	// Attachments and inline images, loaded by <img>/<a> so they can't carry
	// the API header; reject cross-site requests instead.
	mux.HandleFunc("GET /mail-part/{id}/{part}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		base, err := rt.mailpitBase()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		resp, err := http.Get(base + "/api/v1/message/" + url.PathEscape(r.PathValue("id")) + "/part/" + url.PathEscape(r.PathValue("part")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for _, h := range []string{"Content-Type", "Content-Disposition", "Content-Length"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

func (rt *Runtime) projectEnv() core.Env {
	return core.Env{
		Installed: func(name string) bool {
			d, ok := services.Get(name)
			return ok && d.Find() != ""
		},
		StartService: func(name string) (int, error) {
			d, ok := services.Get(name)
			if !ok {
				return 0, fmt.Errorf("unknown service %s", name)
			}
			if d.Find() == "" {
				return 0, fmt.Errorf("%s is not installed", d.Title)
			}
			cfg, err := config.Load()
			if err != nil {
				return 0, err
			}
			// A project now depends on it: keep it running across restarts.
			sc := cfg.Service(d.Name, d.DefaultPort)
			if !sc.Enabled {
				sc.Enabled = true
				if err := cfg.Save(); err != nil {
					return 0, err
				}
			}
			port := sc.Port
			// Another server (often the system's own MySQL or Redis) holds
			// the port: run LaraMonster's copy next to it instead of failing.
			if !rt.svc.Running(d.Name) && services.PortInUse(port) {
				free, err := services.FreePort(port)
				if err != nil {
					return 0, fmt.Errorf("port %d is used by another %s and %v", port, d.Title, err)
				}
				sc.Port, port = free, free
				if err := cfg.Save(); err != nil {
					return 0, err
				}
			}
			if err := rt.svc.Start(d, port); err != nil {
				return 0, err
			}
			if name == "mailpit" {
				go rt.reloadQuiet()
			}
			return port, nil
		},
	}
}

// --- mailpit client ---

var errMailOff = errors.New("the mail inbox isn't running: turn it on from the Mail page")

func (rt *Runtime) mailpitBase() (string, error) {
	d, _ := services.Get("mailpit")
	if !rt.svc.Running(d.Name) {
		return "", errMailOff
	}
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", d.WebPort(cfg.Service(d.Name, d.DefaultPort).Port)), nil
}

var mailClient = &http.Client{Timeout: 10 * time.Second}

func (rt *Runtime) mailpit(method, path string, body any) ([]byte, error) {
	base, err := rt.mailpitBase()
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := mailClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mailpit: %s", strings.TrimSpace(string(data)))
	}
	return data, err
}

func (rt *Runtime) mailpitJSON(method, path string, body any) (any, error) {
	b, err := rt.mailpit(method, path, body)
	if err != nil {
		return nil, err
	}
	var v any
	return v, json.Unmarshal(b, &v)
}

// MailStatus is shown in the nav badge and on the Mail page.
type MailStatus struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
	Total     int  `json:"total"`
	Unread    int  `json:"unread"`
}

func (rt *Runtime) mailStatus() MailStatus {
	d, _ := services.Get("mailpit")
	st := MailStatus{Installed: d.Find() != "", Running: rt.svc.Running(d.Name)}
	if st.Running {
		if b, err := rt.mailpit("GET", "/api/v1/messages?limit=1", nil); err == nil {
			var v struct{ Total, Unread int }
			if json.Unmarshal(b, &v) == nil {
				st.Total, st.Unread = v.Total, v.Unread
			}
		}
	}
	return st
}

// --- packagist ---

type pkgResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Repository  string `json:"repository"`
	Downloads   int    `json:"downloads"`
	Favers      int    `json:"favers"`
	Abandoned   any    `json:"abandoned,omitempty"`
}

var pkgClient = &http.Client{Timeout: 15 * time.Second}

func packagist(u string) ([]pkgResult, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "LaraMonster (+https://monstersgate.com)")
	resp, err := pkgClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("packagist is unreachable: %w", err)
	}
	defer resp.Body.Close()
	var v struct {
		Results []pkgResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	out := v.Results[:0]
	for _, r := range v.Results {
		if r.Abandoned == nil || r.Abandoned == false {
			out = append(out, r)
		}
	}
	return out, nil
}

var top struct {
	sync.Mutex
	at   time.Time
	list []pkgResult
}

// topPackages returns the most downloaded Laravel packages (cached 6h).
func (rt *Runtime) topPackages() ([]pkgResult, error) {
	top.Lock()
	defer top.Unlock()
	if time.Since(top.at) < 6*time.Hour && len(top.list) > 0 {
		return top.list, nil
	}
	list, err := packagist("https://packagist.org/search.json?tags=laravel&per_page=30")
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{"laravel/framework": true, "laravel/laravel": true, "laravel/tinker": true, "nunomaduro/collision": true}
	out := list[:0]
	for _, r := range list {
		if !skip[r.Name] {
			out = append(out, r)
		}
	}
	top.list, top.at = out, time.Now()
	return out, nil
}
