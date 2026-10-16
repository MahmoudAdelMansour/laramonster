// Package server runs an embedded Caddy that serves every site.
package server

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"

	"laramonster/internal/config"
	"laramonster/internal/paths"
	"laramonster/internal/sites"
)

// Hosts for LaraMonster's own pages. Site names can't contain dots, so these
// never collide with a project folder (even one called "laramonster").
const (
	DashboardName = "dashboard.laramonster"
	MailName      = "mail.laramonster"
)

// Proxy is an extra host that is reverse-proxied (dashboard, Mailpit UI…).
type Proxy struct {
	Name     string            // host label, e.g. "mailpit"
	Upstream string            // e.g. "127.0.0.1:8025" or "unix//path.sock"
	Headers  map[string]string // added to proxied requests (the engine token on Windows)
}

// Caddyfile renders the Caddy config.
// upstreams gives a site's FastCGI addresses (by its PHP version and name).
func Caddyfile(cfg *config.Config, list []sites.Site, upstreams func(version, site string) []string, proxies []Proxy) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{
	admin off
	persist_config off
	auto_https disable_redirects
	skip_install_trust
	http_port %d
	https_port %d
	storage file_system %s
	log {
		output file %s {
			roll_size 10MiB
			roll_keep 3
		}
	}
	pki {
		ca local {
			name "LaraMonster Local CA"
			root_cn "LaraMonster Root CA"
			intermediate_cn "LaraMonster Intermediate CA"
		}
	}
}

`, cfg.HTTPPort, cfg.HTTPSPort, quote(paths.Data()), quote(filepath.Join(paths.Logs(), "caddy.log")))

	for _, p := range proxies {
		if len(p.Headers) == 0 {
			fmt.Fprintf(&b, "http://%s {\n\treverse_proxy %s\n}\n\n", host(cfg, p.Name, cfg.HTTPPort), p.Upstream)
			continue
		}
		fmt.Fprintf(&b, "http://%s {\n\treverse_proxy %s {\n", host(cfg, p.Name, cfg.HTTPPort), p.Upstream)
		keys := make([]string, 0, len(p.Headers))
		for k := range p.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "\t\theader_up %s %s\n", k, quote(p.Headers[k]))
		}
		b.WriteString("\t}\n}\n\n")
	}

	for _, s := range list {
		httpHost := host(cfg, s.Name, cfg.HTTPPort)
		if s.Secure {
			fmt.Fprintf(&b, "http://%s {\n\tredir %s{uri} 308\n}\n\n", httpHost, s.URL)
			fmt.Fprintf(&b, "https://%s {\n\ttls internal\n", host(cfg, s.Name, cfg.HTTPSPort))
		} else {
			fmt.Fprintf(&b, "http://%s {\n", httpHost)
		}
		if s.Enabled {
			fmt.Fprintf(&b, "\tlaramonster_count %s\n", quote(s.Name))
			fmt.Fprintf(&b, "\tlaramonster_hooks %s\n", quote(s.Name))
		}
		switch {
		case !s.Enabled:
			fmt.Fprintf(&b, "\trespond %s 503\n}\n\n", quote(s.Name+" is switched off in LaraMonster. Turn it on from the Sites page."))
			continue
		case s.Proxy != "":
			b.WriteString(proxyBlock(s.Proxy))
			b.WriteString("}\n\n")
			continue
		}
		fmt.Fprintf(&b, "\troot * %s\n", quote(s.Root))
		b.WriteString("\tencode zstd gzip\n")
		if ups := upstreams(s.PHP, s.Name); s.PHP != "" && len(ups) > 0 {
			fmt.Fprintf(&b, "\tphp_fastcgi")
			for _, u := range ups {
				fmt.Fprintf(&b, " %s", quote(u))
			}
			b.WriteString("\n")
		}
		b.WriteString("\tfile_server\n}\n\n")
	}

	fmt.Fprintf(&b, "http://:%d {\n\trespond \"LaraMonster: no site is configured for {host}\" 404\n}\n", cfg.HTTPPort)
	return b.String()
}

// proxyBlock forwards a site to the user's own server (Node, php -S,
// artisan serve, Apache…). Local addresses keep the site's Host header, so
// apps build links with the pretty domain. A named target (an Apache
// virtual host such as http://shop.test) gets its own name as Host, which
// is what name-based virtual hosts match on.
func proxyBlock(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return "\trespond \"invalid server address\" 502\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\treverse_proxy %s {\n", quote(u.Scheme+"://"+u.Host))
	if !isLocal(u.Hostname()) {
		b.WriteString("\t\theader_up Host {upstream_hostport}\n")
	}
	if u.Scheme == "https" {
		// Local servers use self-signed certificates.
		b.WriteString("\t\ttransport http {\n\t\t\ttls_insecure_skip_verify\n\t\t}\n")
	}
	b.WriteString("\t}\n")
	return b.String()
}

func isLocal(h string) bool {
	return h == "localhost" || net.ParseIP(h) != nil
}

func host(cfg *config.Config, name string, port int) string {
	return fmt.Sprintf("%s.%s:%d", name, cfg.TLD, port)
}

// Apply loads (or hot-reloads) the given Caddyfile.
func Apply(caddyfile string) error {
	adapter := caddyconfig.GetAdapter("caddyfile")
	cfgJSON, _, err := adapter.Adapt([]byte(caddyfile), nil)
	if err != nil {
		return fmt.Errorf("caddyfile: %w", err)
	}
	return caddy.Load(cfgJSON, true)
}

func Stop() error { return caddy.Stop() }

func quote(s string) string { return strconv.Quote(s) }
