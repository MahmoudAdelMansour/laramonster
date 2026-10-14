package daemon

import (
	"fmt"
	"net/http"

	"laramonster/internal/core"
	"laramonster/internal/sites"
)

// registerSiteSettings adds on/off, own-server, rename and .env endpoints.
func (rt *Runtime) registerSiteSettings(handle func(string, apiFunc)) {
	handle("POST /api/sites/enable", rt.mutating(func(b body) error { return core.SetEnabled(b.str("name"), b.bool("on")) }))
	handle("POST /api/sites/enable-many", rt.mutating(func(b body) error {
		names, _ := b["names"].([]any)
		var list []string
		for _, n := range names {
			if s, ok := n.(string); ok {
				list = append(list, s)
			}
		}
		return core.SetEnabledMany(list, b.bool("on"))
	}))
	handle("GET /api/monitor", func(r *http.Request) (any, error) { return rt.mon.Report(r.URL.Query().Get("peek") == ""), nil })
	handle("POST /api/sites/proxy", rt.mutating(func(b body) error { return core.SetProxy(b.str("name"), b.str("target")) }))
	handle("POST /api/sites/rename", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		res, err := core.Rename(b.str("name"), b.str("to"))
		if err != nil {
			return nil, err
		}
		return res, rt.Reload()
	})

	handle("GET /api/sites/env", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("name"))
		if err != nil {
			return nil, err
		}
		return core.ReadEnv(s.Path)
	})
	handle("POST /api/sites/env", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("name"))
		if err != nil {
			return nil, err
		}
		content, _ := b["content"].(string) // not trimmed: whitespace is the user's
		if err := core.WriteEnv(s.Path, content); err != nil {
			return nil, err
		}
		return core.ReadEnv(s.Path)
	})
	handle("POST /api/sites/env/create", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("name"))
		if err != nil {
			return nil, err
		}
		if err := core.CreateEnv(s.Path); err != nil {
			return nil, err
		}
		return core.ReadEnv(s.Path)
	})
	handle("POST /api/sites/env/clear-cache", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("name"))
		if err != nil {
			return nil, err
		}
		if err := core.ClearConfigCache(s.Path); err != nil {
			return nil, err
		}
		return core.ReadEnv(s.Path)
	})
}

func findSite(name string) (sites.Site, error) {
	_, list, err := core.Sites()
	if err != nil {
		return sites.Site{}, err
	}
	s, ok := sites.Find(list, name)
	if !ok {
		return sites.Site{}, fmt.Errorf("no site named %q", name)
	}
	return s, nil
}
