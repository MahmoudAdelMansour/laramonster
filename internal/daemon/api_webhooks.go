package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/paths"
	"laramonster/internal/server"
	"laramonster/internal/share"
	"laramonster/internal/sites"
	"laramonster/internal/webhook"
)

// Webhooks (Pro): find the routes that receive webhooks, record what
// arrives (locally or through a public link), explain each delivery, and
// replay or send test events.

func webhookDir() string { return filepath.Join(paths.Root(), "webhooks") }

func siteKey(dir string) string {
	sum := sha1.Sum([]byte(filepath.Clean(dir)))
	return hex.EncodeToString(sum[:8])
}

// deliveriesFile holds a site's recorded deliveries, one JSON per line.
func deliveriesFile(dir string) string { return filepath.Join(webhookDir(), siteKey(dir)+".ndjson") }

// routesFile keeps the webhook routes found last time, so recording works
// right after the engine starts.
func routesFile(dir string) string { return filepath.Join(webhookDir(), siteKey(dir)+".routes.json") }

type webhookRoutes struct {
	Routes    []webhook.Found `json:"routes"`
	Laravel11 bool            `json:"laravel11"`
}

func readWebhookRoutes(dir string) webhookRoutes {
	var r webhookRoutes
	if b, err := os.ReadFile(routesFile(dir)); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

var deliveryMu sync.Mutex

// recordDelivery appends a delivery, keeping the newest 300 per site.
func recordDelivery(d server.Delivery) {
	s, err := findSite(d.Site)
	if err != nil {
		return
	}
	deliveryMu.Lock()
	defer deliveryMu.Unlock()
	_ = os.MkdirAll(webhookDir(), 0o755)
	file := deliveriesFile(s.Path)
	line, err := json.Marshal(d)
	if err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	f.Close()
	if fi, err := os.Stat(file); err == nil && fi.Size() > 16<<20 {
		all := readDeliveries(file, 300)
		slices.Reverse(all)
		var buf bytes.Buffer
		for _, x := range all {
			b, _ := json.Marshal(x)
			buf.Write(append(b, '\n'))
		}
		_ = os.WriteFile(file, buf.Bytes(), 0o644)
	}
}

// readDeliveries returns the newest first.
func readDeliveries(file string, limit int) []server.Delivery {
	f, err := os.Open(file)
	if err != nil {
		return []server.Delivery{}
	}
	defer f.Close()
	var all []server.Delivery
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		var d server.Delivery
		if json.Unmarshal(sc.Bytes(), &d) == nil && d.ID != "" {
			all = append(all, d)
		}
	}
	slices.Reverse(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}

// applyWebhookWatch tells Caddy which paths to record: for every site with
// Webhooks on, its webhook routes and the paths added by hand.
func (rt *Runtime) applyWebhookWatch(cfg *config.Config, list []sites.Site) {
	watch := map[string][]*regexp.Regexp{}
	if rt.acct.Pro() {
		for _, s := range list {
			if !cfg.Webhooks[s.Name] {
				continue
			}
			var res []*regexp.Regexp
			for _, f := range readWebhookRoutes(s.Path).Routes {
				res = append(res, webhook.Pattern(f.URI))
			}
			for _, p := range cfg.WebhookPaths[s.Name] {
				res = append(res, webhook.Pattern(p))
			}
			watch[s.Name] = res
		}
	}
	server.WatchWebhooks(watch, recordDelivery)
}

// discoverWebhooks lists the webhook routes of a Laravel site (and saves
// them for recording).
func (rt *Runtime) discoverWebhooks(ctx context.Context, s sites.Site, refresh bool) (webhookRoutes, []webhook.Route, error) {
	_, inst, err := laravelSite(s.Name)
	if err != nil {
		return webhookRoutes{}, nil, err
	}
	ttl := 20 * time.Second
	if refresh {
		ttl = 0
	}
	raw, err := rt.cachedRun(ctx, s, inst, "routes", ttl)
	if err != nil {
		return webhookRoutes{}, nil, err
	}
	var res struct {
		OK        bool              `json:"ok"`
		Routes    []webhook.Route   `json:"routes"`
		Bootstrap bool              `json:"bootstrap_middleware"`
		Error     *runErrorFromJSON `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return webhookRoutes{}, nil, err
	}
	if !res.OK && res.Error != nil {
		return webhookRoutes{}, nil, fmt.Errorf("the project failed while loading its routes: %s: %s", res.Error.Class, res.Error.Message)
	}
	env := core.EnvValues(s.Path)
	cfg, _ := config.Load()
	out := webhookRoutes{Routes: webhook.Detect(res.Routes, env), Laravel11: res.Bootstrap}
	for _, p := range cfg.WebhookPaths[s.Name] {
		out.Routes = append(out.Routes, webhook.Custom(p, res.Routes, env))
	}
	if out.Routes == nil {
		out.Routes = []webhook.Found{}
	}
	_ = os.MkdirAll(webhookDir(), 0o755)
	if b, err := json.Marshal(webhookRoutes{Routes: webhook.Detect(res.Routes, env), Laravel11: res.Bootstrap}); err == nil {
		_ = os.WriteFile(routesFile(s.Path), b, 0o644)
	}
	return out, res.Routes, nil
}

type runErrorFromJSON struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

// deliveryView is a delivery with LaraMonster's reading of it.
type deliveryView struct {
	server.Delivery
	Provider  string              `json:"provider,omitempty"`
	ProvName  string              `json:"provider_name,omitempty"`
	Event     string              `json:"event,omitempty"`
	Route     string              `json:"route,omitempty"`
	Signature *webhook.Check      `json:"signature,omitempty"`
	Diagnosis []webhook.Diagnosis `json:"diagnosis,omitempty"`
	Pretty    string              `json:"pretty,omitempty"` // the body as indented JSON
}

func analyze(d server.Delivery, wr webhookRoutes, env map[string]string) deliveryView {
	v := deliveryView{Delivery: d}
	var route *webhook.Found
	for i, f := range wr.Routes {
		if webhook.Match(f.URI, d.Path) {
			route = &wr.Routes[i]
			break
		}
	}
	hint := ""
	if route != nil {
		hint, v.Route = route.Provider, route.URI
	}
	h := http.Header(d.Headers)
	p, ev := webhook.Identify(h, []byte(d.Body), hint)
	v.Provider, v.ProvName, v.Event = p.ID, p.Name, ev
	if p.ID != "" {
		c := webhook.Verify(p, h, []byte(d.Body), env, d.At)
		v.Signature = &c
	}
	v.Diagnosis = webhook.Diagnose(d.Status, d.MS, p, route, wr.Laravel11, d.Path)
	var any any
	if json.Unmarshal([]byte(d.Body), &any) == nil {
		if b, err := json.MarshalIndent(any, "", "  "); err == nil {
			v.Pretty = string(b)
		}
	}
	return v
}

// summary keeps the list light: no bodies.
func summary(v deliveryView) deliveryView {
	v.Body, v.RespBody, v.Pretty = "", "", ""
	v.Headers, v.RespHeaders = nil, nil
	v.Diagnosis = v.Diagnosis[:min(1, len(v.Diagnosis))]
	return v
}

func (rt *Runtime) registerWebhooks(pro func(string, apiFunc)) {
	pro("GET /api/pro/webhooks", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		wr, _, err := rt.discoverWebhooks(r.Context(), s, r.URL.Query().Get("refresh") == "1")
		if err != nil {
			return nil, err
		}
		cfg, _ := config.Load()
		if cfg.Webhooks[s.Name] {
			_, list, _ := core.Sites()
			rt.applyWebhookWatch(cfg, list)
		}
		var link *share.Link
		if l, ok := rt.shares.Get(s.Name); ok {
			link = &l
		}
		var samples []webhook.Sample
		seen := map[string]bool{}
		for _, f := range wr.Routes {
			if !seen[f.Provider] {
				seen[f.Provider] = true
				samples = append(samples, webhook.Samples(f.Provider)...)
			}
		}
		if !seen[""] {
			samples = append(samples, webhook.Samples("")...)
		}
		return map[string]any{"routes": wr.Routes, "laravel11": wr.Laravel11, "watching": cfg.Webhooks[s.Name],
			"custom": cfg.WebhookPaths[s.Name], "link": link, "samples": samples, "url": s.URL}, nil
	})
	pro("POST /api/pro/webhooks/watch", rt.mutating(func(b body) error {
		s, err := findSite(b.str("site"))
		if err != nil {
			return err
		}
		return core.Mutate(func(c *config.Config) error {
			if b.bool("on") {
				c.Webhooks[s.Name] = true
			} else {
				delete(c.Webhooks, s.Name)
			}
			return nil
		})
	}))
	pro("POST /api/pro/webhooks/paths", rt.mutating(func(b body) error {
		s, err := findSite(b.str("site"))
		if err != nil {
			return err
		}
		p := "/" + strings.Trim(b.str("path"), "/ ")
		if p == "/" || strings.ContainsAny(p, " \"?#") {
			return errors.New("enter a path like /payments/callback")
		}
		return core.Mutate(func(c *config.Config) error {
			if c.WebhookPaths == nil {
				c.WebhookPaths = map[string][]string{}
			}
			list := slices.DeleteFunc(slices.Clone(c.WebhookPaths[s.Name]), func(x string) bool { return x == p })
			if !b.bool("remove") {
				list = append(list, p)
				c.Webhooks[s.Name] = true
			}
			if len(list) == 0 {
				delete(c.WebhookPaths, s.Name)
			} else {
				c.WebhookPaths[s.Name] = list
			}
			return nil
		})
	}))
	pro("GET /api/pro/webhooks/deliveries", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		wr := readWebhookRoutes(s.Path)
		env := core.EnvValues(s.Path)
		out := []deliveryView{}
		for _, d := range readDeliveries(deliveriesFile(s.Path), 200) {
			out = append(out, summary(analyze(d, wr, env)))
		}
		return out, nil
	})
	pro("GET /api/pro/webhooks/delivery", func(r *http.Request) (any, error) {
		s, err := findSite(r.URL.Query().Get("site"))
		if err != nil {
			return nil, err
		}
		id := r.URL.Query().Get("id")
		for _, d := range readDeliveries(deliveriesFile(s.Path), 0) {
			if d.ID == id {
				return analyze(d, readWebhookRoutes(s.Path), core.EnvValues(s.Path)), nil
			}
		}
		return nil, errors.New("that delivery is no longer kept")
	})
	pro("POST /api/pro/webhooks/clear", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		deliveryMu.Lock()
		defer deliveryMu.Unlock()
		if err := os.Remove(deliveriesFile(s.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
	// Replay sends a recorded delivery again to the local site, optionally
	// with a fresh signature (Stripe and others refuse old timestamps).
	pro("POST /api/pro/webhooks/replay", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		var d *server.Delivery
		for _, x := range readDeliveries(deliveriesFile(s.Path), 0) {
			if x.ID == b.str("id") {
				d = &x
				break
			}
		}
		if d == nil {
			return nil, errors.New("that delivery is no longer kept")
		}
		h := http.Header(d.Headers).Clone()
		// What the tunnel added isn't part of the delivery.
		for _, k := range []string{"Content-Length", "Host", "Cf-Connecting-Ip", "Cf-Ray", "Cf-Ipcountry", "Cf-Visitor", "Cf-Warp-Tag-Id", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", server.SendHeader} {
			h.Del(k)
		}
		resigned := false
		if b.bool("resign") {
			env := core.EnvValues(s.Path)
			hint := ""
			for _, f := range readWebhookRoutes(s.Path).Routes {
				if webhook.Match(f.URI, d.Path) {
					hint = f.Provider
				}
			}
			if p, _ := webhook.Identify(h, []byte(d.Body), hint); p.ID != "" {
				resigned = webhook.Sign(p, h, []byte(d.Body), env, time.Now())
			}
		}
		target := d.Path
		if d.Query != "" {
			target += "?" + d.Query
		}
		status, err := sendToSite(r.Context(), s, d.Method, target, h, []byte(d.Body), "replay")
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": status, "resigned": resigned}, nil
	})
	pro("POST /api/pro/webhooks/send", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		s, err := findSite(b.str("site"))
		if err != nil {
			return nil, err
		}
		path := "/" + strings.TrimLeft(b.str("path"), "/")
		if strings.Contains(path, "{") {
			return nil, errors.New("fill in the route's parameters first, e.g. /webhooks/stripe instead of /webhooks/{provider}")
		}
		env := core.EnvValues(s.Path)
		body, h, signed, err := webhook.Build(b.str("provider"), b.str("event"), env, time.Now())
		if err != nil {
			return nil, err
		}
		if raw := b.str("body"); raw != "" {
			if !json.Valid([]byte(raw)) {
				return nil, errors.New("the body isn't valid JSON")
			}
			body = []byte(raw)
			if p, ok := webhook.Get(b.str("provider")); ok {
				signed = webhook.Sign(p, h, body, env, time.Now())
			}
		}
		status, err := sendToSite(r.Context(), s, http.MethodPost, path, h, body, "test")
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": status, "signed": signed}, nil
	})
	pro("GET /api/pro/webhooks/sample", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		body, _, _, err := webhook.Build(q.Get("provider"), q.Get("event"), nil, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]string{"body": string(body)}, nil
	})
}

// sendToSite posts to a site through LaraMonster's own web server, so the
// request is recorded like a real delivery.
func sendToSite(ctx context.Context, s sites.Site, method, target string, h http.Header, body []byte, via string) (int, error) {
	if !s.Enabled {
		return 0, errors.New(s.Name + " is switched off")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.URL, "/")+target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header = h
	req.Header.Set(server.SendHeader, via)
	// Straight to LaraMonster's web server: the site's name may only
	// resolve in browsers (*.localhost) or through LaraMonster's DNS.
	port := req.URL.Port()
	if port == "" {
		port = map[bool]string{true: "443", false: "80"}[req.URL.Scheme == "https"]
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // our own local CA
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+port)
			},
		},
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the site didn't answer: %w", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode, nil
}
