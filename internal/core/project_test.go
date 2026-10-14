package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetEnvUncommentsAndAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	in := "APP_NAME=Laravel\nDB_CONNECTION=sqlite\n# DB_HOST=127.0.0.1\n# DB_PORT=3306\nMAIL_MAILER=log\n"
	if err := os.WriteFile(p, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	err := setEnv(p, map[string]string{"APP_NAME": "shop", "DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_PORT": "3307", "SCOUT_DRIVER": "meilisearch"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	for _, want := range []string{"APP_NAME=shop\n", "DB_CONNECTION=mysql\n", "DB_HOST=127.0.0.1\n", "DB_PORT=3307\n", "MAIL_MAILER=log\n", "SCOUT_DRIVER=meilisearch\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "# DB_HOST") || strings.Count(got, "DB_PORT=") != 1 {
		t.Errorf("commented keys not replaced in place:\n%s", got)
	}
}

func TestPackageNameValidation(t *testing.T) {
	for name, ok := range map[string]bool{"laravel/boost": true, "spatie/laravel-permission": true, "a/b": true, "Laravel/Boost": false, "rm -rf/x": false, "laravel": false, "x/y; ls": false} {
		if validPackage.MatchString(name) != ok {
			t.Errorf("%q valid=%v, want %v", name, !ok, ok)
		}
	}
}
