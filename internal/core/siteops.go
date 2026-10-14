package core

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"laramonster/internal/config"
	"laramonster/internal/sites"
)

// SetEnabled switches a site on or off. An off site keeps its settings.
func SetEnabled(name string, on bool) error {
	if err := requireSite(name); err != nil {
		return err
	}
	return Mutate(func(c *config.Config) error {
		if on {
			delete(c.Disabled, name)
		} else {
			c.Disabled[name] = true
		}
		return nil
	})
}

// SetEnabledMany switches several sites at once (one config write).
func SetEnabledMany(names []string, on bool) error {
	if len(names) == 0 {
		return errors.New("no sites given")
	}
	_, list, err := Sites()
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, ok := sites.Find(list, n); !ok {
			return fmt.Errorf("no site named %q", n)
		}
	}
	return Mutate(func(c *config.Config) error {
		for _, n := range names {
			if on {
				delete(c.Disabled, n)
			} else {
				c.Disabled[n] = true
			}
		}
		return nil
	})
}

// SetProxy makes the user's own server (Node, php -S, artisan serve,
// Apache…) answer for a site. An empty target goes back to LaraMonster's PHP.
func SetProxy(name, target string) error {
	if err := requireSite(name); err != nil {
		return err
	}
	target = strings.TrimSpace(target)
	if target != "" {
		norm, err := NormalizeProxy(target)
		if err != nil {
			return err
		}
		target = norm
	}
	return Mutate(func(c *config.Config) error {
		if target == "" {
			delete(c.Proxies, name)
		} else {
			c.Proxies[name] = target
		}
		return nil
	})
}

// NormalizeProxy accepts "3000", "localhost:3000" or a full URL and returns
// a scheme://host[:port] URL.
func NormalizeProxy(target string) (string, error) {
	if !strings.Contains(target, "://") {
		if !strings.Contains(target, ":") && !strings.Contains(target, ".") {
			target = "127.0.0.1:" + target // just a port
		}
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%q is not a server address; use something like http://127.0.0.1:3000", target)
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("the server address can't contain a path, only http(s)://host:port")
	}
	return u.Scheme + "://" + u.Host, nil
}

// RenameResult tells the UI what else changed with the site's address.
type RenameResult struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	AppURLUpdate bool   `json:"app_url_updated"` // .env APP_URL followed the rename
}

// Rename gives a site a new address (name.<tld>). Its settings move along,
// and a Laravel APP_URL that pointed at the old address is updated.
func Rename(name, to string) (RenameResult, error) {
	to = sites.Slug(to)
	if to == "" {
		return RenameResult{}, errors.New("use letters, numbers and dashes for the address")
	}
	cfg, list, err := Sites()
	if err != nil {
		return RenameResult{}, err
	}
	site, ok := sites.Find(list, name)
	if !ok {
		return RenameResult{}, fmt.Errorf("no site named %q", name)
	}
	if to == name {
		return RenameResult{Name: name, URL: site.URL}, nil
	}
	if _, taken := sites.Find(list, to); taken {
		return RenameResult{}, fmt.Errorf("%s.%s is already used by another site", to, cfg.TLD)
	}
	err = Mutate(func(c *config.Config) error {
		delete(c.Links, name)
		// Back to its folder name inside a parked directory: no link needed.
		parked := slices.Contains(c.Paths, filepath.Dir(site.Path)) && sites.Slug(filepath.Base(site.Path)) == to
		if !parked {
			c.Links[to] = site.Path
		}
		moveKey(c.Isolated, name, to)
		moveKey(c.Secured, name, to)
		moveKey(c.Disabled, name, to)
		moveKey(c.Proxies, name, to)
		moveKey(c.Tracking, name, to)
		return nil
	})
	if err != nil {
		return RenameResult{}, err
	}
	cfg, _ = config.Load()
	res := RenameResult{Name: to, URL: sites.URL(cfg, to)}
	// Keep Laravel's APP_URL in step when it pointed at the old address.
	if content, err := os.ReadFile(EnvPath(site.Path)); err == nil {
		if v, ok := EnvValue(string(content), "APP_URL"); ok && strings.TrimRight(v, "/") == site.URL {
			if err := WriteEnv(site.Path, SetEnvValue(string(content), "APP_URL", res.URL)); err == nil {
				res.AppURLUpdate = true
			}
		}
	}
	return res, nil
}

func moveKey[V any](m map[string]V, from, to string) {
	if v, ok := m[from]; ok {
		m[to] = v
		delete(m, from)
	}
}
