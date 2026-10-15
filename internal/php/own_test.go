//go:build !windows

package php

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOwnReleasesPicksNewestCompletePatch(t *testing.T) {
	platform, err := staticPlatform()
	if err != nil {
		t.Skip(err)
	}
	asset := func(name string) string {
		return fmt.Sprintf(`{"name":%q,"browser_download_url":"https://example.test/%s","digest":"sha256:ab12"}`, name, name)
	}
	body := `{"assets":[` +
		asset("php-8.4.20-cli-"+platform+".tar.gz") + "," + asset("php-8.4.20-fpm-"+platform+".tar.gz") + "," +
		asset("php-8.4.21-cli-"+platform+".tar.gz") + "," + // no fpm yet: not offered
		asset("php-8.5.1-cli-other-arch.tar.gz") + `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	old := OwnBuilds
	OwnBuilds = srv.URL
	defer func() { OwnBuilds = old }()

	got, err := ownReleases()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Full != "8.4.20" || len(got[0].urls) != 2 || got[0].sums[0] != "ab12" {
		t.Fatalf("ownReleases = %+v", got)
	}
}
