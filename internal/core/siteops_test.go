package core

import (
	"os"
	"path/filepath"
	"testing"

	"laramonster/internal/config"
)

func TestRenameMovesSettingsAndAppURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LARAMONSTER_HOME", filepath.Join(home, "lm"))
	parked := filepath.Join(home, "code")
	app := filepath.Join(parked, "build-feed")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(app, ".env"), []byte("APP_NAME=Feed\nAPP_URL=http://build-feed.localhost:8080\n"), 0o600)
	if _, err := Park(parked); err != nil {
		t.Fatal(err)
	}
	if err := SetEnabled("build-feed", false); err != nil {
		t.Fatal(err)
	}

	res, err := Rename("build-feed", "Asdasd")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "asdasd" || res.URL != "http://asdasd.localhost:8080" || !res.AppURLUpdate {
		t.Fatalf("unexpected result %+v", res)
	}
	c, _ := config.Load()
	if c.Links["asdasd"] != app || !c.Disabled["asdasd"] || c.Disabled["build-feed"] {
		t.Fatalf("settings did not move: links=%v disabled=%v", c.Links, c.Disabled)
	}
	env, _ := os.ReadFile(filepath.Join(app, ".env"))
	if v, _ := EnvValue(string(env), "APP_URL"); v != res.URL {
		t.Fatalf("APP_URL = %q", v)
	}
	if fi, _ := os.Stat(filepath.Join(app, ".env")); fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env permissions changed to %v", fi.Mode().Perm())
	}

	// Back to the folder name: served from the parked dir again, no link.
	if _, err := Rename("asdasd", "build-feed"); err != nil {
		t.Fatal(err)
	}
	c, _ = config.Load()
	if len(c.Links) != 0 || !c.Disabled["build-feed"] {
		t.Fatalf("expected no links and the site still off: %v %v", c.Links, c.Disabled)
	}
	if _, err := Rename("build-feed", "bad name!"); err != nil {
		t.Fatalf("names are slugged, got %v", err)
	}
}

func TestNormalizeProxy(t *testing.T) {
	for in, want := range map[string]string{
		"3000":                  "http://127.0.0.1:3000",
		"localhost:5173":        "http://localhost:5173",
		"http://127.0.0.1:8000": "http://127.0.0.1:8000",
		"https://shop.test/":    "https://shop.test",
		"shop.test":             "http://shop.test",
	} {
		if got, err := NormalizeProxy(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"ftp://x", "http://x/path", "http://"} {
		if _, err := NormalizeProxy(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestEnvValueAndSet(t *testing.T) {
	in := "# app\nAPP_NAME=\"My App\" \nexport DB_HOST=127.0.0.1 # local\nEMPTY=\n"
	if v, _ := EnvValue(in, "APP_NAME"); v != "My App" {
		t.Errorf("APP_NAME = %q", v)
	}
	if v, _ := EnvValue(in, "DB_HOST"); v != "127.0.0.1" {
		t.Errorf("DB_HOST = %q", v)
	}
	out := SetEnvValue(in, "APP_NAME", `Say "hi"`)
	if v, _ := EnvValue(out, "APP_NAME"); v != `Say \"hi\"` {
		t.Errorf("round trip = %q", v)
	}
	if out = SetEnvValue(out, "NEW", "x"); out[len(out)-6:] != "NEW=x\n" {
		t.Errorf("append: %q", out)
	}
}
