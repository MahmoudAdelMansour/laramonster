// Package sites turns parked paths and links into a list of served sites.
package sites

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"laramonster/internal/config"
)

type Site struct {
	Name    string `json:"name"`
	Path    string `json:"path"` // project directory
	Root    string `json:"root"` // document root (public/ when present)
	PHP     string `json:"php"`
	URL     string `json:"url"`
	Linked  bool   `json:"linked"`
	Secure  bool   `json:"secure"`
	Laravel bool   `json:"laravel"`
	Enabled bool   `json:"enabled"`
	Proxy   string `json:"proxy"`  // served by the user's own server at this URL
	Health  string `json:"health"` // up, down or off; set by the engine
	Reason  string `json:"reason,omitempty"`
}

// List resolves every site. Links override parked folders of the same name.
func List(cfg *config.Config, defaultPHP string) []Site {
	byName := map[string]Site{}
	// A parked folder that is also linked (e.g. under a custom domain) is
	// served only under its linked name.
	linked := map[string]bool{}
	for _, p := range cfg.Links {
		linked[p] = true
	}
	for _, dir := range cfg.Paths {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if linked[p] {
				continue
			}
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() { // follows symlinks
				continue
			}
			name := Slug(e.Name())
			if name == "" {
				continue
			}
			byName[name] = Site{Name: name, Path: p}
		}
	}
	for name, p := range cfg.Links {
		byName[name] = Site{Name: name, Path: p, Linked: true}
	}

	out := make([]Site, 0, len(byName))
	for _, s := range byName {
		s.Root = docRoot(s.Path)
		s.PHP = defaultPHP
		if v, ok := cfg.Isolated[s.Name]; ok {
			s.PHP = v
		}
		s.Secure = cfg.Secured[s.Name]
		s.Enabled = !cfg.Disabled[s.Name]
		s.Proxy = cfg.Proxies[s.Name]
		s.URL = URL(cfg, s.Name)
		_, err := os.Stat(filepath.Join(s.Path, "artisan"))
		s.Laravel = err == nil
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Site) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ForDir finds the site whose project directory is dir.
func ForDir(list []Site, dir string) (Site, bool) {
	for _, s := range list {
		if s.Path == dir {
			return s, true
		}
	}
	return Site{}, false
}

func URL(cfg *config.Config, name string) string {
	host := name + "." + cfg.TLD
	if cfg.Secured[name] {
		if cfg.HTTPSPort != 443 {
			host += fmt.Sprintf(":%d", cfg.HTTPSPort)
		}
		return "https://" + host
	}
	if cfg.HTTPPort != 80 {
		host += fmt.Sprintf(":%d", cfg.HTTPPort)
	}
	return "http://" + host
}

// Find returns the site with the given name.
func Find(list []Site, name string) (Site, bool) {
	for _, s := range list {
		if s.Name == name {
			return s, true
		}
	}
	return Site{}, false
}

// ForPath finds the site containing dir (dir itself or any parent).
func ForPath(list []Site, dir string) (Site, bool) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if s, ok := ForDir(list, d); ok {
			return s, true
		}
		if filepath.Dir(d) == d {
			return Site{}, false
		}
	}
}

func docRoot(dir string) string {
	for _, sub := range []string{"public", "web", "public_html"} {
		p := filepath.Join(dir, sub)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return dir
}

// Slug turns a folder name into a valid hostname label.
func Slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
