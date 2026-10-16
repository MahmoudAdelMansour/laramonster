package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/share"
)

// registerShare adds public links: a site opened to the internet through a
// Cloudflare tunnel, optionally behind a username and password.
func (rt *Runtime) registerShare(handle func(string, apiFunc)) {
	handle("GET /api/shares", func(r *http.Request) (any, error) {
		out := map[string]any{"links": rt.shares.List(), "installed": share.Installed() != ""}
		if name := r.URL.Query().Get("site"); name != "" {
			s, err := findSite(name)
			if err != nil {
				return nil, err
			}
			// Vite's dev server (public/hot) serves assets from localhost,
			// which visitors of the public link can't reach.
			_, err = os.Stat(filepath.Join(s.Root, "hot"))
			out["vite_hot"] = err == nil && s.Proxy == ""
		}
		return out, nil
	})
	handle("POST /api/shares/start", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		if !s.Enabled {
			return nil, errors.New(s.Name + " is switched off: turn it on first")
		}
		o := share.Options{Minutes: int(b.num("minutes"))}
		if b.bool("protect") {
			o.User, o.Password = b.str("user"), b.str("password")
			if o.User == "" || o.Password == "" {
				return nil, errors.New("enter a username and a password, or make the link open to anyone")
			}
		}
		if open, ok := b["open"].([]any); ok {
			for _, p := range open {
				if p, ok := p.(string); ok && p != "" {
					o.Open = append(o.Open, p)
				}
			}
		}
		// Webhook routes stay reachable behind a password: providers can't
		// log in (Webhooks, Pro, found them earlier).
		if o.User != "" && rt.acct.Pro() {
			cfg, _ := config.Load()
			for _, f := range readWebhookRoutes(s.Path).Routes {
				o.Open = append(o.Open, caddyPath(f.URI))
			}
			for _, p := range cfg.WebhookPaths[s.Name] {
				o.Open = append(o.Open, caddyPath(p))
			}
			slices.Sort(o.Open)
			o.Open = slices.Compact(o.Open)
		}
		title := "Share " + s.Name
		if rt.jobs.Busy(title) {
			return nil, errors.New("the link for " + s.Name + " is already starting")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			if share.Installed() == "" {
				j.Set(-1, "Downloading Cloudflare's tunnel program (once)")
			} else {
				j.Set(-1, "Opening a tunnel")
			}
			if err := rt.shares.Start(s.Name, o, j.Bytes); err != nil {
				return err
			}
			j.Set(-1, "Waiting for Cloudflare to give the link")
			for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
				l, ok := rt.shares.Get(s.Name)
				if !ok {
					return errors.New("the link was closed")
				}
				switch l.Status {
				case "ready":
					j.SetResult(l)
					j.Set(1, l.URL)
					return nil
				case "error":
					return errors.New(l.Error)
				}
			}
			rt.shares.Stop(s.Name)
			return fmt.Errorf("Cloudflare didn't answer within 45 seconds: check your internet connection and try again")
		}, nil), nil
	})
	handle("POST /api/shares/stop", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		rt.shares.Stop(b.str("site"))
		return map[string]any{"links": rt.shares.List()}, nil
	})
}

var routeParam = regexp.MustCompile(`\{[^}]*\}`)

// caddyPath turns a route URI into a Caddy path matcher: {id} becomes *.
func caddyPath(uri string) string {
	return "/" + strings.TrimLeft(routeParam.ReplaceAllString(uri, "*"), "/")
}
