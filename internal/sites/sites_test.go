package sites

import (
	"os"
	"path/filepath"
	"testing"

	"laramonster/internal/config"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"My Shop": "my-shop", "Blog": "blog", "__x__": "x", "café!": "caf", "a.b": "a-b"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListParkLinkAndDocRoot(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "parked", "Blog", "public"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "parked", "plain"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "parked", ".hidden"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "parked", "Blog", "artisan"), nil, 0o644))
	must(t, os.MkdirAll(filepath.Join(dir, "elsewhere"), 0o755))

	c := config.Default()
	c.Paths = []string{filepath.Join(dir, "parked")}
	c.Links["api"] = filepath.Join(dir, "elsewhere")
	c.Isolated["plain"] = "8.3"
	c.Secured["blog"] = true

	list := List(c, "8.4")
	if len(list) != 3 {
		t.Fatalf("want 3 sites, got %+v", list)
	}
	blog, _ := Find(list, "blog")
	if blog.Root != filepath.Join(dir, "parked", "Blog", "public") || !blog.Laravel || blog.PHP != "8.4" {
		t.Errorf("blog: %+v", blog)
	}
	if blog.URL != "https://blog.localhost:8443" {
		t.Errorf("blog url %s", blog.URL)
	}
	plain, _ := Find(list, "plain")
	if plain.PHP != "8.3" || plain.Root != plain.Path {
		t.Errorf("plain: %+v", plain)
	}
	if s, ok := ForPath(list, filepath.Join(dir, "parked", "Blog", "app", "Http")); !ok || s.Name != "blog" {
		t.Errorf("ForPath: %+v %v", s, ok)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
