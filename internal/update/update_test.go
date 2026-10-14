package update

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true},
		{"0.10.0", "0.9.3", true},
		{"v1.0.0", "0.99.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.2.0", "0.3.0", false},
		{"0.3.0", "0.3.0-beta.1", true},
		{"0.3.0-beta.1", "0.3.0", false},
		{"0.1.0-beta.6", "0.1.0-beta.5", true},
		{"0.1.0-beta.10", "0.1.0-beta.9", true},
		{"0.1.0-beta.5", "0.1.0-beta.5", false},
		{"0.1.0-rc.1", "0.1.0-beta.9", true},
		{"0.2.0-beta.1", "0.1.9", true},
		{"0.2.0+build.7", "0.2.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.3.0","notes_url":"https://n","page_url":"https://p","downloads":{"linux":{"url":"https://dl/linux.tar.gz"}}}`))
	}))
	defer srv.Close()

	if i := fetch(srv.URL, "v0.2.0", "linux"); !i.Available || i.Latest != "0.3.0" || i.DownloadURL != "https://dl/linux.tar.gz" {
		t.Errorf("newer release on linux: %+v", i)
	}
	if i := fetch(srv.URL, "v0.2.0", "macos"); i.DownloadURL != "https://p" {
		t.Errorf("no macOS asset should fall back to the downloads page: %+v", i)
	}
	if i := fetch(srv.URL, "0.1.0-dev", "linux"); i.Available {
		t.Errorf("dev builds never report updates: %+v", i)
	}
	if i := fetch(srv.URL, "v0.2.0-3-g660226a", "linux"); i.Available {
		t.Errorf("builds between tags are dev builds: %+v", i)
	}
	if i := fetch(srv.URL, "v0.3.0-beta.2", "linux"); !i.Available {
		t.Errorf("the release is newer than its betas: %+v", i)
	}
	if i := fetch(srv.URL, "v0.3.0", "linux"); i.Available {
		t.Errorf("same version is not an update: %+v", i)
	}
	if i := fetch("http://127.0.0.1:1/none", "v0.1.0", "linux"); i.Available || i.Error == "" {
		t.Errorf("offline: no update, and Settings says why: %+v", i)
	}
	none := httptest.NewServer(http.NotFoundHandler())
	defer none.Close()
	if i := fetch(none.URL, "v0.1.0", "linux"); i.Available || i.Error != "" {
		t.Errorf("no release yet is simply up to date: %+v", i)
	}
}
