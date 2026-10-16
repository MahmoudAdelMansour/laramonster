package server

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// Hooks is a Caddy handler that records incoming webhook requests of a site
// (Webhooks, Pro): only paths the engine asked for (WatchWebhooks), with the
// app's answer. It runs in every site block and does nothing otherwise.
type Hooks struct {
	Site string `json:"site"`
	Via  string `json:"via,omitempty"` // "public" in a public link's block
}

// Delivery is one recorded webhook request and its answer.
type Delivery struct {
	ID          string              `json:"id"`
	Site        string              `json:"site"`
	Via         string              `json:"via"` // local, public, replay, test
	At          time.Time           `json:"at"`
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Query       string              `json:"query,omitempty"`
	Headers     map[string][]string `json:"headers"`
	Body        string              `json:"body"`
	BodySize    int64               `json:"body_size"`
	Truncated   bool                `json:"truncated,omitempty"`
	RemoteIP    string              `json:"remote_ip,omitempty"`
	Status      int                 `json:"status"`
	RespHeaders map[string][]string `json:"resp_headers,omitempty"`
	RespBody    string              `json:"resp_body,omitempty"`
	MS          float64             `json:"ms"`
}

// SendHeader marks requests LaraMonster sends itself (replay, test).
const SendHeader = "X-Laramonster-Send"

const (
	maxHookBody = 1 << 20
	maxHookResp = 64 << 10
)

var (
	hooksMu    sync.RWMutex
	hookWatch  = map[string][]*regexp.Regexp{}
	hookRecord func(Delivery)
	hookSeq    struct {
		sync.Mutex
		n uint64
	}
)

// WatchWebhooks sets the path patterns recorded for each site (nil: none)
// and where deliveries go.
func WatchWebhooks(watch map[string][]*regexp.Regexp, record func(Delivery)) {
	hooksMu.Lock()
	hookWatch, hookRecord = watch, record
	hooksMu.Unlock()
}

func watched(site, path string) (func(Delivery), bool) {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	if hookRecord == nil {
		return nil, false
	}
	for _, re := range hookWatch[site] {
		if re.MatchString(path) {
			return hookRecord, true
		}
	}
	return nil, false
}

func init() {
	caddy.RegisterModule(Hooks{})
	httpcaddyfile.RegisterHandlerDirective("laramonster_hooks", func(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
		c := new(Hooks)
		h.Next()
		if !h.NextArg() {
			return nil, h.ArgErr()
		}
		c.Site = h.Val()
		if h.NextArg() {
			c.Via = h.Val()
		}
		return c, nil
	})
	httpcaddyfile.RegisterDirectiveOrder("laramonster_hooks", httpcaddyfile.Before, "tracing")
}

func (Hooks) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.laramonster_hooks", New: func() caddy.Module { return new(Hooks) }}
}

func (hk Hooks) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	record, ok := watched(hk.Site, r.URL.Path)
	if !ok {
		return next.ServeHTTP(w, r)
	}
	start := time.Now()
	d := Delivery{Site: hk.Site, Via: "local", At: start, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Headers: r.Header.Clone(), BodySize: r.ContentLength}
	if hk.Via != "" {
		d.Via = hk.Via
	}
	if v := strings.ToLower(r.Header.Get(SendHeader)); v == "replay" || v == "test" {
		d.Via = v
	}
	d.RemoteIP = r.Header.Get("Cf-Connecting-Ip")
	if d.RemoteIP == "" {
		d.RemoteIP, _, _ = strings.Cut(r.RemoteAddr, ":")
	}
	if r.Body != nil {
		buf, _ := io.ReadAll(io.LimitReader(r.Body, maxHookBody+1))
		if len(buf) > maxHookBody {
			d.Truncated = true
			d.Body = string(buf[:maxHookBody])
		} else {
			d.Body = string(buf)
		}
		if d.BodySize < 0 || !d.Truncated {
			d.BodySize = int64(len(buf))
		}
		r.Body = readCloser{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
	}
	rec := &hookWriter{ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w}}
	err := next.ServeHTTP(rec, r)
	d.Status = rec.status
	if d.Status == 0 && err == nil {
		d.Status = http.StatusOK
	}
	if he, ok := err.(caddyhttp.HandlerError); ok && d.Status == 0 {
		d.Status = he.StatusCode
	}
	d.RespHeaders = w.Header().Clone()
	d.RespBody = rec.body.String()
	d.MS = float64(time.Since(start).Microseconds()) / 1000
	hookSeq.Lock()
	hookSeq.n++
	d.ID = start.Format("20060102150405.000000") + "-" + strconv.FormatUint(hookSeq.n, 10)
	hookSeq.Unlock()
	go record(d)
	return err
}

type readCloser struct {
	io.Reader
	io.Closer
}

// hookWriter passes the answer through and keeps its status and the start
// of its body.
type hookWriter struct {
	*caddyhttp.ResponseWriterWrapper
	status int
	body   bytes.Buffer
}

func (h *hookWriter) WriteHeader(code int) {
	if h.status == 0 {
		h.status = code
	}
	h.ResponseWriterWrapper.WriteHeader(code)
}

func (h *hookWriter) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.status = http.StatusOK
	}
	if room := maxHookResp - h.body.Len(); room > 0 {
		h.body.Write(b[:min(len(b), room)])
	}
	return h.ResponseWriterWrapper.Write(b)
}

var (
	_ caddy.Module                = (*Hooks)(nil)
	_ caddyhttp.MiddlewareHandler = (*Hooks)(nil)
)
