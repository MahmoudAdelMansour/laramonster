package webhook

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignThenVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	env := map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_test", "GITHUB_WEBHOOK_SECRET": "gh", "PADDLE_WEBHOOK_SECRET": "pd",
		"SHOPIFY_WEBHOOK_SECRET": "sh", "LEMON_SQUEEZY_SIGNING_SECRET": "ls", "SLACK_SIGNING_SECRET": "sl", "CLERK_WEBHOOK_SECRET": "whsec_dGVzdHNlY3JldA==",
		"GITLAB_WEBHOOK_SECRET": "gl", "WEBHOOK_CLIENT_SECRET": "sp"}
	for _, p := range Providers {
		h := http.Header{}
		body := []byte(`{"type":"x"}`)
		if !Sign(p, h, body, env, now) {
			continue
		}
		if c := Verify(p, h, body, env, now); c.State != "valid" {
			t.Errorf("%s: signed request verifies as %s (%s)", p.ID, c.State, c.Detail)
		}
		if c := Verify(p, h, []byte(`{"type":"changed"}`), env, now); p.ID != "gitlab" && c.State != "invalid" {
			t.Errorf("%s: a changed body must not verify, got %s", p.ID, c.State)
		}
	}
	h := http.Header{}
	Sign(Providers[0], h, []byte("{}"), env, now)
	if c := Verify(Providers[0], h, []byte("{}"), env, now.Add(10*time.Minute)); c.State != "valid" || !strings.Contains(c.Detail, "5 minutes") {
		t.Errorf("an old Stripe signature should verify with a warning: %+v", c)
	}
	if c := Verify(Providers[0], h, []byte("{}"), map[string]string{}, now); c.State != "no_secret" || c.Env != "STRIPE_WEBHOOK_SECRET" {
		t.Errorf("no secret: %+v", c)
	}
}

func TestDetectAndMatch(t *testing.T) {
	routes := []Route{
		{Methods: []string{"POST"}, URI: "/stripe/webhook", Name: "cashier.webhook", CSRF: true},
		{Methods: []string{"POST"}, URI: "/webhooks/{provider}", Action: `App\Http\Controllers\HookController@handle`},
		{Methods: []string{"POST"}, URI: "/login"},
		{Methods: []string{"GET"}, URI: "/github/webhook"},
	}
	found := Detect(routes, map[string]string{"STRIPE_WEBHOOK_SECRET": "x"})
	if len(found) != 2 || found[0].Provider != "stripe" || !found[0].SecretSet || found[1].Provider != "" {
		t.Fatalf("detect: %+v", found)
	}
	if !Match("/webhooks/{provider}", "/webhooks/paddle") || Match("/webhooks/{provider}", "/webhooks/a/b") {
		t.Error("pattern matching")
	}
	d := Diagnose(419, 40, Providers[0], &found[0], true, "/stripe/webhook")
	if d[0].Tone != "bad" || !strings.Contains(d[0].Fix, "'stripe/webhook'") {
		t.Errorf("diagnose 419: %+v", d)
	}
	if d := Diagnose(200, 50, Providers[0], &found[1], true, "/webhooks/x"); d[0].Tone != "good" {
		t.Errorf("diagnose 200: %+v", d)
	}
	for _, s := range []string{"stripe", "github", ""} {
		for _, sm := range Samples(s) {
			if _, _, _, err := Build(sm.Provider, sm.Event, nil, time.Now()); err != nil {
				t.Error(err)
			}
		}
	}
}
