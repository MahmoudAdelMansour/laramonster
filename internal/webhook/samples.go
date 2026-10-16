package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Sample is a test event LaraMonster can send to a route.
type Sample struct {
	Provider string `json:"provider"`
	Event    string `json:"event"`
	Label    string `json:"label"`
}

// Samples lists the test events of a provider ("" = generic JSON).
func Samples(provider string) []Sample {
	var out []Sample
	for _, s := range samples {
		if s.Provider == provider {
			out = append(out, s.Sample)
		}
	}
	if len(out) == 0 {
		for _, s := range samples {
			if s.Provider == "" {
				out = append(out, s.Sample)
			}
		}
	}
	return out
}

type sample struct {
	Sample
	body    func(now time.Time) map[string]any
	headers map[string]string
}

func stripeEvent(typ string, object map[string]any) func(time.Time) map[string]any {
	return func(now time.Time) map[string]any {
		return map[string]any{
			"id": fmt.Sprintf("evt_laramonster_%d", now.UnixNano()), "object": "event", "api_version": "2024-06-20",
			"created": now.Unix(), "livemode": false, "type": typ, "pending_webhooks": 1,
			"data": map[string]any{"object": object},
		}
	}
}

var samples = []sample{
	{Sample: Sample{"stripe", "checkout.session.completed", "Checkout completed"}, body: stripeEvent("checkout.session.completed", map[string]any{
		"id": "cs_test_laramonster", "object": "checkout.session", "mode": "payment", "payment_status": "paid", "status": "complete",
		"amount_total": 2000, "currency": "usd", "customer": "cus_test_laramonster", "customer_details": map[string]any{"email": "customer@example.com", "name": "Test Customer"},
		"metadata": map[string]any{}, "client_reference_id": nil})},
	{Sample: Sample{"stripe", "payment_intent.succeeded", "Payment succeeded"}, body: stripeEvent("payment_intent.succeeded", map[string]any{
		"id": "pi_test_laramonster", "object": "payment_intent", "amount": 2000, "amount_received": 2000, "currency": "usd", "status": "succeeded",
		"customer": "cus_test_laramonster", "metadata": map[string]any{}})},
	{Sample: Sample{"stripe", "customer.subscription.updated", "Subscription updated"}, body: stripeEvent("customer.subscription.updated", map[string]any{
		"id": "sub_test_laramonster", "object": "subscription", "status": "active", "customer": "cus_test_laramonster", "cancel_at_period_end": false,
		"items": map[string]any{"object": "list", "data": []any{map[string]any{"id": "si_test", "price": map[string]any{"id": "price_test", "product": "prod_test"}, "quantity": 1}}}})},
	{Sample: Sample{"stripe", "invoice.payment_failed", "Invoice payment failed"}, body: stripeEvent("invoice.payment_failed", map[string]any{
		"id": "in_test_laramonster", "object": "invoice", "status": "open", "customer": "cus_test_laramonster", "amount_due": 2000, "currency": "usd", "subscription": "sub_test_laramonster"})},

	{Sample: Sample{"github", "ping", "Ping"}, headers: map[string]string{"X-GitHub-Event": "ping"}, body: func(time.Time) map[string]any {
		return map[string]any{"zen": "Keep it logically awesome.", "hook_id": 1, "repository": map[string]any{"full_name": "octocat/hello-world"}}
	}},
	{Sample: Sample{"github", "push", "Push"}, headers: map[string]string{"X-GitHub-Event": "push"}, body: func(now time.Time) map[string]any {
		return map[string]any{"ref": "refs/heads/main", "before": strings.Repeat("0", 40), "after": strings.Repeat("a", 40),
			"repository": map[string]any{"full_name": "octocat/hello-world", "default_branch": "main"}, "pusher": map[string]any{"name": "octocat"},
			"commits": []any{map[string]any{"id": strings.Repeat("a", 40), "message": "Test commit from LaraMonster", "timestamp": now.Format(time.RFC3339)}}}
	}},
	{Sample: Sample{"github", "pull_request.opened", "Pull request opened"}, headers: map[string]string{"X-GitHub-Event": "pull_request"}, body: func(time.Time) map[string]any {
		return map[string]any{"action": "opened", "number": 1, "pull_request": map[string]any{"title": "Test pull request", "state": "open"}, "repository": map[string]any{"full_name": "octocat/hello-world"}}
	}},

	{Sample: Sample{"paddle", "transaction.completed", "Transaction completed"}, body: func(now time.Time) map[string]any {
		return map[string]any{"event_id": "evt_laramonster", "event_type": "transaction.completed", "occurred_at": now.Format(time.RFC3339),
			"data": map[string]any{"id": "txn_laramonster", "status": "completed", "customer_id": "ctm_laramonster", "currency_code": "USD"}}
	}},
	{Sample: Sample{"shopify", "orders/create", "Order created"}, headers: map[string]string{"X-Shopify-Topic": "orders/create", "X-Shopify-Shop-Domain": "laramonster-test.myshopify.com"}, body: func(time.Time) map[string]any {
		return map[string]any{"id": 820982911946154508, "email": "customer@example.com", "total_price": "20.00", "currency": "USD", "line_items": []any{}}
	}},
	{Sample: Sample{"lemonsqueezy", "order_created", "Order created"}, headers: map[string]string{"X-Event-Name": "order_created"}, body: func(time.Time) map[string]any {
		return map[string]any{"meta": map[string]any{"event_name": "order_created"}, "data": map[string]any{"type": "orders", "id": "1", "attributes": map[string]any{"status": "paid", "total": 2000}}}
	}},
	{Sample: Sample{"svix", "user.created", "User created"}, body: func(time.Time) map[string]any {
		return map[string]any{"type": "user.created", "object": "event", "data": map[string]any{"id": "user_laramonster", "email_addresses": []any{map[string]any{"email_address": "user@example.com"}}}}
	}},
	{Sample: Sample{"gitlab", "Push Hook", "Push"}, headers: map[string]string{"X-Gitlab-Event": "Push Hook"}, body: func(time.Time) map[string]any {
		return map[string]any{"object_kind": "push", "ref": "refs/heads/main", "project": map[string]any{"path_with_namespace": "group/project"}}
	}},
	{Sample: Sample{"", "test", "Test JSON event"}, body: func(now time.Time) map[string]any {
		return map[string]any{"type": "test", "id": fmt.Sprintf("laramonster_%d", now.Unix()), "created_at": now.Format(time.RFC3339), "data": map[string]any{"message": "Hello from LaraMonster"}}
	}},
}

// Build makes a test request body and headers for a sample, signed with
// the secret from .env when there is one. signed reports whether it is.
func Build(provider, event string, env map[string]string, now time.Time) (body []byte, h http.Header, signed bool, err error) {
	var s *sample
	for i := range samples {
		if samples[i].Provider == provider && samples[i].Event == event {
			s = &samples[i]
		}
	}
	if s == nil {
		for i := range samples {
			if samples[i].Provider == "" {
				s = &samples[i]
			}
		}
	}
	body, err = json.MarshalIndent(s.body(now), "", "  ")
	if err != nil {
		return nil, nil, false, err
	}
	h = http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "LaraMonster webhook test")
	for k, v := range s.headers {
		h.Set(k, v)
	}
	if p, ok := Get(provider); ok {
		signed = Sign(p, h, body, env, now)
	}
	return body, h, signed, nil
}

// Diagnosis explains an app's answer to a webhook in plain words.
type Diagnosis struct {
	Tone  string `json:"tone"` // good, warn, bad
	Title string `json:"title"`
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"` // code to paste
}

// Diagnose reads the status, timing and route facts of a delivery.
func Diagnose(status int, ms float64, p Provider, route *Found, laravel11 bool, path string) []Diagnosis {
	var out []Diagnosis
	except := strings.TrimPrefix(path, "/")
	if route != nil {
		except = strings.TrimPrefix(Pattern(route.URI).String(), "^/")
		except = strings.TrimSuffix(strings.TrimSuffix(except, "/?$"), "$")
		except = strings.ReplaceAll(strings.ReplaceAll(except, `[^/]+`, "*"), `\`, "")
	}
	csrfFix := "// app/Http/Middleware/VerifyCsrfToken.php\nprotected $except = [\n    '" + except + "',\n];"
	if laravel11 {
		csrfFix = "// bootstrap/app.php\n->withMiddleware(function (Middleware $middleware) {\n    $middleware->validateCsrfTokens(except: [\n        '" + except + "',\n    ]);\n})"
	}
	switch {
	case status == 419:
		out = append(out, Diagnosis{"bad", "Rejected by Laravel's CSRF check (419)", "Providers can't send a CSRF token. Exclude the webhook URL from the check.", csrfFix})
	case status == 404:
		out = append(out, Diagnosis{"bad", "No route for this URL (404)", "Check the URL in the provider's dashboard against your routes (php artisan route:list). The route may be under /api.", ""})
	case status == 405:
		out = append(out, Diagnosis{"bad", "Wrong HTTP method (405)", "Webhooks arrive as POST: register the route with Route::post().", ""})
	case status == 401 || status == 403:
		out = append(out, Diagnosis{"bad", fmt.Sprintf("Refused by the app (%d)", status), "Usually the signature check failed (see Signature) or the route is behind auth middleware. Webhook routes must not require a logged-in user.", ""})
	case status == 301 || status == 302 || status == 307 || status == 308:
		out = append(out, Diagnosis{"bad", fmt.Sprintf("Redirected (%d)", status), "Providers don't follow redirects. A redirect to /login means the route sits behind auth middleware; one to https:// or another host means the URL in the provider's dashboard is outdated.", ""})
	case status >= 500:
		out = append(out, Diagnosis{"bad", fmt.Sprintf("The app crashed (%d)", status), "The provider will retry it later. Turn on the Debugger for this site to see the exception and the line it came from.", ""})
	case status >= 400:
		out = append(out, Diagnosis{"warn", fmt.Sprintf("The app answered %d", status), "Providers treat any 4xx as failed delivery and retry it.", ""})
	case status >= 200 && status < 300:
		out = append(out, Diagnosis{"good", fmt.Sprintf("Accepted (%d)", status), "The provider counts this delivery as done.", ""})
	case status == 0:
		out = append(out, Diagnosis{"bad", "No answer", "The request didn't finish (the app may have timed out or the connection closed).", ""})
	}
	if route != nil && route.CSRF && status != 419 && status != 0 {
		out = append(out, Diagnosis{"warn", "The CSRF check still covers this route", "It passed this time, but real deliveries from the provider will be refused with 419.", csrfFix})
	}
	if route != nil && route.Auth {
		out = append(out, Diagnosis{"warn", "The route needs a logged-in user", "Providers never log in. Move the route out of the auth middleware group.", ""})
	}
	if p.Timeout > 0 && ms > float64(p.Timeout.Milliseconds())*0.7 {
		out = append(out, Diagnosis{"warn", fmt.Sprintf("Slow answer: %.1f s", ms/1000), fmt.Sprintf("%s gives up after %d seconds and retries, so the same event may arrive twice. Answer right away and do the work in a queued job.", p.Name, int(p.Timeout.Seconds())), ""})
	} else if ms > 3000 {
		out = append(out, Diagnosis{"warn", fmt.Sprintf("Slow answer: %.1f s", ms/1000), "Most providers wait 5 to 30 seconds. Answer right away and do the work in a queued job.", ""})
	}
	return out
}
