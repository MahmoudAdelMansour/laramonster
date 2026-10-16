package daemon

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/laravel"
	"laramonster/internal/php"
)

// registerDebug adds the Debugger (free): per site, the dumps, queries,
// logs and exceptions of each request, job and command.
func (rt *Runtime) registerDebug(handle func(string, apiFunc)) {
	handle("GET /api/debug/sites", func(*http.Request) (any, error) {
		cfg, list, err := core.Sites()
		if err != nil {
			return nil, err
		}
		installs := php.Discover()
		out := []map[string]any{}
		for _, s := range list {
			if s.Proxy != "" || !s.Laravel {
				continue
			}
			inst, _ := php.Find(installs, s.PHP)
			out = append(out, map[string]any{
				"name": s.Name, "url": s.URL, "php": s.PHP, "enabled": s.Enabled,
				"debugging": cfg.Debugging[s.Name], "quiet": cfg.DebugQuiet[s.Name],
				"ready": laravel.IsLaravel(s.Path), "xdebug": inst.HasExtension("xdebug") || cfg.PHPExtensions[inst.Version]["xdebug"],
				"xdebug_on": cfg.PHPSettings[core.SiteScope(s.Name)]["xdebug.mode"] == "debug",
			})
		}
		return out, nil
	})
	handle("POST /api/debug/toggle", rt.mutating(func(b body) error {
		s, err := findSite(b.str("site"))
		if err != nil {
			return err
		}
		if b.bool("on") && !laravel.IsLaravel(s.Path) {
			return errors.New(s.Name + " isn't ready: run composer install in it first")
		}
		return core.Mutate(func(c *config.Config) error {
			if b.bool("on") {
				c.Debugging[s.Name] = true
			} else {
				delete(c.Debugging, s.Name)
			}
			return nil
		})
	}))
	handle("POST /api/debug/quiet", rt.mutating(func(b body) error {
		s, err := findSite(b.str("site"))
		if err != nil {
			return err
		}
		return core.Mutate(func(c *config.Config) error {
			if b.bool("on") {
				c.DebugQuiet[s.Name] = true
			} else {
				delete(c.DebugQuiet, s.Name)
			}
			return nil
		})
	}))
	// Xdebug step debugging for one site: its own PHP settings, so the
	// other sites stay fast.
	handle("POST /api/debug/xdebug", rt.mutating(func(b body) error {
		s, err := findSite(b.str("site"))
		if err != nil {
			return err
		}
		var mode, start *string
		if b.bool("on") {
			inst, ok := php.Find(php.Discover(), s.PHP)
			cfg, _ := config.Load()
			if !ok || !(inst.HasExtension("xdebug") || cfg.PHPExtensions[inst.Version]["xdebug"]) {
				return errors.New("PHP " + s.PHP + " doesn't have Xdebug: add it in PHP → Settings → Extensions first")
			}
			d, y := "debug", "yes"
			mode, start = &d, &y
		}
		return core.SetPHPSettings(core.SiteScope(s.Name), map[string]*string{"xdebug.mode": mode, "xdebug.start_with_request": start})
	}))
	handle("GET /api/debug/entries", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		file := laravel.DebugFile(s.Path)
		_ = laravel.TrimDebug(file)
		all, err := laravel.ReadDebug(file, 400)
		if err != nil {
			return nil, err
		}
		out := make([]laravel.DebugSummary, 0, len(all))
		for _, e := range all {
			out = append(out, e.Summary())
		}
		cfg, _ := config.Load()
		return map[string]any{"debugging": cfg.Debugging[s.Name], "entries": out}, nil
	})
	handle("GET /api/debug/dumps", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		all, err := laravel.ReadDebug(laravel.DebugFile(s.Path), 400)
		if err != nil {
			return nil, err
		}
		return laravel.Dumps(all, 300), nil
	})
	handle("GET /api/debug/entry", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		e, ok := laravel.FindDebug(laravel.DebugFile(s.Path), r.URL.Query().Get("id"))
		if !ok {
			return nil, errors.New("that entry is no longer kept: newer ones replaced it")
		}
		return e, nil
	})
	handle("POST /api/debug/clear", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		if err := os.Remove(laravel.DebugFile(s.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
	handle("GET /api/debug/source", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		s, err := findSite(q.Get("site"))
		if err != nil {
			return nil, err
		}
		line, _ := strconv.Atoi(q.Get("line"))
		start, lines, err := laravel.SourceLines(s.Path, q.Get("file"), line, 8)
		if err != nil {
			return nil, err
		}
		return map[string]any{"start": start, "lines": lines, "line": line}, nil
	})
	handle("GET /api/debug/log", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		file := laravel.LogFile(s.Path)
		if file == "" {
			return map[string]any{"file": "", "entries": []laravel.LogEntry{}}, nil
		}
		entries, err := laravel.ReadLog(file, 300)
		if err != nil {
			return nil, err
		}
		return map[string]any{"file": file, "entries": entries}, nil
	})
	handle("POST /api/debug/log/clear", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		if file := laravel.LogFile(s.Path); file != "" {
			if err := os.Truncate(file, 0); err != nil {
				return nil, err
			}
		}
		return map[string]bool{"ok": true}, nil
	})
	handle("POST /api/debug/open", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, core.OpenEditorAt(s.Path, b.str("file"), int(b.num("line")))
	})
}
