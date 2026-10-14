package server

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// Counter is a Caddy handler that counts each site's requests and keeps the
// ones in progress, for the Monitor page. It runs first in every site.
type Counter struct {
	Site string `json:"site"`
}

func init() {
	caddy.RegisterModule(Counter{})
	httpcaddyfile.RegisterHandlerDirective("laramonster_count", func(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
		c := new(Counter)
		h.Next() // the directive name
		if !h.NextArg() {
			return nil, h.ArgErr()
		}
		c.Site = h.Val()
		return c, nil
	})
	httpcaddyfile.RegisterDirectiveOrder("laramonster_count", httpcaddyfile.Before, "tracing")
}

func (Counter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.laramonster_count", New: func() caddy.Module { return new(Counter) }}
}

func (c Counter) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	s := trafficOf(c.Site)
	s.total.Add(1)
	id := s.seq.Add(1)
	s.mu.Lock()
	s.live[id] = inflight{start: time.Now(), uri: r.URL.RequestURI()}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.live, id)
		s.mu.Unlock()
	}()
	return next.ServeHTTP(w, r)
}

type inflight struct {
	start time.Time
	uri   string
}

type siteTraffic struct {
	total atomic.Uint64
	seq   atomic.Uint64
	mu    sync.Mutex
	live  map[uint64]inflight
}

var (
	trafficMu sync.Mutex
	traffic   = map[string]*siteTraffic{}
)

func trafficOf(site string) *siteTraffic {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	s, ok := traffic[site]
	if !ok {
		s = &siteTraffic{live: map[uint64]inflight{}}
		traffic[site] = s
	}
	return s
}

// Traffic is a site's request counter and its longest request in progress.
type Traffic struct {
	Total   uint64
	Running int
	Longest time.Duration
	URI     string // of the longest running request
}

// SiteTraffic snapshots every site's counters.
func SiteTraffic() map[string]Traffic {
	trafficMu.Lock()
	sites := make(map[string]*siteTraffic, len(traffic))
	for k, v := range traffic {
		sites[k] = v
	}
	trafficMu.Unlock()
	now := time.Now()
	out := make(map[string]Traffic, len(sites))
	for name, s := range sites {
		t := Traffic{Total: s.total.Load()}
		s.mu.Lock()
		t.Running = len(s.live)
		for _, f := range s.live {
			if d := now.Sub(f.start); d > t.Longest {
				t.Longest, t.URI = d, f.uri
			}
		}
		s.mu.Unlock()
		out[name] = t
	}
	return out
}

var (
	_ caddy.Module                = (*Counter)(nil)
	_ caddyhttp.MiddlewareHandler = (*Counter)(nil)
)
