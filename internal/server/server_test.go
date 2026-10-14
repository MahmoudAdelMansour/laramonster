package server

import (
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig"

	"laramonster/internal/config"
	"laramonster/internal/sites"
)

func TestCaddyfileAdapts(t *testing.T) {
	c := config.Default()
	c.Secured["blog"] = true
	list := []sites.Site{
		{Name: "blog", Root: "/srv/blog/public", PHP: "8.4", Secure: true, URL: "https://blog.localhost:8443", Enabled: true},
		{Name: "my shop", Root: "/srv/my shop", PHP: "8.3", Enabled: true},
		{Name: "static", Root: "/srv/static", Enabled: true},
	}
	ups := func(v string) []string { return []string{"unix//tmp/php" + v + ".sock"} }
	cf := Caddyfile(c, list, ups, []Proxy{{Name: DashboardName, Upstream: "unix//tmp/lm.sock"}})
	for _, want := range []string{"tls internal", "redir https://blog.localhost:8443{uri} 308", `"unix//tmp/php8.4.sock"`, "laramonster.localhost:8080"} {
		if !strings.Contains(cf, want) {
			t.Errorf("missing %q", want)
		}
	}
	if _, _, err := caddyconfig.GetAdapter("caddyfile").Adapt([]byte(cf), nil); err != nil {
		t.Fatalf("caddyfile does not adapt: %v\n%s", err, cf)
	}
}

func TestCaddyfileOffAndProxySites(t *testing.T) {
	c := config.Default()
	list := []sites.Site{
		{Name: "off", Root: "/srv/off", PHP: "8.4"},
		{Name: "node", Enabled: true, Proxy: "http://127.0.0.1:3000"},
		{Name: "apache", Enabled: true, Proxy: "https://shop.test"},
	}
	cf := Caddyfile(c, list, func(string) []string { return nil }, nil)
	for _, want := range []string{"is switched off", `laramonster_count "node"`, `reverse_proxy "http://127.0.0.1:3000"`, `reverse_proxy "https://shop.test"`, "header_up Host {upstream_hostport}", "tls_insecure_skip_verify"} {
		if !strings.Contains(cf, want) {
			t.Errorf("missing %q in\n%s", want, cf)
		}
	}
	if strings.Contains(cf, `laramonster_count "off"`) {
		t.Errorf("switched-off sites aren't counted:\n%s", cf)
	}
	if strings.Count(cf, "header_up Host") != 1 {
		t.Errorf("only the named target should rewrite Host:\n%s", cf)
	}
	j, _, err := caddyconfig.GetAdapter("caddyfile").Adapt([]byte(cf), nil)
	if err != nil {
		t.Fatalf("caddyfile does not adapt: %v\n%s", err, cf)
	}
	if !strings.Contains(string(j), `"handler":"laramonster_count","site":"node"`) {
		t.Errorf("counter handler not configured for the site:\n%s", j)
	}
}

func TestCaddyfileProxyHeaders(t *testing.T) {
	c := config.Default()
	cf := Caddyfile(c, nil, nil, []Proxy{{Name: DashboardName, Upstream: "127.0.0.1:5000", Headers: map[string]string{"X-LaraMonster-Token": "abc"}}})
	if !strings.Contains(cf, "reverse_proxy 127.0.0.1:5000 {\n\t\theader_up X-LaraMonster-Token \"abc\"\n\t}") {
		t.Errorf("token header missing from the dashboard proxy:\n%s", cf)
	}
}
