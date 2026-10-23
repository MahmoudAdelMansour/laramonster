// Package tools installs the things a PHP developer needs — Composer,
// Node.js, databases, Git, certificate tools — either as portable downloads
// managed by LaraMonster, or through the system package manager.
package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"laramonster/internal/core"
	"laramonster/internal/elevate"
	"laramonster/internal/fetch"
	"laramonster/internal/paths"
	"laramonster/internal/php"
	"laramonster/internal/proc"
	"laramonster/internal/services"
)

// Tool is one installable item in the catalog.
type Tool struct {
	ID          string
	Name        string
	Category    string
	Description string
	// Service links the tool to a service card (start/stop lives there).
	Service string
	// Page sends the UI elsewhere instead of installing (PHP has its own page).
	Page string
	// Detect returns the path of an existing install ("" if none).
	Detect func() string
	// Download installs a portable copy managed by LaraMonster (optional).
	Download func(progress fetch.Progress, out io.Writer) error
	// Packages maps a package manager to the package(s) to install.
	Packages map[string]string
}

// State is a tool as shown in the UI.
type State struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Service     string `json:"service,omitempty"`
	Page        string `json:"page,omitempty"`
	Installed   bool   `json:"installed"`
	Path        string `json:"path,omitempty"`
	Managed     bool   `json:"managed"`
	// Method is how Install will do it: "download", the package manager
	// name (e.g. "pacman"), or "" when it can't be installed automatically.
	Method string `json:"method"`
	Admin  bool   `json:"admin"` // needs a password prompt
}

// PackageManager is the detected system package manager.
type PackageManager struct {
	Name  string
	Admin bool // needs root
	cmd   func(pkgs string) string
}

var managers = []PackageManager{
	{"pacman", true, func(p string) string { return "pacman -S --needed --noconfirm " + p }},
	{"apt-get", true, func(p string) string {
		return "export DEBIAN_FRONTEND=noninteractive; apt-get update -q && apt-get install -y -q " + p
	}},
	{"dnf", true, func(p string) string { return "dnf install -y " + p }},
	{"zypper", true, func(p string) string { return "zypper --non-interactive install " + p }},
	{"brew", false, func(p string) string { return "brew install " + p }},
	{"winget", false, func(p string) string {
		var cmds []string
		for _, id := range strings.Fields(p) {
			cmds = append(cmds, "winget install --id "+id+" -e --silent --accept-package-agreements --accept-source-agreements")
		}
		return strings.Join(cmds, " && ")
	}},
	{"scoop", false, func(p string) string { return "scoop install " + p }},
}

// DetectManager returns the first package manager found on this system.
func DetectManager() (PackageManager, bool) {
	for _, m := range managers {
		if runtime.GOOS == "darwin" && m.Name != "brew" {
			continue
		}
		if runtime.GOOS == "windows" && m.Name != "winget" && m.Name != "scoop" {
			continue
		}
		if _, err := exec.LookPath(m.Name); err == nil {
			return m, true
		}
	}
	return PackageManager{}, false
}

func lookPath(names ...string) func() string {
	return func() string {
		for _, n := range names {
			if p, err := exec.LookPath(n); err == nil {
				return p
			}
		}
		return ""
	}
}

func serviceTool(name string) func() string {
	return func() string {
		d, _ := services.Get(name)
		return d.Find()
	}
}

func serviceDownload(name string) func(fetch.Progress, io.Writer) error {
	return func(progress fetch.Progress, _ io.Writer) error {
		d, _ := services.Get(name)
		return services.NewManager().Install(d, progress)
	}
}

// Catalog lists every tool, in display order.
var Catalog = []*Tool{
	{
		ID: "php", Name: "PHP", Category: "Runtime", Page: "php",
		Description: "Several PHP versions side by side, switchable per site.",
		Detect: func() string {
			if i := php.Discover(); len(i) > 0 {
				return i[0].CLI
			}
			return ""
		},
	},
	{
		ID: "composer", Name: "Composer", Category: "Runtime",
		Description: "PHP dependency manager. Runs with each site's PHP version.",
		Detect: func() string {
			if _, err := os.Stat(core.ComposerPhar()); err == nil {
				return core.ComposerPhar()
			}
			return lookPath("composer")()
		},
		Download: func(p fetch.Progress, _ io.Writer) error { return core.EnsureComposer(p) },
	},
	{
		ID: "node", Name: "Node.js (LTS)", Category: "Runtime",
		Description: "JavaScript runtime with npm and npx, for Vite and frontend builds.",
		Detect: func() string {
			if p := core.ManagedNode(); p != "" {
				return p
			}
			return lookPath("node")()
		},
		Download: installNode,
		Packages: map[string]string{"pacman": "nodejs npm", "apt-get": "nodejs npm", "dnf": "nodejs npm", "zypper": "nodejs npm", "brew": "node", "winget": "OpenJS.NodeJS.LTS", "scoop": "nodejs-lts"},
	},
	{
		ID: "postgres", Name: "PostgreSQL", Category: "Database", Service: "postgres",
		Description: "Advanced open-source SQL database.",
		Detect:      serviceTool("postgres"), Download: serviceDownload("postgres"),
		Packages: map[string]string{"pacman": "postgresql", "apt-get": "postgresql", "dnf": "postgresql-server", "zypper": "postgresql-server", "brew": "postgresql@17", "winget": "PostgreSQL.PostgreSQL.17"},
	},
	{
		ID: "mysql", Name: "MariaDB (MySQL)", Category: "Database", Service: "mysql",
		Description: "MySQL-compatible database, the Laravel classic.",
		Detect:      serviceTool("mysql"),
		Packages:    map[string]string{"pacman": "mariadb", "apt-get": "mariadb-server", "dnf": "mariadb-server", "zypper": "mariadb", "brew": "mariadb", "winget": "MariaDB.Server", "scoop": "mariadb"},
	},
	{
		ID: "redis", Name: "Redis / Valkey", Category: "Cache", Service: "redis",
		Description: "In-memory store for cache, queues and sessions.",
		Detect:      serviceTool("redis"),
		Packages:    map[string]string{"pacman": "valkey", "apt-get": "redis-server", "dnf": "valkey", "zypper": "redis", "brew": "redis", "scoop": "redis"},
	},
	{
		ID: "mailpit", Name: "Mailpit", Category: "Mail", Service: "mailpit",
		Description: "Catches every email your apps send, shown in the Mail inbox.",
		Detect:      serviceTool("mailpit"), Download: serviceDownload("mailpit"),
	},
	{
		ID: "meilisearch", Name: "Meilisearch", Category: "Search", Service: "meilisearch",
		Description: "Fast search engine for Laravel Scout.",
		Detect:      serviceTool("meilisearch"), Download: serviceDownload("meilisearch"),
	},
	{
		ID: "git", Name: "Git", Category: "Developer tools",
		Description: "Version control. New projects get a first commit.",
		Detect:      lookPath("git"),
		Packages:    map[string]string{"pacman": "git", "apt-get": "git", "dnf": "git", "zypper": "git", "brew": "git", "winget": "Git.Git", "scoop": "git"},
	},
	{
		ID: "nss", Name: "Browser certificate tools", Category: "Developer tools",
		Description: "certutil, so Firefox and Chrome trust LaraMonster's HTTPS certificates.",
		Detect:      lookPath("certutil"),
		Packages:    map[string]string{"pacman": "nss", "apt-get": "libnss3-tools", "dnf": "nss-tools", "zypper": "mozilla-nss-tools", "brew": "nss"},
	},
}

func Get(id string) (*Tool, bool) {
	i := slices.IndexFunc(Catalog, func(t *Tool) bool { return t.ID == id })
	if i < 0 {
		return nil, false
	}
	return Catalog[i], true
}

var cache struct {
	sync.Mutex
	at     time.Time
	states []State
}

// States reports every tool for this platform. Detection shells out (PHP
// probes every php-fpm binary), so results are cached for a short while.
func States() []State {
	cache.Lock()
	defer cache.Unlock()
	if time.Since(cache.at) < 20*time.Second && cache.states != nil {
		return slices.Clone(cache.states)
	}
	cache.states, cache.at = detectStates(), time.Now()
	return slices.Clone(cache.states)
}

// Invalidate forces the next States call to re-detect (after an install).
func Invalidate() {
	cache.Lock()
	cache.at = time.Time{}
	cache.Unlock()
}

func detectStates() []State {
	pm, hasPM := DetectManager()
	var out []State
	for _, t := range Catalog {
		if t.ID == "nss" && runtime.GOOS == "windows" {
			continue // Windows browsers use the system store
		}
		st := State{ID: t.ID, Name: t.Name, Category: t.Category, Description: t.Description, Service: t.Service, Page: t.Page}
		st.Path = t.Detect()
		st.Installed = st.Path != ""
		st.Managed = st.Installed && strings.HasPrefix(st.Path, paths.Root())
		switch {
		case t.Page != "":
			st.Method = "page"
		case t.Download != nil:
			st.Method = "download"
		case hasPM && t.Packages[pm.Name] != "":
			st.Method, st.Admin = pm.Name, pm.Admin
		}
		out = append(out, st)
	}
	return out
}

// Install installs a tool, preferring a portable download.
func Install(t *Tool, progress fetch.Progress, out io.Writer) error {
	defer Invalidate()
	if t.Download != nil {
		return t.Download(progress, out)
	}
	pm, ok := DetectManager()
	if !ok {
		return fmt.Errorf("no supported package manager found; install %s manually", t.Name)
	}
	pkgs := t.Packages[pm.Name]
	if pkgs == "" {
		return fmt.Errorf("%s isn't available through %s; install it manually", t.Name, pm.Name)
	}
	script := pm.cmd(pkgs)
	fmt.Fprintf(out, "$ %s\n", script)
	if pm.Admin {
		return elevate.RunTo(script, out)
	}
	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "powershell", "-Command"
	}
	cmd := proc.Command(shell, flag, script)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// --- Node.js ---

func installNode(progress fetch.Progress, out io.Writer) error {
	var index []struct {
		Version string `json:"version"`
		LTS     any    `json:"lts"`
	}
	if err := fetch.GetJSON("https://nodejs.org/dist/index.json", &index); err != nil {
		return err
	}
	version := ""
	for _, r := range index {
		if lts, ok := r.LTS.(string); ok && lts != "" {
			version = r.Version
			break
		}
	}
	if version == "" {
		return errors.New("no Node.js LTS release found")
	}
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	osName := map[string]string{"linux": "linux", "darwin": "darwin", "windows": "win"}[runtime.GOOS]
	if arch == "" || osName == "" {
		return errors.New("no Node.js build for this platform")
	}
	ext := ".tar.gz"
	if osName == "win" {
		ext = ".zip"
	}
	name := fmt.Sprintf("node-%s-%s-%s", version, osName, arch)
	if err := os.MkdirAll(core.NodeDir(), 0o755); err != nil {
		return err
	}
	archive := filepath.Join(core.NodeDir(), name+ext)
	fmt.Fprintf(out, "downloading Node.js %s\n", version)
	if _, err := fetch.Download("https://nodejs.org/dist/"+version+"/"+name+ext, archive, "", progress); err != nil {
		return err
	}
	defer os.Remove(archive)
	if err := fetch.Extract(archive, core.NodeDir()); err != nil {
		return err
	}
	if _, err := core.InstallShims(); err != nil {
		return err
	}
	fmt.Fprintf(out, "Node.js %s installed", version)
	return nil
}
