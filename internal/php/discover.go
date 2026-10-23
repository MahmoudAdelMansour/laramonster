// Package php finds PHP installations and runs php-fpm pools for them.
package php

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"laramonster/internal/paths"
	"laramonster/internal/proc"
)

type Install struct {
	Version string   `json:"version"` // "8.4"
	Full    string   `json:"full"`    // "8.4.12"
	FPM     string   `json:"fpm"`     // php-fpm, or php-cgi.exe on Windows
	CLI     string   `json:"cli"`     // the php CLI that belongs to it, if found
	Managed bool     `json:"managed"` // installed by LaraMonster
	IniDir  string   `json:"ini_dir"` // directory with php.ini (PHPRC) for managed installs
	PDO     []string `json:"pdo"`     // available PDO drivers: sqlite, mysql, pgsql…
	Exts    []string `json:"exts"`    // loaded extensions, lower case: intl, redis…
}

// Env returns the environment needed to run this PHP with its own php.ini.
func (i Install) Env() []string {
	if i.IniDir == "" {
		return nil
	}
	return []string{"PHPRC=" + i.IniDir}
}

// candidates lists glob patterns where php-fpm binaries usually live.
// laramonster-managed installs come first so they win over system ones.
func candidates() []string {
	own := filepath.Join(paths.PHP(), "*", "php-fpm")
	switch runtime.GOOS {
	case "windows":
		return []string{
			filepath.Join(paths.PHP(), "*", "php-cgi.exe"),
			`C:\php*\php-cgi.exe`,
			`C:\tools\php*\php-cgi.exe`,
		}
	case "darwin":
		return []string{
			own,
			"/opt/homebrew/opt/php*/sbin/php-fpm",
			"/usr/local/opt/php*/sbin/php-fpm",
		}
	default:
		return []string{
			own,
			"/usr/bin/php-fpm*",
			"/usr/sbin/php-fpm*",
			"/usr/bin/php*-fpm",
			"/opt/remi/php*/root/usr/sbin/php-fpm",
		}
	}
}

var versionRe = regexp.MustCompile(`PHP (\d+)\.(\d+)\.(\d+)`)

// Discover returns one install per minor version, newest first.
func Discover() []Install {
	seen := map[string]bool{}
	byVersion := map[string]Install{}
	for _, pattern := range candidates() {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			real, err := filepath.EvalSymlinks(m)
			if err != nil || seen[real] {
				continue
			}
			seen[real] = true
			inst, ok := probe(m)
			if !ok {
				continue
			}
			if _, dup := byVersion[inst.Version]; !dup {
				byVersion[inst.Version] = inst
			}
		}
	}
	out := make([]Install, 0, len(byVersion))
	for _, i := range byVersion {
		out = append(out, i)
	}
	slices.SortFunc(out, func(a, b Install) int { return compare(b.Version, a.Version) })
	return out
}

// probeTimeout: Windows Defender scans a freshly downloaded php-cgi.exe on
// its first start, which can take well over 5 seconds.
var probeTimeout = map[bool]time.Duration{true: 30 * time.Second, false: 5 * time.Second}[runtime.GOOS == "windows"]

func probe(fpm string) (Install, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := proc.CommandContext(ctx, fpm, "-v").Output()
	if err != nil {
		return Install{}, false
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return Install{}, false
	}
	inst := Install{
		Version: m[1] + "." + m[2],
		Full:    m[1] + "." + m[2] + "." + m[3],
		FPM:     fpm,
	}
	inst.CLI = findCLI(fpm, inst.Version)
	if rel, err := filepath.Rel(paths.PHP(), fpm); err == nil && !strings.HasPrefix(rel, "..") {
		inst.Managed = true
		inst.IniDir = filepath.Dir(fpm)
	}
	inst.PDO, inst.Exts = extensions(inst)
	return inst, true
}

// findCLI looks for the php CLI that belongs to an fpm binary.
func findCLI(fpm, version string) string {
	dir := filepath.Dir(fpm)
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "php.exe")
	}
	names := []string{
		"php",
		"php" + version, // debian: php8.4
		"php" + strings.ReplaceAll(version, ".", ""), // php84
	}
	dirs := []string{dir, filepath.Join(filepath.Dir(dir), "bin")}
	for _, d := range dirs {
		for _, n := range names {
			p := filepath.Join(d, n)
			if out, err := proc.Command(p, "-r", "echo PHP_MAJOR_VERSION.'.'.PHP_MINOR_VERSION;").Output(); err == nil && string(out) == version {
				return p
			}
		}
	}
	return ""
}

// Normalize turns "php@8.4", "8.4", "84" or "8.4.12" into "8.4".
func Normalize(v string) string {
	v = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(v), "php@"), "php")
	if !strings.Contains(v, ".") && len(v) == 2 {
		return v[:1] + "." + v[1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

func Find(installs []Install, version string) (Install, bool) {
	version = Normalize(version)
	for _, i := range installs {
		if i.Version == version {
			return i, true
		}
	}
	return Install{}, false
}

// Default picks the configured version if installed, else the newest one.
func Default(configured string, installs []Install) string {
	if configured != "" {
		if i, ok := Find(installs, configured); ok {
			return i.Version
		}
	}
	if len(installs) > 0 {
		return installs[0].Version
	}
	return ""
}

func compare(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x - y
		}
	}
	return len(pa) - len(pb)
}

// extensions asks the CLI for its PDO drivers and loaded extensions.
func extensions(i Install) (pdo, exts []string) {
	if i.CLI == "" {
		return nil, nil
	}
	cmd := proc.Command(i.CLI, "-r", "echo (class_exists('PDO') ? implode(',', PDO::getAvailableDrivers()) : ''), '|', strtolower(implode(',', get_loaded_extensions())), (function_exists('mb_split') ? ',mbregex' : ''), (function_exists('imagejpeg') ? ',gdjpeg' : '');")
	cmd.Env = append(os.Environ(), i.Env()...)
	out, err := cmd.Output()
	if err != nil {
		return []string{}, []string{}
	}
	a, b, _ := strings.Cut(string(out), "|")
	return split(a), split(b)
}

func split(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// HasExtension reports whether the PHP CLI has an extension loaded.
func (i Install) HasExtension(name string) bool {
	if i.Exts != nil {
		return slices.Contains(i.Exts, strings.ToLower(name))
	}
	if i.CLI == "" {
		return false
	}
	cmd := proc.Command(i.CLI, "-r", "echo extension_loaded('"+name+"') ? 'yes' : 'no';")
	cmd.Env = append(os.Environ(), i.Env()...)
	out, err := cmd.Output()
	return err == nil && string(out) == "yes"
}
