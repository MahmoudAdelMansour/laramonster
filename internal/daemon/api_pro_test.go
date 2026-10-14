package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProEndpointsAreLockedWithoutALicense(t *testing.T) {
	t.Setenv("LARAMONSTER_HOME", t.TempDir())
	t.Setenv("LARAMONSTER_ACCOUNT_URL", "http://127.0.0.1:1") // nothing listens: no license
	h := NewRuntime().Handler(func() {})

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(HeaderName, "1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{"/api/pro/sites", "/api/pro/events?site=x", "/api/pro/jobs?site=x", "/api/pro/queues?site=x"} {
		rec := get(path)
		if rec.Code != http.StatusPaymentRequired {
			t.Errorf("%s: got %d, want 402", path, rec.Code)
			continue
		}
		var body struct {
			Error   string `json:"error"`
			Account struct {
				Pro   bool   `json:"pro"`
				State string `json:"state"`
			} `json:"account"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != "pro_required" || body.Account.Pro || body.Account.State != "none" {
			t.Errorf("%s: body %s", path, rec.Body.String())
		}
	}

	if rec := get("/api/account"); rec.Code != http.StatusOK {
		t.Errorf("/api/account should always answer, got %d", rec.Code)
	}
}

func TestSplitArgs(t *testing.T) {
	got, err := splitArgs(`queue:retry all --queue="high priority" 'a b'`)
	want := []string{"queue:retry", "all", "--queue=high priority", "a b"}
	if err != nil || len(got) != len(want) {
		t.Fatalf("%q %v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d: %q, want %q", i, got[i], want[i])
		}
	}
	if _, err := splitArgs(`"open`); err == nil {
		t.Error("an unclosed quote should be an error")
	}
}
