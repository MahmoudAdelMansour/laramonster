package php

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"laramonster/internal/proc"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// Extension kinds of a PHP install, which decide what can change:
//   - static: LaraMonster's own Linux/macOS builds. Every extension is
//     compiled into the one binary; none can be added or turned off.
//   - windows: LaraMonster's Windows PHP. Extensions are DLLs in ext/ and
//     its php.ini (LaraMonster's) turns them on and off.
//   - system: a PHP from a package manager. Shared extensions in its
//     extension_dir can be turned on (LaraMonster adds -d extension=…);
//     what the system's own ini files load stays as it is.
const (
	KindStatic  = "static"
	KindWindows = "windows"
	KindSystem  = "system"
)

// zendExtensions load with zend_extension= instead of extension=.
var zendExtensions = []string{"xdebug", "opcache", "ioncube_loader", "blackfire"}

// IsZend reports whether an extension is a Zend extension.
func IsZend(name string) bool { return slices.Contains(zendExtensions, strings.ToLower(name)) }

// Ext is one extension of an install as the UI shows it.
type Ext struct {
	Name   string `json:"name"`
	State  string `json:"state"`  // builtin, on, off
	Source string `json:"source"` // built-in, php.ini, laramonster, ""
	File   string `json:"file,omitempty"`
	Toggle bool   `json:"toggle"` // can be turned on/off here
	Why    string `json:"why,omitempty"`
}

// ExtView is everything the Extensions tab shows for one PHP version.
type ExtView struct {
	Version string        `json:"version"`
	Full    string        `json:"full"`
	Kind    string        `json:"kind"`
	Dir     string        `json:"dir,omitempty"` // extension_dir
	IniFile string        `json:"ini_file,omitempty"`
	Exts    []Ext         `json:"exts"`
	Catalog []CatalogItem `json:"catalog"`
	Manager string        `json:"manager,omitempty"` // package manager for installs
}

// KindOf tells how an install handles extensions.
func KindOf(i Install) string {
	switch {
	case i.Managed && runtime.GOOS == "windows":
		return KindWindows
	case i.Managed:
		return KindStatic
	}
	return KindSystem
}

// probeExt asks the CLI for its compiled-in extensions (without any ini),
// what's loaded with its ini, its extension_dir and its scanned ini files.
func probeExt(i Install) (builtin, loaded []string, dir string, iniFiles []string, err error) {
	if i.CLI == "" {
		return nil, nil, "", nil, fmt.Errorf("PHP %s has no command-line binary next to it", i.Version)
	}
	const list = `echo strtolower(implode(",", array_merge(get_loaded_extensions(), get_loaded_extensions(true))));`
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		cmd := proc.CommandContext(ctx, i.CLI, args...)
		cmd.Env = append(os.Environ(), i.Env()...)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	b, err := run("-n", "-r", list)
	if err != nil {
		return nil, nil, "", nil, err
	}
	l, err := run("-r", list+` echo "\n", ini_get("extension_dir"), "\n", php_ini_loaded_file(), "\n", (string) php_ini_scanned_files();`)
	if err != nil {
		return nil, nil, "", nil, err
	}
	lines := strings.SplitN(l, "\n", 4)
	for len(lines) < 4 {
		lines = append(lines, "")
	}
	builtin, loaded, dir = split(b), split(lines[0]), lines[1]
	if lines[2] != "" {
		iniFiles = append(iniFiles, lines[2])
	}
	for _, f := range strings.Split(lines[3], ",") {
		if f = strings.TrimSpace(f); f != "" {
			iniFiles = append(iniFiles, f)
		}
	}
	return normExt(builtin), normExt(loaded), dir, iniFiles, nil
}

// normExt maps PHP's names to the file names extensions load by.
func normExt(list []string) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		n = strings.ToLower(strings.TrimSpace(n))
		switch n {
		case "zend opcache":
			n = "opcache"
		case "":
			continue
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// sharedFiles lists the extensions that exist as files in dir.
func sharedFiles(dir string) map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		var name string
		switch {
		case runtime.GOOS == "windows" && strings.HasPrefix(n, "php_") && strings.HasSuffix(n, ".dll"):
			name = strings.TrimSuffix(strings.TrimPrefix(n, "php_"), ".dll")
		case runtime.GOOS != "windows" && strings.HasSuffix(n, ".so"):
			name = strings.TrimSuffix(n, ".so")
		default:
			continue
		}
		out[strings.ToLower(name)] = filepath.Join(dir, n)
	}
	return out
}

// Extensions describes an install's extensions. enabled are the ones
// LaraMonster turns on for it (system installs).
func Extensions(i Install, enabled map[string]bool) (ExtView, error) {
	v := ExtView{Version: i.Version, Full: i.Full, Kind: KindOf(i)}
	builtin, loaded, dir, iniFiles, err := probeExt(i)
	if err != nil {
		return v, fmt.Errorf("couldn't ask PHP %s for its extensions: %w", i.Version, err)
	}
	v.Dir = dir
	if i.IniDir != "" {
		v.IniFile = filepath.Join(i.IniDir, "php.ini")
	}
	files := map[string]string{}
	if v.Kind != KindStatic {
		files = sharedFiles(dir)
	}
	seen := map[string]bool{}
	add := func(e Ext) {
		if !seen[e.Name] {
			seen[e.Name] = true
			v.Exts = append(v.Exts, e)
		}
	}
	for _, n := range loaded {
		e := Ext{Name: n, State: "on", File: files[n]}
		switch {
		case slices.Contains(builtin, n) || v.Kind == KindStatic:
			e.State, e.Source, e.Why = "builtin", "built-in", "Compiled into PHP: always on."
		case v.Kind == KindWindows:
			e.Source, e.Toggle = "php.ini", true
		default:
			e.Source = "php.ini"
			e.Why = "Turned on by the system's PHP configuration"
			if f := iniFileFor(iniFiles, n); f != "" {
				e.Why += " (" + f + ")"
			}
			e.Why += ". Turn it off there."
		}
		add(e)
	}
	for n, f := range files {
		if seen[n] {
			continue
		}
		e := Ext{Name: n, State: "off", File: f, Toggle: true}
		if enabled[n] {
			e.State, e.Source = "on", "laramonster"
		}
		add(e)
	}
	slices.SortFunc(v.Exts, func(a, b Ext) int { return strings.Compare(a.Name, b.Name) })
	v.Catalog = catalogFor(v)
	if v.Kind == KindSystem {
		if m := managerName(); m != "" {
			v.Manager = m
		}
	}
	return v, nil
}

// iniFileFor finds the ini file that loads an extension.
func iniFileFor(files []string, name string) string {
	re := regexp.MustCompile(`(?mi)^\s*(zend_)?extension\s*=\s*"?([^"\s;]*[/\\])?(php_)?` + regexp.QuoteMeta(name) + `(\.so|\.dll)?"?\s*$`)
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && re.Match(b) {
			return f
		}
	}
	return ""
}

// ExtArgs are the -d arguments that load the extensions LaraMonster turns
// on for a system PHP.
func ExtArgs(enabled map[string]bool) []string {
	names := make([]string, 0, len(enabled))
	for n, on := range enabled {
		if on {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	var out []string
	for _, n := range names {
		key := "extension"
		if IsZend(n) {
			key = "zend_extension"
		}
		out = append(out, "-d", key+"="+n)
	}
	return out
}

// SetIniExtension turns an extension on or off in a php.ini that
// LaraMonster manages (Windows), keeping the rest of the file as it is.
func SetIniExtension(iniFile, name string, on bool) error {
	b, err := os.ReadFile(iniFile)
	if err != nil {
		return err
	}
	key := "extension"
	if IsZend(name) {
		key = "zend_extension"
	}
	re := regexp.MustCompile(`(?i)^\s*;?\s*(zend_)?extension\s*=\s*"?(php_)?` + regexp.QuoteMeta(name) + `(\.dll|\.so)?"?\s*$`)
	var lines []string
	found := false
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		l := sc.Text()
		if re.MatchString(l) {
			if found {
				continue // a second line for the same extension
			}
			found = true
			l = key + "=" + name
			if !on {
				l = ";" + l
			}
		}
		lines = append(lines, l)
	}
	if !found && on {
		lines = append(lines, key+"="+name)
	}
	return os.WriteFile(iniFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// ── catalog: popular extensions and how to get them ──

// CatalogItem is a well-known extension and how this PHP can get it.
type CatalogItem struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Desc   string `json:"desc"`
	State  string `json:"state"`            // on, off (file present), installable, missing
	Method string `json:"method,omitempty"` // download, package
	Pkg    string `json:"pkg,omitempty"`    // package that provides it
	Note   string `json:"note,omitempty"`
}

type known struct {
	name, title, desc string
	pecl              bool // downloadable for Windows from PECL
}

var catalog = []known{
	{"xdebug", "Xdebug", "Step debugging with breakpoints in your editor, and code coverage.", false},
	{"redis", "Redis (phpredis)", "Fast Redis client for cache, sessions and queues.", true},
	{"apcu", "APCu", "In-memory cache shared by PHP requests.", true},
	{"igbinary", "igbinary", "Compact serializer; makes Redis and caches smaller.", true},
	{"imagick", "Imagick", "ImageMagick images: PDFs, HEIC, advanced resizing.", false},
	{"mongodb", "MongoDB", "Driver for MongoDB databases.", true},
	{"pcov", "PCOV", "Fast code coverage for PHPUnit and Pest.", true},
	{"msgpack", "MessagePack", "Compact binary serialization.", true},
	{"memcached", "Memcached", "Client for Memcached servers.", false},
	{"intl", "intl", "Internationalization: Filament, number and date formatting.", false},
	{"gd", "GD", "Images: thumbnails, uploads, captchas.", false},
	{"sodium", "Sodium", "Modern cryptography (Laravel uses it for some hashes).", false},
	{"zip", "Zip", "Read and write zip files (Composer needs it).", false},
	{"bcmath", "BCMath", "Exact decimal maths for money.", false},
	{"pdo_pgsql", "PostgreSQL (PDO)", "Connect Laravel to PostgreSQL.", false},
	{"pdo_mysql", "MySQL (PDO)", "Connect Laravel to MySQL and MariaDB.", false},
	{"pdo_sqlite", "SQLite (PDO)", "Connect Laravel to SQLite.", false},
	{"soap", "SOAP", "Talk to SOAP web services.", false},
	{"ldap", "LDAP", "Sign in with Active Directory / LDAP.", false},
	{"exif", "EXIF", "Read photo metadata and orientation.", false},
	{"opcache", "OPcache", "Keeps compiled PHP in memory: much faster pages.", false},
}

func catalogFor(v ExtView) []CatalogItem {
	have := map[string]Ext{}
	for _, e := range v.Exts {
		have[e.Name] = e
	}
	mgr := managerName()
	var out []CatalogItem
	for _, k := range catalog {
		c := CatalogItem{Name: k.name, Title: k.title, Desc: k.desc}
		if e, ok := have[k.name]; ok {
			c.State = e.State
			if c.State == "builtin" {
				c.State = "on"
			}
			out = append(out, c)
			continue
		}
		c.State = "missing"
		switch v.Kind {
		case KindStatic:
			c.Note = "Not in LaraMonster's PHP build, which can't load extra extensions. Install PHP " + v.Version + " with your package manager to use it (LaraMonster finds it)."
		case KindWindows:
			if k.pecl || k.name == "xdebug" {
				c.State, c.Method = "installable", "download"
			} else {
				c.Note = "Not available as a download for PHP " + v.Version + " on Windows."
			}
		case KindSystem:
			if pkg := packageFor(mgr, k.name, v.Version); pkg != "" {
				c.State, c.Method, c.Pkg = "installable", "package", pkg
			} else if mgr == "" {
				c.Note = "No supported package manager found: install it with pecl install " + k.name + "."
			} else {
				c.Note = mgr + " has no package for it: install it with pecl install " + k.name + "."
			}
		}
		out = append(out, c)
	}
	return out
}

// managerName is the package manager that installs PHP extensions here.
func managerName() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	for _, m := range []string{"apt-get", "dnf", "pacman", "zypper", "brew"} {
		if runtime.GOOS == "darwin" && m != "brew" {
			continue
		}
		if _, err := exec.LookPath(m); err == nil {
			return m
		}
	}
	return ""
}

// packageFor names the package that provides an extension for a system PHP.
func packageFor(mgr, ext, version string) string {
	switch mgr {
	case "apt-get":
		switch ext {
		case "pdo_pgsql":
			ext = "pgsql"
		case "pdo_mysql":
			ext = "mysql"
		case "pdo_sqlite":
			ext = "sqlite3"
		case "opcache", "sodium", "exif":
			return "" // part of php-common
		}
		return "php" + version + "-" + ext
	case "dnf":
		switch ext {
		case "xdebug":
			return "php-pecl-xdebug3"
		case "redis", "apcu", "igbinary", "imagick", "mongodb", "pcov", "msgpack", "memcached":
			return "php-pecl-" + ext
		case "pdo_pgsql":
			return "php-pgsql"
		case "pdo_mysql":
			return "php-mysqlnd"
		case "pdo_sqlite", "exif":
			return ""
		}
		return "php-" + ext
	case "pacman":
		switch ext {
		case "xdebug":
			return "xdebug"
		case "redis", "apcu", "igbinary", "imagick", "memcached", "gd", "sodium", "pgsql", "pdo_pgsql", "sqlite", "pdo_sqlite":
			return "php-" + strings.TrimPrefix(ext, "pdo_")
		}
		return ""
	case "zypper":
		return "php8-" + ext
	case "brew":
		switch ext {
		case "xdebug", "redis", "apcu", "igbinary", "imagick", "mongodb", "pcov", "msgpack", "memcached":
			return "shivammathur/extensions/" + ext + "@" + version
		}
		return ""
	}
	return ""
}

// InstallCommand is the shell command that installs pkg, and whether it
// needs administrator rights.
func InstallCommand(mgr, pkg string) (string, bool) {
	switch mgr {
	case "apt-get":
		return "export DEBIAN_FRONTEND=noninteractive; apt-get install -y -q " + pkg, true
	case "dnf":
		return "dnf install -y " + pkg, true
	case "pacman":
		return "pacman -S --needed --noconfirm " + pkg, true
	case "zypper":
		return "zypper --non-interactive install " + pkg, true
	case "brew":
		return "brew install " + pkg, false
	}
	return "", false
}

// Package returns the package manager and package for a catalog extension.
func Package(i Install, ext string) (mgr, pkg string, err error) {
	mgr = managerName()
	if mgr == "" {
		return "", "", errors.New("no supported package manager found")
	}
	if pkg = packageFor(mgr, ext, i.Version); pkg == "" {
		return mgr, "", fmt.Errorf("%s has no package for %s", mgr, ext)
	}
	return mgr, pkg, nil
}

// ── Windows downloads ──

var peclVersion = regexp.MustCompile(`href="(\d+\.\d+\.\d+)/"`)

// WindowsDownload finds the DLL zip (PECL) or DLL (Xdebug) of an extension
// for a Windows PHP version: the newest stable release built for it.
func WindowsDownload(ext, phpVersion string, get func(string) (string, error)) (url string, err error) {
	if ext == "xdebug" {
		page, err := get("https://xdebug.org/download")
		if err != nil {
			return "", err
		}
		re := regexp.MustCompile(`php_xdebug-(\d+\.\d+\.\d+)-` + regexp.QuoteMeta(phpVersion) + `-nts-vs1[67]-x86_64\.dll`)
		if m := re.FindString(page); m != "" {
			return "https://xdebug.org/files/" + m, nil
		}
		return "", fmt.Errorf("xdebug.org has no build for PHP %s yet", phpVersion)
	}
	base := "https://downloads.php.net/~windows/pecl/releases/" + ext + "/"
	page, err := get(base)
	if err != nil {
		return "", err
	}
	var versions []string
	for _, m := range peclVersion.FindAllStringSubmatch(page, -1) {
		versions = append(versions, m[1])
	}
	slices.SortFunc(versions, func(a, b string) int { return compareSemver(b, a) })
	re := regexp.MustCompile(`php_` + regexp.QuoteMeta(ext) + `-[\d.]+-` + regexp.QuoteMeta(phpVersion) + `-nts-vs1[67]-x64\.zip`)
	for i, v := range versions {
		if i >= 6 {
			break
		}
		dir, err := get(base + v + "/")
		if err != nil {
			continue
		}
		if m := re.FindString(dir); m != "" {
			return base + v + "/" + m, nil
		}
	}
	return "", fmt.Errorf("PECL has no Windows build of %s for PHP %s yet", ext, phpVersion)
}

func compareSemver(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			return x - y
		}
	}
	return 0
}
