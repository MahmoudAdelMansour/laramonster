package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"laramonster/internal/config"
	"laramonster/internal/php"
	"laramonster/internal/sites"
)

// ProjectOptions is everything the new-project wizard can ask for.
type ProjectOptions struct {
	Name     string   `json:"name"`     // folder name
	Domain   string   `json:"domain"`   // site name; defaults to the folder name
	Dir      string   `json:"dir"`      // parent folder; defaults to the first parked path
	Kit      string   `json:"kit"`      // starter kit id
	PHP      string   `json:"php"`      // version to pin; "" = default
	Database string   `json:"database"` // sqlite, mysql, pgsql
	Packages []string `json:"packages"`
	Secure   bool     `json:"secure"`
	Mail     bool     `json:"mail"`  // send mail to Mailpit
	Redis    bool     `json:"redis"` // cache, queue and sessions on Redis
	NPM      bool     `json:"npm"`   // npm install + build
	Git      bool     `json:"git"`
}

// Env lets project creation use services owned by the daemon.
type Env struct {
	// StartService starts a service if needed and returns its port.
	StartService func(name string) (int, error)
	// Installed reports whether a service binary is available.
	Installed func(name string) bool
}

// Reporter receives progress (0..1) and log lines.
type Reporter interface {
	io.Writer
	Set(progress float64, detail string)
}

// CreateProject runs the whole recipe. Optional steps that fail (a package's
// setup command, npm, git) are reported as warnings and don't abort.
func CreateProject(o ProjectOptions, env Env, r Reporter) (string, []string, error) {
	o.Name = sites.Slug(o.Name)
	if o.Name == "" {
		return "", nil, errors.New("enter a project name")
	}
	if o.Domain = sites.Slug(o.Domain); o.Domain == "" {
		o.Domain = o.Name
	}
	kit, ok := findKit(o.Kit)
	if !ok {
		kit = StarterKits[0]
	}
	if o.Database == "" {
		o.Database = "sqlite"
	}

	c, list, err := Sites()
	if err != nil {
		return "", nil, err
	}
	if _, taken := sites.Find(list, o.Domain); taken {
		return "", nil, fmt.Errorf("%s.%s is already used by another site", o.Domain, c.TLD)
	}
	if o.Dir == "" {
		if len(c.Paths) == 0 {
			return "", nil, errors.New("park a folder first, or choose where to create the project")
		}
		o.Dir = c.Paths[0]
	}
	dir, err := absDir(o.Dir)
	if err != nil {
		return "", nil, err
	}
	target := filepath.Join(dir, o.Name)
	if _, err := os.Stat(target); err == nil {
		return "", nil, fmt.Errorf("%s already exists", target)
	}

	inst, err := phpInstall(o.PHP)
	if err != nil {
		return "", nil, err
	}
	// Fail early when PHP can't talk to the chosen database at all.
	if kit.Package != "" {
		ext := map[string]string{"sqlite": "pdo_sqlite", "mysql": "pdo_mysql", "pgsql": "pdo_pgsql"}[o.Database]
		if ext != "" && !inst.HasExtension(ext) {
			hint := "pick a PHP installed by LaraMonster (they include every database driver)"
			if !inst.Managed {
				hint = "enable `extension=" + ext + "` in your system php.ini, or " + hint
			}
			return "", nil, fmt.Errorf("PHP %s doesn't have the %s extension, so the app couldn't use %s: %s", inst.Full, ext, o.Database, hint)
		}
	}

	var warnings []string
	warn := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		warnings = append(warnings, msg)
		fmt.Fprintln(r, "warning: "+msg)
	}
	steps := []string{"create", "site", "env", "packages", "migrate", "npm", "git"}
	step := func(name, detail string) {
		r.Set(float64(slices.Index(steps, name))/float64(len(steps)), detail)
		fmt.Fprintln(r, "==> "+detail)
	}

	// 1. Create the project.
	step("create", "Creating "+o.Name+" from "+kit.Title)
	if kit.Package == "" {
		if err := os.MkdirAll(filepath.Join(target, "public"), 0o755); err != nil {
			return "", nil, err
		}
		index := "<?php\n\necho '<h1>" + o.Name + "</h1><p>Served by LaraMonster with PHP ' . PHP_VERSION . '</p>';\n"
		if err := os.WriteFile(filepath.Join(target, "public", "index.php"), []byte(index), 0o644); err != nil {
			return "", nil, err
		}
	} else {
		if err := EnsureComposer(nil); err != nil {
			return "", nil, fmt.Errorf("downloading composer: %w", err)
		}
		if err := runPHP(inst, dir, r, ComposerPhar(), "create-project", "--no-interaction", "--prefer-dist", kit.Package, o.Name); err != nil {
			_ = os.RemoveAll(target)
			return "", nil, fmt.Errorf("composer create-project failed: %w", err)
		}
	}

	// 2. Serve it: custom domain, PHP pin, HTTPS.
	step("site", "Serving it at "+o.Domain+"."+c.TLD)
	if o.Domain != o.Name || !slices.Contains(c.Paths, dir) {
		if _, err := Link(o.Domain, target); err != nil {
			return target, warnings, err
		}
	}
	if o.PHP != "" {
		if err := Mutate(func(c *config.Config) error { c.Isolated[o.Domain] = inst.Version; return nil }); err != nil {
			return target, warnings, err
		}
	}
	if o.Secure {
		if err := Mutate(func(c *config.Config) error { c.Secured[o.Domain] = true; return nil }); err != nil {
			return target, warnings, err
		}
	}
	c, _ = config.Load()
	url := sites.URL(c, o.Domain)

	isLaravel := fileExists(filepath.Join(target, "artisan"))
	if !isLaravel {
		r.Set(1, "done")
		return target, warnings, nil
	}

	// 3. .env: URL, database, mail, redis.
	step("env", "Configuring .env")
	vars := map[string]string{"APP_NAME": quoteEnv(o.Name), "APP_URL": url}
	dbName := strings.ReplaceAll(o.Domain, "-", "_")
	switch o.Database {
	case "mysql", "pgsql":
		service := map[string]string{"mysql": "mysql", "pgsql": "postgres"}[o.Database]
		port, err := env.StartService(service)
		if err != nil {
			warn("%s isn't available (%v): using SQLite instead", service, err)
			break
		}
		user := map[string]string{"mysql": "root", "pgsql": "postgres"}[o.Database]
		vars["DB_CONNECTION"] = o.Database
		vars["DB_HOST"] = "127.0.0.1"
		vars["DB_PORT"] = fmt.Sprint(port)
		vars["DB_DATABASE"] = dbName
		vars["DB_USERNAME"] = user
		vars["DB_PASSWORD"] = ""
		if err := createDatabase(inst, target, o.Database, port, user, dbName, r); err != nil {
			warn("couldn't create database %s: %v", dbName, err)
		}
	}
	if o.Mail {
		if env.Installed("mailpit") {
			if port, err := env.StartService("mailpit"); err == nil {
				for k, v := range map[string]string{"MAIL_MAILER": "smtp", "MAIL_HOST": "127.0.0.1", "MAIL_PORT": fmt.Sprint(port), "MAIL_USERNAME": "null", "MAIL_PASSWORD": "null", "MAIL_SCHEME": "null"} {
					vars[k] = v
				}
			} else {
				warn("Mailpit didn't start: %v", err)
			}
		} else {
			warn("Mailpit isn't installed: install it from Tools to catch emails")
		}
	}
	needsRedis := o.Redis
	for _, name := range o.Packages {
		if p, ok := findPackage(name); ok && p.Needs == "redis" {
			needsRedis = true
		}
	}
	if needsRedis {
		if port, err := env.StartService("redis"); err == nil {
			for k, v := range map[string]string{"REDIS_HOST": "127.0.0.1", "REDIS_PORT": fmt.Sprint(port), "CACHE_STORE": "redis", "QUEUE_CONNECTION": "redis", "SESSION_DRIVER": "redis"} {
				vars[k] = v
			}
			if !inst.HasExtension("redis") {
				vars["REDIS_CLIENT"] = "predis"
				o.Packages = append(o.Packages, "predis/predis")
			}
		} else {
			warn("Redis isn't available (%v): keeping database cache and queues", err)
		}
	}
	if slices.Contains(o.Packages, "laravel/scout") && env.Installed("meilisearch") {
		if port, err := env.StartService("meilisearch"); err == nil {
			vars["SCOUT_DRIVER"] = "meilisearch"
			vars["MEILISEARCH_HOST"] = fmt.Sprintf("http://127.0.0.1:%d", port)
			o.Packages = append(o.Packages, "meilisearch/meilisearch-php", "http-interop/http-factory-guzzle")
		}
	}
	if err := setEnv(filepath.Join(target, ".env"), vars); err != nil {
		warn("couldn't update .env: %v", err)
	}
	_ = runPHP(inst, target, r, "artisan", "config:clear")

	// 4. Packages.
	if len(o.Packages) > 0 {
		step("packages", "Installing packages")
		var prod, dev []string
		for _, name := range slices.Compact(slices.Sorted(slices.Values(o.Packages))) {
			if !validPackage.MatchString(name) {
				warn("skipped invalid package name %q", name)
				continue
			}
			if p, ok := findPackage(name); ok && p.Dev {
				dev = append(dev, name)
			} else {
				prod = append(prod, name)
			}
		}
		if slices.Contains(dev, "pestphp/pest") {
			dev = append(dev, "pestphp/pest-plugin-laravel")
		}
		installed := map[string]bool{}
		require := func(pkgs []string, devFlag bool) {
			if len(pkgs) == 0 {
				return
			}
			base := []string{ComposerPhar(), "require", "--no-interaction", "-W"}
			if devFlag {
				base = append(base, "--dev")
			}
			if runPHP(inst, target, r, append(base, pkgs...)...) == nil {
				for _, p := range pkgs {
					installed[p] = true
				}
				return
			}
			// One bad package fails the whole batch: retry one by one so the
			// good ones still get installed.
			if len(pkgs) == 1 {
				warn("couldn't install %s (check the name and version constraints)", pkgs[0])
				return
			}
			fmt.Fprintln(r, "retrying packages one by one")
			for _, p := range pkgs {
				if runPHP(inst, target, r, append(slices.Clone(base), p)...) == nil {
					installed[p] = true
				} else {
					warn("couldn't install %s (check the name and version constraints)", p)
				}
			}
		}
		require(prod, false)
		require(dev, true)
		for _, name := range o.Packages {
			if !installed[name] {
				continue
			}
			p, ok := findPackage(name)
			if !ok {
				continue
			}
			for _, post := range p.Post {
				if err := runPHP(inst, target, r, append([]string{"artisan"}, post...)...); err != nil {
					warn("%s: `php artisan %s` failed, run it yourself later", p.Title, strings.Join(post, " "))
				}
			}
		}
		if slices.Contains(dev, "pestphp/pest") {
			if err := runPHP(inst, target, r, filepath.Join("vendor", "bin", "pest"), "--init"); err != nil {
				warn("pest --init failed")
			}
		}
	}

	// 5. Migrations.
	step("migrate", "Running migrations")
	if err := runPHP(inst, target, r, "artisan", "migrate", "--force", "--no-interaction"); err != nil {
		warn("migrations failed: check your database settings in .env")
	}

	// 6. Frontend build.
	if o.NPM && kit.Frontend && fileExists(filepath.Join(target, "package.json")) {
		step("npm", "Installing frontend dependencies")
		if bin := NodeBinDir(); bin == "" {
			warn("Node.js isn't installed: install it from Tools, then run npm install && npm run build")
		} else {
			npm := filepath.Join(bin, "npm")
			if err := runTool(target, r, bin, npm, "install", "--no-audit", "--no-fund"); err != nil {
				warn("npm install failed")
			} else if err := runTool(target, r, bin, npm, "run", "build"); err != nil {
				warn("npm run build failed")
			}
		}
	}

	// 7. Git.
	if o.Git {
		step("git", "Creating the first commit")
		if _, err := exec.LookPath("git"); err != nil {
			warn("Git isn't installed: install it from Tools")
		} else if !fileExists(filepath.Join(target, ".git")) {
			gitArgs := []string{}
			if out, _ := exec.Command("git", "config", "--global", "user.email").Output(); len(strings.TrimSpace(string(out))) == 0 {
				gitArgs = []string{"-c", "user.name=LaraMonster", "-c", "user.email=laramonster@localhost"}
			}
			_ = runTool(target, r, "", "git", "init", "-q", "-b", "main")
			_ = runTool(target, r, "", "git", "add", "-A")
			if err := runTool(target, r, "", "git", append(gitArgs, "commit", "-q", "-m", "Initial commit (created with LaraMonster)")...); err != nil {
				warn("git commit failed")
			}
		}
	}

	r.Set(1, "done")
	fmt.Fprintf(r, "created %s → %s", target, url)
	return target, warnings, nil
}

var validPackage = regexp.MustCompile(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`)

func phpInstall(version string) (php.Install, error) {
	if version == "" {
		c, err := config.Load()
		if err != nil {
			return php.Install{}, err
		}
		installs := php.Discover()
		version = php.Default(c.PHP, installs)
		if version == "" {
			return php.Install{}, errors.New("no PHP installed: install a version on the PHP page first")
		}
	}
	inst, err := RequirePHP(version)
	if err == nil && inst.CLI == "" {
		err = fmt.Errorf("PHP %s has no CLI binary", inst.Version)
	}
	return inst, err
}

func runPHP(inst php.Install, dir string, out io.Writer, args ...string) error {
	cmd := exec.Command(inst.CLI, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), inst.Env()...)
	cmd.Env = append(cmd.Env, "COMPOSER_NO_INTERACTION=1")
	// Composer and artisan may call node/npm (e.g. Boost, Filament assets).
	if bin := NodeBinDir(); bin != "" {
		cmd.Env = append(cmd.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return cmd.Run()
}

func runTool(dir string, out io.Writer, pathDir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = os.Environ()
	if pathDir != "" {
		cmd.Env = append(cmd.Env, "PATH="+pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return cmd.Run()
}

func createDatabase(inst php.Install, dir, driver string, port int, user, name string, out io.Writer) error {
	var code string
	switch driver {
	case "mysql":
		code = fmt.Sprintf(`$p=new PDO("mysql:host=127.0.0.1;port=%d","%s","");$p->exec("CREATE DATABASE IF NOT EXISTS `+"`%s`"+` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci");`, port, user, name)
	case "pgsql":
		code = fmt.Sprintf(`$p=new PDO("pgsql:host=127.0.0.1;port=%d;dbname=postgres","%s","");if(!$p->query("SELECT 1 FROM pg_database WHERE datname='%s'")->fetch())$p->exec('CREATE DATABASE "%s"');`, port, user, name, name)
	}
	return runPHP(inst, dir, out, "-r", code)
}

// setEnv sets keys in a .env file, uncommenting lines like "# DB_HOST=…".
func setEnv(path string, vars map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	done := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		key, _, ok := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		if v, want := vars[key]; ok && want && !done[key] {
			lines[i] = key + "=" + v
			done[key] = true
		}
	}
	var extra []string
	for k, v := range vars {
		if !done[k] {
			extra = append(extra, k+"="+v)
		}
	}
	slices.Sort(extra)
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	if len(extra) > 0 {
		out += "\n" + strings.Join(extra, "\n") + "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

func quoteEnv(s string) string {
	if strings.ContainsAny(s, " #\"'") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
