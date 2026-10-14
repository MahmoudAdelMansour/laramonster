// Package core holds LaraMonster's operations, shared by the CLI, the daemon
// API and the desktop app.
package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"laramonster/internal/config"
	"laramonster/internal/php"
	"laramonster/internal/server"
	"laramonster/internal/sites"
)

// Version is set at build time with -ldflags "-X laramonster/internal/core.Version=…".
var Version = "0.1.0-dev"

// Mutate loads the config, applies fn and saves it.
func Mutate(fn func(*config.Config) error) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if err := fn(c); err != nil {
		return err
	}
	return c.Save()
}

// Sites resolves the current site list.
func Sites() (*config.Config, []sites.Site, error) {
	c, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	return c, sites.List(c, php.Default(c.PHP, php.Discover())), nil
}

func absDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

func Park(dir string) (string, error) {
	abs, err := absDir(dir)
	if err != nil {
		return "", err
	}
	return abs, Mutate(func(c *config.Config) error {
		if !c.AddPath(abs) {
			return fmt.Errorf("%s is already parked", abs)
		}
		return nil
	})
}

func Forget(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, Mutate(func(c *config.Config) error {
		if !c.RemovePath(abs) {
			return fmt.Errorf("%s is not parked", abs)
		}
		return nil
	})
}

// Link serves dir as name (defaults to the folder name). Returns the site name.
func Link(name, dir string) (string, error) {
	abs, err := absDir(dir)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	name = sites.Slug(name)
	if name == "" {
		return "", errors.New("invalid site name")
	}
	return name, Mutate(func(c *config.Config) error {
		c.Links[name] = abs
		return nil
	})
}

func Unlink(name string) error {
	return Mutate(func(c *config.Config) error {
		if _, ok := c.Links[name]; !ok {
			return fmt.Errorf("%s is not a linked site", name)
		}
		delete(c.Links, name)
		delete(c.Isolated, name)
		delete(c.Secured, name)
		delete(c.Disabled, name)
		delete(c.Proxies, name)
		delete(c.Tracking, name)
		return nil
	})
}

func requireSite(name string) error {
	_, list, err := Sites()
	if err != nil {
		return err
	}
	if _, ok := sites.Find(list, name); !ok {
		return fmt.Errorf("no site named %q", name)
	}
	return nil
}

func Secure(name string, on bool) error {
	if err := requireSite(name); err != nil {
		return err
	}
	return Mutate(func(c *config.Config) error {
		if on {
			c.Secured[name] = true
		} else {
			delete(c.Secured, name)
		}
		return nil
	})
}

// Isolate pins a site to a PHP version; an empty version un-pins it.
func Isolate(name, version string) error {
	if err := requireSite(name); err != nil {
		return err
	}
	if version != "" {
		inst, err := RequirePHP(version)
		if err != nil {
			return err
		}
		version = inst.Version
	}
	return Mutate(func(c *config.Config) error {
		if version == "" {
			delete(c.Isolated, name)
		} else {
			c.Isolated[name] = version
		}
		return nil
	})
}

func SetDefaultPHP(version string) (php.Install, error) {
	inst, err := RequirePHP(version)
	if err != nil {
		return inst, err
	}
	return inst, Mutate(func(c *config.Config) error {
		c.PHP = inst.Version
		return nil
	})
}

func RequirePHP(v string) (php.Install, error) {
	installs := php.Discover()
	if inst, ok := php.Find(installs, v); ok {
		return inst, nil
	}
	var have []string
	for _, i := range installs {
		have = append(have, i.Version)
	}
	if len(have) == 0 {
		have = []string{"none"}
	}
	return php.Install{}, fmt.Errorf("PHP %s is not installed (installed: %s); install it with `laramonster php:install %s`",
		php.Normalize(v), strings.Join(have, ", "), php.Normalize(v))
}

// Settings are the user-editable global options.
type Settings struct {
	TLD       *string `json:"tld"`
	HTTPPort  *int    `json:"http_port"`
	HTTPSPort *int    `json:"https_port"`
	DNSPort   *int    `json:"dns_port"`
	Editor    *string `json:"editor"`

	BrowserDashboard *bool `json:"browser_dashboard"`
	AutoUpdate       *bool `json:"auto_update"`
}

func UpdateSettings(s Settings) error {
	return Mutate(func(c *config.Config) error {
		if s.TLD != nil {
			tld := sites.Slug(strings.TrimPrefix(*s.TLD, "."))
			if tld == "" {
				return errors.New("invalid TLD")
			}
			c.TLD = tld
		}
		for _, p := range []struct {
			v   *int
			dst *int
		}{{s.HTTPPort, &c.HTTPPort}, {s.HTTPSPort, &c.HTTPSPort}, {s.DNSPort, &c.DNSPort}} {
			if p.v == nil {
				continue
			}
			if *p.v < 0 || *p.v > 65535 {
				return fmt.Errorf("invalid port %d", *p.v)
			}
			*p.dst = *p.v
		}
		if c.HTTPPort == 0 || c.HTTPSPort == 0 || c.HTTPPort == c.HTTPSPort {
			return errors.New("HTTP and HTTPS ports must be set and different")
		}
		if s.Editor != nil {
			c.Editor = strings.TrimSpace(*s.Editor)
		}
		if s.BrowserDashboard != nil {
			c.BrowserDashboard = *s.BrowserDashboard
		}
		if s.AutoUpdate != nil {
			c.ManualUpdates = !*s.AutoUpdate
		}
		return nil
	})
}

// PHPFor returns the PHP install that a directory should use: the PHP of
// the site containing it, else the default.
func PHPFor(dir string) (php.Install, error) {
	c, err := config.Load()
	if err != nil {
		return php.Install{}, err
	}
	installs := php.Discover()
	version := php.Default(c.PHP, installs)
	list := sites.List(c, version)
	if s, ok := sites.ForPath(list, dir); ok && s.PHP != "" {
		version = s.PHP
	}
	inst, ok := php.Find(installs, version)
	if !ok {
		return inst, errors.New("no PHP installed: run `laramonster php:install 8.4`")
	}
	if inst.CLI == "" {
		return inst, fmt.Errorf("PHP %s has no CLI binary next to %s", inst.Version, inst.FPM)
	}
	return inst, nil
}

// Open opens a URL or path with the desktop's default handler.
func Open(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

// OpenEditor opens a directory in the configured editor.
func OpenEditor(dir string) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	editor := c.Editor
	if editor == "" {
		editor = "code"
	}
	parts := strings.Fields(editor)
	bin, err := exec.LookPath(parts[0])
	if err != nil {
		return fmt.Errorf("editor %q not found, change it in Settings", parts[0])
	}
	return exec.Command(bin, append(parts[1:], dir)...).Start()
}

// DashboardURL is where the browser dashboard is served, or "" when it is off.
func DashboardURL(c *config.Config) string {
	if !c.BrowserDashboard {
		return ""
	}
	return sites.URL(c, server.DashboardName)
}

// HostNames lists every host LaraMonster serves (for hosts-file DNS).
func HostNames(tld string, list []sites.Site) []string {
	var hosts []string
	for _, s := range list {
		hosts = append(hosts, s.Name+"."+tld)
	}
	return append(hosts, server.DashboardName+"."+tld, server.MailName+"."+tld)
}
