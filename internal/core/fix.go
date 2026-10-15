package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"laramonster/internal/php"
	"laramonster/internal/sites"
)

// Warning is an optional step that didn't work. Detail is the end of the
// failing command's output; Fix, when set, is a one-click way to retry or
// repair it (RunFix).
type Warning struct {
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Fix     *Fix   `json:"fix,omitempty"`
}

func (w Warning) String() string { return w.Message }

// Fix kinds. Args never carry commands: "setup" names a catalog package
// whose own setup commands are run.
const (
	FixMigrate  = "migrate"  // php artisan migrate
	FixSetup    = "setup"    // a package's setup commands; Args: package name
	FixRequire  = "require"  // composer require; Args: packages
	FixDatabase = "database" // start a database, create it, point .env at it, migrate; Args: driver
	FixNPM      = "npm"      // npm install && npm run build
	FixGit      = "git"      // git init + first commit
)

type Fix struct {
	Label string   `json:"label"`
	Kind  string   `json:"kind"`
	Args  []string `json:"args,omitempty"`
}

// warnings collects warnings and echoes them to the job log.
type warnings struct {
	list []Warning
	out  io.Writer
}

func (w *warnings) add(fix *Fix, detail string, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	w.list = append(w.list, Warning{Message: msg, Detail: detail, Fix: fix})
	fmt.Fprintln(w.out, "warning: "+msg)
}

// tail passes output through and keeps its last few KB, so a warning can
// say why a command failed.
type tail struct {
	w   io.Writer
	mu  sync.Mutex
	buf []byte
}

func newTail(w io.Writer) *tail { return &tail{w: w} }

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 16<<10 {
		t.buf = t.buf[len(t.buf)-8<<10:]
	}
	t.mu.Unlock()
	return t.w.Write(p)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// Detail is the last meaningful lines: no colours, PHP deprecation noise,
// progress bars or blank lines.
func (t *tail) Detail() string {
	t.mu.Lock()
	text := ansi.ReplaceAllString(string(t.buf), "")
	t.mu.Unlock()
	var keep []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "Deprecated:"), strings.HasPrefix(line, "PHP Deprecated:"),
			strings.Contains(line, "[====") || strings.Contains(line, "[>---") || strings.Contains(line, "----]"):
			continue
		}
		keep = append(keep, line)
	}
	// Composer explains a failed install under "Problem 1"; its advice
	// that follows is generic.
	if i := slices.IndexFunc(keep, func(l string) bool { return strings.HasPrefix(l, "Problem 1") }); i >= 0 {
		end := i + 1
		for end < len(keep) && end-i < 8 && strings.HasPrefix(keep[end], "- ") {
			end++
		}
		keep = keep[i:end]
	} else if len(keep) > 8 {
		keep = keep[len(keep)-8:]
	}
	return strings.Join(keep, "\n")
}

var missingExtRe = regexp.MustCompile(`requires? ext-([a-z0-9_]+) \S+ -> it is missing from your system`)

// MissingExt is the PHP extension Composer said is missing, or "".
func (t *tail) MissingExt() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m := missingExtRe.FindSubmatch(ansi.ReplaceAll(t.buf, nil)); m != nil {
		return string(m[1])
	}
	return ""
}

// packageArg is "vendor/name" or "vendor/name:constraint" as typed in the
// wizard. It returns the name and the argument for composer require.
func packageArg(s string) (name, arg string, ok bool) {
	s = strings.TrimSpace(s)
	name, constraint, hasC := strings.Cut(s, ":")
	name = strings.ToLower(strings.TrimSpace(name))
	if !validPackage.MatchString(name) {
		return "", "", false
	}
	if !hasC {
		if p, found := findPackage(name); found && p.Version != "" {
			return name, name + ":" + p.Version, true
		}
		return name, name, true
	}
	constraint = strings.TrimSpace(constraint)
	if constraint == "" || !validConstraint.MatchString(constraint) {
		return "", "", false
	}
	return name, name + ":" + constraint, true
}

var validConstraint = regexp.MustCompile(`^[0-9A-Za-z.*^~|<>=!@,\- ]{1,64}$`)

// InstallPackages runs composer require in a Laravel project, then each
// catalog package's setup commands. Packages that need a PHP extension the
// PHP lacks are skipped with a warning instead of letting Composer fall back
// to an ancient release.
func InstallPackages(inst php.Install, dir string, names []string, r io.Writer) (installed []string, warns []Warning) {
	w := &warnings{out: r}
	var prod, dev, all []string
	seen := map[string]bool{}
	for _, raw := range names {
		name, arg, ok := packageArg(raw)
		if !ok {
			w.add(nil, "", "skipped %q: package names look like vendor/name (optionally vendor/name:^1.0)", raw)
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		p, known := findPackage(name)
		if missing := missingExt(inst, p.Ext); known && missing != "" {
			w.add(nil, "", "skipped %s: it needs PHP's %s extension, which PHP %s (%s) doesn't have", p.Title, missing, inst.Full, phpSource(inst))
			continue
		}
		all = append(all, name)
		if known && p.Dev {
			dev = append(dev, arg)
		} else {
			prod = append(prod, arg)
		}
	}
	if slices.Contains(all, "pestphp/pest") {
		dev = append(dev, "pestphp/pest-plugin-laravel")
	}
	done := map[string]bool{}
	require := func(args []string, devFlag bool) {
		if len(args) == 0 {
			return
		}
		base := []string{ComposerPhar(), "require", "--no-interaction", "-W"}
		if devFlag {
			base = append(base, "--dev")
		}
		failed := func(a string, t *tail) {
			fix := &Fix{Label: "Try again", Kind: FixRequire, Args: []string{a}}
			if devFlag {
				fix.Args = append([]string{"--dev"}, a)
			}
			name := strings.SplitN(a, ":", 2)[0]
			if ext := t.MissingExt(); ext != "" {
				w.add(nil, t.Detail(), "couldn't install %s: this project needs PHP's %s extension, which PHP %s (%s) doesn't have", name, ext, inst.Full, phpSource(inst))
				return
			}
			w.add(fix, t.Detail(), "couldn't install %s", name)
		}
		t := newTail(r)
		if runPHP(inst, dir, t, append(base, args...)...) == nil {
			for _, a := range args {
				done[strings.SplitN(a, ":", 2)[0]] = true
			}
			return
		}
		if len(args) == 1 {
			failed(args[0], t)
			return
		}
		// One bad package fails the whole batch: retry one by one so the
		// good ones still get installed.
		fmt.Fprintln(r, "retrying packages one by one")
		for _, a := range args {
			t := newTail(r)
			if runPHP(inst, dir, t, append(slices.Clone(base), a)...) != nil {
				failed(a, t)
				continue
			}
			done[strings.SplitN(a, ":", 2)[0]] = true
		}
	}
	require(prod, false)
	require(dev, true)
	for _, name := range all {
		if !done[name] {
			continue
		}
		installed = append(installed, name)
		if err := setupPackage(inst, dir, name, r); err != nil {
			p, _ := findPackage(name)
			w.add(&Fix{Label: "Run it again", Kind: FixSetup, Args: []string{name}}, err.detail,
				"%s: `%s` failed", p.Title, err.cmd)
		}
	}
	return installed, w.list
}

type setupError struct{ cmd, detail string }

func (e *setupError) Error() string { return "`" + e.cmd + "` failed: " + e.detail }

// setupPackage runs a catalog package's setup commands (filament:install,
// install:api…). Packages outside the catalog have none.
func setupPackage(inst php.Install, dir, name string, r io.Writer) *setupError {
	p, ok := findPackage(name)
	if !ok {
		return nil
	}
	for _, post := range p.Post {
		t := newTail(r)
		if err := runPHP(inst, dir, t, append([]string{"artisan"}, post...)...); err != nil {
			return &setupError{cmd: "php artisan " + strings.Join(post, " "), detail: t.Detail()}
		}
	}
	if name == "pestphp/pest" {
		t := newTail(r)
		if err := runPHP(inst, dir, t, filepath.Join("vendor", "bin", "pest"), "--init"); err != nil {
			return &setupError{cmd: "pest --init", detail: t.Detail()}
		}
	}
	return nil
}

func missingExt(inst php.Install, exts []string) string {
	for _, e := range exts {
		if !inst.HasExtension(e) {
			return e
		}
	}
	return ""
}

func phpSource(inst php.Install) string {
	if inst.Managed {
		return "LaraMonster"
	}
	return "system"
}

// migrate runs the migrations and explains a failure.
func migrate(inst php.Install, dir string, w *warnings, r io.Writer) {
	t := newTail(r)
	if err := runPHP(inst, dir, t, "artisan", "migrate", "--force", "--no-interaction"); err != nil {
		w.add(&Fix{Label: "Run migrations again", Kind: FixMigrate}, t.Detail(), "migrations failed")
	}
}

// ComposerPackages lists the packages a project requires (require and
// require-dev), for marking them as installed in the package picker.
func ComposerPackages(dir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return nil, err
	}
	var c struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &c); err != nil {
		return nil, fmt.Errorf("composer.json: %w", err)
	}
	out := []string{}
	for name := range c.Require {
		if strings.Contains(name, "/") {
			out = append(out, strings.ToLower(name))
		}
	}
	for name := range c.RequireDev {
		out = append(out, strings.ToLower(name))
	}
	slices.Sort(out)
	return out, nil
}

// ProjectDir checks that dir is a Laravel project LaraMonster serves.
func ProjectDir(dir string) (string, error) {
	_, list, err := Sites()
	if err != nil {
		return "", err
	}
	dir = filepath.Clean(dir)
	for _, s := range list {
		if filepath.Clean(s.Path) == dir {
			if !fileExists(filepath.Join(dir, "artisan")) {
				return "", fmt.Errorf("%s isn't a Laravel project", s.Name)
			}
			return dir, nil
		}
	}
	return "", fmt.Errorf("%s isn't one of your sites", dir)
}

// RunFix repairs what a warning reported, in the project at dir.
func RunFix(dir string, f Fix, env Env, r Reporter) ([]Warning, error) {
	dir, err := ProjectDir(dir)
	if err != nil {
		return nil, err
	}
	inst, err := PHPFor(dir)
	if err != nil {
		return nil, err
	}
	w := &warnings{out: r}
	r.Set(-1, f.Label)
	switch f.Kind {
	case FixMigrate:
		migrate(inst, dir, w, r)
	case FixSetup:
		if len(f.Args) != 1 {
			return nil, errors.New("setup needs one package")
		}
		if _, ok := findPackage(f.Args[0]); !ok {
			return nil, fmt.Errorf("%s has no setup commands", f.Args[0])
		}
		if err := setupPackage(inst, dir, f.Args[0], r); err != nil {
			return nil, err
		}
	case FixRequire:
		args := slices.DeleteFunc(slices.Clone(f.Args), func(a string) bool { return a == "--dev" })
		_, warns := InstallPackages(inst, dir, args, r)
		w.list = append(w.list, warns...)
	case FixDatabase:
		if len(f.Args) != 1 {
			return nil, errors.New("database needs a driver")
		}
		if err := useDatabase(inst, dir, f.Args[0], env, w, r); err != nil {
			return nil, err
		}
		migrate(inst, dir, w, r)
	case FixNPM:
		buildFrontend(dir, w, r)
	case FixGit:
		firstCommit(dir, w, r)
	default:
		return nil, fmt.Errorf("unknown fix %q", f.Kind)
	}
	r.Set(1, "done")
	return w.list, nil
}

// useDatabase starts MySQL or PostgreSQL, creates the project's database
// and points .env at it.
func useDatabase(inst php.Install, dir, driver string, env Env, w *warnings, r io.Writer) error {
	service, ok := map[string]string{"mysql": "mysql", "pgsql": "postgres"}[driver]
	if !ok {
		return fmt.Errorf("unknown database %q", driver)
	}
	if ext := "pdo_" + driver; !inst.HasExtension(ext) {
		return fmt.Errorf("PHP %s doesn't have the %s extension: pick a PHP installed by LaraMonster", inst.Full, ext)
	}
	if env.Installed != nil && !env.Installed(service) {
		return fmt.Errorf("%s isn't installed: install it from Tools first", service)
	}
	port, err := env.StartService(service)
	if err != nil {
		return fmt.Errorf("%s didn't start: %w", service, err)
	}
	user := map[string]string{"mysql": "root", "pgsql": "postgres"}[driver]
	name := strings.ReplaceAll(sites.Slug(filepath.Base(dir)), "-", "_")
	fmt.Fprintf(r, "creating database %s on port %d\n", name, port)
	t := newTail(r)
	if err := createDatabase(inst, dir, driver, port, user, name, t); err != nil {
		return fmt.Errorf("couldn't create database %s: %s", name, t.Detail())
	}
	vars := map[string]string{
		"DB_CONNECTION": driver, "DB_HOST": "127.0.0.1", "DB_PORT": fmt.Sprint(port),
		"DB_DATABASE": name, "DB_USERNAME": user, "DB_PASSWORD": "",
	}
	if err := setEnv(filepath.Join(dir, ".env"), vars); err != nil {
		return fmt.Errorf("couldn't update .env: %w", err)
	}
	_ = runPHP(inst, dir, io.Discard, "artisan", "config:clear")
	fmt.Fprintf(r, ".env now uses %s on 127.0.0.1:%d, database %s\n", driver, port, name)
	return nil
}

func buildFrontend(dir string, w *warnings, r io.Writer) {
	fix := &Fix{Label: "Build again", Kind: FixNPM}
	bin := NodeBinDir()
	if bin == "" {
		w.add(fix, "", "Node.js isn't installed: install it from Tools, then build again")
		return
	}
	npm := filepath.Join(bin, "npm")
	t := newTail(r)
	if err := runTool(dir, t, bin, npm, "install", "--no-audit", "--no-fund"); err != nil {
		w.add(fix, t.Detail(), "npm install failed")
	} else if err := runTool(dir, t, bin, npm, "run", "build"); err != nil {
		w.add(fix, t.Detail(), "npm run build failed")
	}
}

func firstCommit(dir string, w *warnings, r io.Writer) {
	fix := &Fix{Label: "Try again", Kind: FixGit}
	if _, err := exec.LookPath("git"); err != nil {
		w.add(fix, "", "Git isn't installed: install it from Tools")
		return
	}
	if fileExists(filepath.Join(dir, ".git")) {
		return
	}
	gitArgs := []string{}
	if out, _ := exec.Command("git", "config", "--global", "user.email").Output(); len(strings.TrimSpace(string(out))) == 0 {
		gitArgs = []string{"-c", "user.name=LaraMonster", "-c", "user.email=laramonster@localhost"}
	}
	t := newTail(r)
	_ = runTool(dir, t, "", "git", "init", "-q", "-b", "main")
	_ = runTool(dir, t, "", "git", "add", "-A")
	if err := runTool(dir, t, "", "git", append(gitArgs, "commit", "-q", "-m", "Initial commit (created with LaraMonster)")...); err != nil {
		_ = os.RemoveAll(filepath.Join(dir, ".git"))
		w.add(fix, t.Detail(), "git commit failed")
	}
}
