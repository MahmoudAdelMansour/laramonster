package account

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeServer answers like monstersgate.com; next decides the /status reply.
func fakeServer(t *testing.T, next *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ok := map[string]any{"account": map[string]string{"name": "Sara", "email": "sara@example.com"},
			"license": map[string]any{"key": "LM-AAAA-••••-••••-DDDD", "status": "active", "term": "1 year", "perpetual": false}}
		switch r.URL.Path {
		case "/api/laramonster/activate":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != "right" {
				w.WriteHeader(401)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "invalid_credentials", "message": "The email or password is wrong."})
				return
			}
			ok["token"] = "tok"
			ok["replaced"] = map[string]string{"device_name": "old-laptop"}
			_ = json.NewEncoder(w).Encode(ok)
		case "/api/laramonster/status":
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("status without the token")
			}
			switch *next {
			case 401:
				w.WriteHeader(401)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "replaced", "message": "Signed in elsewhere.", "replaced_by": map[string]string{"device_name": "home-pc"}})
			case 403:
				w.WriteHeader(403)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "expired", "message": "Your license has expired."})
			default:
				_ = json.NewEncoder(w).Encode(ok)
			}
		case "/api/laramonster/sign-out":
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
}

func newClient(t *testing.T, url string) *Client {
	t.Setenv("LARAMONSTER_HOME", t.TempDir())
	t.Setenv("LARAMONSTER_ACCOUNT_URL", url)
	return New()
}

func TestSignInChecksAndSignOut(t *testing.T) {
	next := 200
	srv := fakeServer(t, &next)
	defer srv.Close()
	c := newClient(t, srv.URL)

	if c.Pro() || c.Status().State != "none" {
		t.Fatalf("starts without Pro: %+v", c.Status())
	}
	if _, err := c.SignIn("sara@example.com", "wrong", ""); err == nil || err.(*ProblemError).Code != "invalid_credentials" {
		t.Fatalf("wrong password: %v", err)
	}
	replaced, err := c.SignIn("sara@example.com", "right", "")
	if err != nil || replaced == nil || replaced.Name != "old-laptop" {
		t.Fatalf("sign in: %v %+v", err, replaced)
	}
	if !c.Pro() || c.Status().Account.Email != "sara@example.com" {
		t.Fatalf("signed in: %+v", c.Status())
	}

	// The state survives a restart.
	if !New().Pro() {
		t.Fatal("state not saved")
	}

	next = 403
	_ = c.Check()
	if s := c.Status(); s.Pro || s.State != "expired" {
		t.Fatalf("expired: %+v", s)
	}
	next = 200
	_ = c.Check()
	if !c.Pro() {
		t.Fatal("renewal should bring Pro back")
	}

	next = 401
	_ = c.Check()
	s := c.Status()
	if s.Pro || s.SignedIn || s.State != "replaced" || s.ReplacedBy == nil || s.ReplacedBy.Name != "home-pc" {
		t.Fatalf("replaced: %+v", s)
	}

	_, _ = c.SignIn("sara@example.com", "right", "")
	c.SignOut()
	if s := c.Status(); s.SignedIn || s.State != "none" {
		t.Fatalf("signed out: %+v", s)
	}
}

func TestOfflineGrace(t *testing.T) {
	next := 200
	srv := fakeServer(t, &next)
	c := newClient(t, srv.URL)
	if _, err := c.SignIn("sara@example.com", "right", ""); err != nil {
		t.Fatal(err)
	}
	srv.Close() // offline from here

	_ = c.Check()
	if s := c.Status(); !s.Pro || s.Message == "" {
		t.Fatalf("offline but within grace: %+v", s)
	}
	c.now = func() time.Time { return time.Now().Add(GraceOffline + time.Hour) }
	if s := c.Status(); s.Pro || s.State != "offline" {
		t.Fatalf("grace over: %+v", s)
	}
}

func TestDeviceIDIsStable(t *testing.T) {
	t.Setenv("LARAMONSTER_HOME", t.TempDir())
	a, b := DeviceID(), DeviceID()
	if a != b || len(a) < 16 {
		t.Fatalf("device id %q %q", a, b)
	}
}
