// Package config loads and saves laramonster's config.json.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"

	"laramonster/internal/paths"
)

type Config struct {
	TLD       string            `json:"tld"`
	HTTPPort  int               `json:"http_port"`
	HTTPSPort int               `json:"https_port"`
	DNSPort   int               `json:"dns_port"` // built-in resolver for *.<tld>, 0 = off
	PHP       string            `json:"php"`      // default PHP version, e.g. "8.4"; empty = newest installed
	Paths     []string          `json:"paths"`    // parked directories: every subfolder becomes a site
	Links     map[string]string `json:"links"`    // site name -> directory
	Isolated  map[string]string `json:"isolated"` // site name -> PHP version
	Secured   map[string]bool   `json:"secured"`  // site name -> served over HTTPS
	Disabled  map[string]bool   `json:"disabled"` // site name -> switched off
	Proxies   map[string]string `json:"proxies"`  // site name -> URL of the user's own server
	Tracking  map[string]bool   `json:"tracking"` // site name -> Event Tracker records it (Pro)
	// Debugging: site name -> the Debugger records its dumps, queries, logs
	// and exceptions. DebugQuiet keeps dumps out of the page (only in
	// LaraMonster).
	Debugging  map[string]bool `json:"debugging,omitempty"`
	DebugQuiet map[string]bool `json:"debug_quiet,omitempty"`
	// Webhooks: site name -> incoming webhook requests are recorded (Pro).
	Webhooks map[string]bool `json:"webhooks,omitempty"`
	// WebhookPaths: extra paths to record per site, added by the person.
	WebhookPaths map[string][]string `json:"webhook_paths,omitempty"`
	// PHPExtensions turns extensions on (true) or off (false) per PHP
	// version, on top of what that PHP's own php.ini loads.
	PHPExtensions map[string]map[string]bool `json:"php_extensions,omitempty"`
	Services      map[string]*ServiceConfig  `json:"services"`
	Editor        string                     `json:"editor"` // command used by "open in editor", e.g. "code"
	// PHPSettings are php.ini settings by scope: "global", a version ("8.4")
	// or "site:<name>". Later scopes win: global < version < site.
	PHPSettings map[string]map[string]string `json:"php_settings,omitempty"`
	// BrowserDashboard also serves the UI at dashboard.laramonster.<tld>.
	// Off by default: the desktop app doesn't need it.
	BrowserDashboard bool `json:"browser_dashboard"`
	Onboarded        bool `json:"onboarded"` // the first-run setup was finished or skipped
	// SeenVersion is the newest version whose "What's new" was shown.
	SeenVersion string `json:"seen_version,omitempty"`
	// ManualUpdates turns off installing updates by themselves: the app
	// only says a new version is out. Off (updates on) by default.
	ManualUpdates bool `json:"manual_updates"`
}

type ServiceConfig struct {
	Enabled bool `json:"enabled"` // start together with LaraMonster
	Port    int  `json:"port"`
}

func Default() *Config {
	return &Config{
		// *.localhost resolves to 127.0.0.1 in browsers and curl without any
		// DNS setup. Switch to "test" once laramonster manages DNS.
		TLD:       "localhost",
		HTTPPort:  8080,
		HTTPSPort: 8443,
		DNSPort:   5354,
		Links:     map[string]string{},
		Isolated:  map[string]string{},
		Secured:   map[string]bool{},
		Disabled:  map[string]bool{},
		Proxies:   map[string]string{},
		Tracking:  map[string]bool{},
		Services:  map[string]*ServiceConfig{},
		Editor:    "code",
	}
}

func Load() (*Config, error) {
	c := Default()
	b, err := os.ReadFile(paths.Config())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.Links == nil {
		c.Links = map[string]string{}
	}
	if c.Isolated == nil {
		c.Isolated = map[string]string{}
	}
	if c.Secured == nil {
		c.Secured = map[string]bool{}
	}
	if c.Services == nil {
		c.Services = map[string]*ServiceConfig{}
	}
	if c.Disabled == nil {
		c.Disabled = map[string]bool{}
	}
	if c.Proxies == nil {
		c.Proxies = map[string]string{}
	}
	if c.Tracking == nil {
		c.Tracking = map[string]bool{}
	}
	if c.Debugging == nil {
		c.Debugging = map[string]bool{}
	}
	if c.DebugQuiet == nil {
		c.DebugQuiet = map[string]bool{}
	}
	if c.Webhooks == nil {
		c.Webhooks = map[string]bool{}
	}
	if c.PHPExtensions == nil {
		c.PHPExtensions = map[string]map[string]bool{}
	}
	return c, nil
}

// Save writes the config atomically.
func (c *Config) Save() error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := paths.Config() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, paths.Config())
}

func (c *Config) AddPath(dir string) bool {
	if slices.Contains(c.Paths, dir) {
		return false
	}
	c.Paths = append(c.Paths, dir)
	return true
}

func (c *Config) RemovePath(dir string) bool {
	i := slices.Index(c.Paths, dir)
	if i < 0 {
		return false
	}
	c.Paths = slices.Delete(c.Paths, i, i+1)
	return true
}

// Service returns the config for a service, creating it on first use.
func (c *Config) Service(name string, defaultPort int) *ServiceConfig {
	sc, ok := c.Services[name]
	if !ok || sc == nil {
		sc = &ServiceConfig{Port: defaultPort}
		c.Services[name] = sc
	}
	if sc.Port == 0 {
		sc.Port = defaultPort
	}
	return sc
}
