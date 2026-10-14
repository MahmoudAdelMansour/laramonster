package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"laramonster/internal/fetch"
	"laramonster/internal/paths"
)

var mysqlDef = &Def{
	Name:        "mysql",
	Title:       "MySQL / MariaDB",
	Category:    "Database",
	DefaultPort: 3306,
	Binaries:    []string{"mariadbd", "mysqld"},
	Globs: []string{
		"/opt/homebrew/opt/mariadb/bin/mariadbd",
		`C:\Program Files\MariaDB *\bin\mysqld.exe`,
		`C:\Program Files\MySQL\MySQL Server *\bin\mysqld.exe`,
	},
	InstallHint: "install MariaDB or MySQL with your package manager (e.g. pacman -S mariadb, apt install mariadb-server, brew install mariadb)",
	Init: func(bin, data string, port int) error {
		if isMariaDB(bin) {
			installDB := siblingOrPath(bin, "mariadb-install-db", "mysql_install_db")
			if installDB == "" {
				return fmt.Errorf("mariadb-install-db not found")
			}
			args := []string{"--no-defaults", "--datadir=" + data, "--auth-root-authentication-method=normal", "--skip-test-db"}
			if runtime.GOOS != "windows" {
				args = append(args, "--user="+currentUser())
			}
			return run(installDB, args...)
		}
		return run(bin, "--no-defaults", "--initialize-insecure", "--datadir="+data)
	},
	Args: func(bin, data string, port int) []string {
		args := []string{
			"--no-defaults",
			"--datadir=" + data,
			fmt.Sprintf("--port=%d", port),
			"--bind-address=127.0.0.1",
			"--pid-file=" + filepath.Join(paths.Run(), "mysql.pid"),
		}
		if runtime.GOOS != "windows" {
			args = append(args, "--socket="+filepath.Join(paths.Run(), "mysql.sock"))
		}
		return args
	},
	Env: func(port int) string {
		return fmt.Sprintf("DB_CONNECTION=mysql\nDB_HOST=127.0.0.1\nDB_PORT=%d\nDB_DATABASE=laravel\nDB_USERNAME=root\nDB_PASSWORD=", port)
	},
}

var postgresDef = &Def{
	Name:        "postgres",
	Title:       "PostgreSQL",
	Category:    "Database",
	DefaultPort: 5432,
	Binaries:    []string{"postgres"},
	Globs: []string{
		"/usr/lib/postgresql/*/bin/postgres",
		"/opt/homebrew/opt/postgresql@*/bin/postgres",
		"/usr/local/opt/postgresql@*/bin/postgres",
		`C:\Program Files\PostgreSQL\*\bin\postgres.exe`,
	},
	InstallHint: "install PostgreSQL with your package manager (e.g. pacman -S postgresql, apt install postgresql, brew install postgresql)",
	Download: func(dir string, progress fetch.Progress) error {
		target := map[string]string{
			"linux/amd64":   "x86_64-unknown-linux-gnu",
			"linux/arm64":   "aarch64-unknown-linux-gnu",
			"darwin/amd64":  "x86_64-apple-darwin",
			"darwin/arm64":  "aarch64-apple-darwin",
			"windows/amd64": "x86_64-pc-windows-msvc",
		}[platformKey()]
		if target == "" {
			return errUnsupported
		}
		var rel struct {
			Tag string `json:"tag_name"`
		}
		if err := fetch.GetJSON("https://api.github.com/repos/theseus-rs/postgresql-binaries/releases/latest", &rel); err != nil {
			return err
		}
		ext := ".tar.gz"
		if runtime.GOOS == "windows" {
			ext = ".zip"
		}
		archive, err := githubAsset("theseus-rs/postgresql-binaries", "postgresql-"+rel.Tag+"-"+target+ext, dir, progress)
		if err != nil {
			return err
		}
		defer os.Remove(archive)
		return fetch.Extract(archive, dir)
	},
	Init: func(bin, data string, port int) error {
		initdb := siblingOrPath(bin, "initdb")
		if initdb == "" {
			return fmt.Errorf("initdb not found")
		}
		return run(initdb, "-D", data, "-U", "postgres", "--auth=trust", "-E", "UTF8")
	},
	Args: func(bin, data string, port int) []string {
		args := []string{"-D", data, "-p", fmt.Sprint(port), "-c", "listen_addresses=127.0.0.1"}
		if runtime.GOOS != "windows" {
			args = append(args, "-k", paths.Run())
		}
		return args
	},
	Env: func(port int) string {
		return fmt.Sprintf("DB_CONNECTION=pgsql\nDB_HOST=127.0.0.1\nDB_PORT=%d\nDB_DATABASE=laravel\nDB_USERNAME=postgres\nDB_PASSWORD=", port)
	},
}

var redisDef = &Def{
	Name:        "redis",
	Title:       "Redis / Valkey",
	Category:    "Cache",
	DefaultPort: 6379,
	Binaries:    []string{"redis-server", "valkey-server"},
	InstallHint: "install Redis or Valkey with your package manager (e.g. pacman -S valkey, apt install redis-server, brew install redis)",
	Args: func(bin, data string, port int) []string {
		return []string{"--port", fmt.Sprint(port), "--bind", "127.0.0.1", "--dir", data, "--daemonize", "no", "--appendonly", "yes"}
	},
	Env: func(port int) string {
		return fmt.Sprintf("REDIS_CLIENT=phpredis\nREDIS_HOST=127.0.0.1\nREDIS_PORT=%d\nREDIS_PASSWORD=null", port)
	},
}

var mailpitDef = &Def{
	Name:        "mailpit",
	Title:       "Mailpit",
	Category:    "Mail",
	DefaultPort: 1025,
	Binaries:    []string{"mailpit"},
	Download: func(dir string, progress fetch.Progress) error {
		goos := map[string]string{"linux": "linux", "darwin": "darwin", "windows": "windows"}[runtime.GOOS]
		ext := ".tar.gz"
		if goos == "windows" {
			ext = ".zip"
		}
		if goos == "" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
			return errUnsupported
		}
		archive, err := githubAsset("axllent/mailpit", "mailpit-"+goos+"-"+runtime.GOARCH+ext, dir, progress)
		if err != nil {
			return err
		}
		defer os.Remove(archive)
		return fetch.Extract(archive, dir)
	},
	Args: func(bin, data string, port int) []string {
		return []string{
			"--smtp", fmt.Sprintf("127.0.0.1:%d", port),
			"--listen", fmt.Sprintf("127.0.0.1:%d", port+7000),
			"--database", filepath.Join(data, "mailpit.db"),
			"--smtp-auth-accept-any", "--smtp-auth-allow-insecure",
		}
	},
	WebPort: func(port int) int { return port + 7000 },
	Env: func(port int) string {
		return fmt.Sprintf("MAIL_MAILER=smtp\nMAIL_HOST=127.0.0.1\nMAIL_PORT=%d\nMAIL_USERNAME=null\nMAIL_PASSWORD=null", port)
	},
}

var meilisearchDef = &Def{
	Name:        "meilisearch",
	Title:       "Meilisearch",
	Category:    "Search",
	DefaultPort: 7700,
	Binaries:    []string{"meilisearch"},
	Download: func(dir string, progress fetch.Progress) error {
		asset := map[string]string{
			"linux/amd64":   "meilisearch-linux-amd64",
			"linux/arm64":   "meilisearch-linux-aarch64",
			"darwin/amd64":  "meilisearch-macos-amd64",
			"darwin/arm64":  "meilisearch-macos-apple-silicon",
			"windows/amd64": "meilisearch-windows-amd64.exe",
		}[platformKey()]
		if asset == "" {
			return errUnsupported
		}
		p, err := githubAsset("meilisearch/meilisearch", asset, dir, progress)
		if err != nil {
			return err
		}
		name := "meilisearch"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.Rename(p, filepath.Join(dir, name)); err != nil {
			return err
		}
		return os.Chmod(filepath.Join(dir, name), 0o755)
	},
	Args: func(bin, data string, port int) []string {
		return []string{"--db-path", filepath.Join(data, "data.ms"), "--http-addr", fmt.Sprintf("127.0.0.1:%d", port), "--no-analytics", "--env", "development"}
	},
	WebPort: func(port int) int { return port },
	Env: func(port int) string {
		return fmt.Sprintf("SCOUT_DRIVER=meilisearch\nMEILISEARCH_HOST=http://127.0.0.1:%d\nMEILISEARCH_KEY=", port)
	},
}

func isMariaDB(bin string) bool {
	if strings.Contains(filepath.Base(bin), "mariadb") {
		return true
	}
	out, _ := exec.Command(bin, "--version").Output()
	return strings.Contains(strings.ToLower(string(out)), "mariadb")
}

// siblingOrPath finds a helper binary next to bin, or in PATH.
func siblingOrPath(bin string, names ...string) string {
	for _, n := range names {
		if runtime.GOOS == "windows" {
			n += ".exe"
		}
		if p := filepath.Join(filepath.Dir(bin), n); isFile(p) {
			return p
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}
