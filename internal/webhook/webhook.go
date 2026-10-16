// Package webhook knows the webhook providers a Laravel app talks to: how
// to recognise their requests and routes, which .env key holds the signing
// secret, how to check a signature, and how to sign a test event.
package webhook

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Provider describes one webhook sender.
type Provider struct {
	ID    string
	Name  string
	Words []string // in a route's URI, name or controller
	Env   []string // .env keys that usually hold the signing secret
	// Header that only this provider sends.
	Header string
	// Timeout is how long the provider waits for an answer.
	Timeout time.Duration
}

var Providers = []Provider{
	{ID: "stripe", Name: "Stripe", Words: []string{"stripe", "cashier"}, Env: []string{"STRIPE_WEBHOOK_SECRET", "CASHIER_WEBHOOK_SECRET"}, Header: "Stripe-Signature", Timeout: 20 * time.Second},
	{ID: "github", Name: "GitHub", Words: []string{"github"}, Env: []string{"GITHUB_WEBHOOK_SECRET"}, Header: "X-Github-Event", Timeout: 10 * time.Second},
	{ID: "paddle", Name: "Paddle", Words: []string{"paddle"}, Env: []string{"PADDLE_WEBHOOK_SECRET"}, Header: "Paddle-Signature", Timeout: 5 * time.Second},
	{ID: "shopify", Name: "Shopify", Words: []string{"shopify"}, Env: []string{"SHOPIFY_WEBHOOK_SECRET", "SHOPIFY_API_SECRET"}, Header: "X-Shopify-Hmac-Sha256", Timeout: 5 * time.Second},
	{ID: "lemonsqueezy", Name: "Lemon Squeezy", Words: []string{"lemon"}, Env: []string{"LEMON_SQUEEZY_SIGNING_SECRET"}, Header: "X-Event-Name", Timeout: 10 * time.Second},
	{ID: "slack", Name: "Slack", Words: []string{"slack"}, Env: []string{"SLACK_SIGNING_SECRET"}, Header: "X-Slack-Signature", Timeout: 3 * time.Second},
	{ID: "svix", Name: "Svix (Clerk, Resend…)", Words: []string{"clerk", "resend", "svix"}, Env: []string{"CLERK_WEBHOOK_SECRET", "RESEND_WEBHOOK_SECRET", "SVIX_WEBHOOK_SECRET"}, Header: "Svix-Id", Timeout: 15 * time.Second},
	{ID: "gitlab", Name: "GitLab", Words: []string{"gitlab"}, Env: []string{"GITLAB_WEBHOOK_SECRET", "GITLAB_WEBHOOK_TOKEN"}, Header: "X-Gitlab-Event", Timeout: 10 * time.Second},
	{ID: "paypal", Name: "PayPal", Words: []string{"paypal", "ipn"}, Header: "Paypal-Transmission-Id", Timeout: 30 * time.Second},
	{ID: "twilio", Name: "Twilio", Words: []string{"twilio", "sms"}, Env: []string{"TWILIO_AUTH_TOKEN"}, Header: "X-Twilio-Signature", Timeout: 15 * time.Second},
	{ID: "mailgun", Name: "Mailgun", Words: []string{"mailgun"}, Env: []string{"MAILGUN_WEBHOOK_SIGNING_KEY"}, Timeout: 10 * time.Second},
	{ID: "postmark", Name: "Postmark", Words: []string{"postmark"}, Timeout: 10 * time.Second},
	{ID: "telegram", Name: "Telegram", Words: []string{"telegram"}, Env: []string{"TELEGRAM_WEBHOOK_SECRET"}, Header: "X-Telegram-Bot-Api-Secret-Token", Timeout: 60 * time.Second},
	{ID: "spatie", Name: "Webhook client (spatie)", Env: []string{"WEBHOOK_CLIENT_SECRET"}, Header: "Signature", Timeout: 10 * time.Second},
}

func Get(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

var hookWords = regexp.MustCompile(`(?i)(web-?hooks?|hooks?\b|callback|ipn|notif(y|ication)|incoming)`)

// Route is a route as the runner lists it.
type Route struct {
	Methods    []string `json:"methods"`
	URI        string   `json:"uri"`
	Name       string   `json:"name"`
	Action     string   `json:"action"`
	Middleware []string `json:"middleware"`
	Auth       bool     `json:"auth"`
	CSRF       bool     `json:"csrf"`
}

// Found is a route that receives webhooks, with what LaraMonster sees.
type Found struct {
	Route
	Provider  string `json:"provider,omitempty"`
	Name      string `json:"provider_name,omitempty"`
	SecretEnv string `json:"secret_env,omitempty"` // the .env key that holds the secret
	SecretSet bool   `json:"secret_set"`
	Custom    bool   `json:"custom,omitempty"` // added by the person, not detected
}

// Detect picks the routes that look like webhook receivers: POST routes
// named or handled like one, or by a known webhook package.
func Detect(routes []Route, env map[string]string) []Found {
	var out []Found
	for _, r := range routes {
		if !accepts(r, "POST") {
			continue
		}
		text := strings.ToLower(r.URI + " " + r.Name + " " + r.Action)
		p, known := providerOf(text)
		isPkg := strings.Contains(r.Action, `Spatie\WebhookClient`) || strings.Contains(r.Action, `Cashier\Http\Controllers\WebhookController`) ||
			strings.Contains(r.Action, `Laravel\Paddle`)
		if !known && !isPkg && !hookWords.MatchString(text) {
			continue
		}
		if !known && strings.Contains(r.Action, `Spatie\WebhookClient`) {
			p, known = Get("spatie")
		}
		f := Found{Route: r}
		if known {
			f.Provider, f.Name = p.ID, p.Name
			f.SecretEnv, f.SecretSet = secretOf(p, env)
		}
		out = append(out, f)
	}
	return out
}

// Custom describes a path the person asked to watch.
func Custom(path string, routes []Route, env map[string]string) Found {
	f := Found{Route: Route{Methods: []string{"POST"}, URI: path}, Custom: true}
	for _, r := range routes {
		if Match(r.URI, path) {
			f.Route = r
			break
		}
	}
	if p, ok := providerOf(strings.ToLower(f.URI + " " + f.Route.Name + " " + f.Action)); ok {
		f.Provider, f.Name = p.ID, p.Name
		f.SecretEnv, f.SecretSet = secretOf(p, env)
	}
	return f
}

func accepts(r Route, method string) bool {
	for _, m := range r.Methods {
		if m == method || m == "ANY" {
			return true
		}
	}
	return false
}

func providerOf(text string) (Provider, bool) {
	for _, p := range Providers {
		for _, w := range p.Words {
			if strings.Contains(text, w) {
				return p, true
			}
		}
	}
	return Provider{}, false
}

func secretOf(p Provider, env map[string]string) (string, bool) {
	for _, k := range p.Env {
		if v, ok := env[k]; ok {
			return k, strings.TrimSpace(v) != ""
		}
	}
	if len(p.Env) > 0 {
		return p.Env[0], false
	}
	return "", false
}

// Secret returns the provider's signing secret from .env ("" if not set).
func Secret(p Provider, env map[string]string) (string, string) {
	for _, k := range p.Env {
		if v := strings.TrimSpace(env[k]); v != "" {
			return k, v
		}
	}
	return "", ""
}

var param = regexp.MustCompile(`\\\{[^}]*\\\}`)

// Pattern turns a route URI (with {params}) into a regexp for paths.
func Pattern(uri string) *regexp.Regexp {
	q := regexp.QuoteMeta("/" + strings.Trim(uri, "/"))
	q = param.ReplaceAllString(q, `[^/]+`)
	return regexp.MustCompile("^" + q + "/?$")
}

// Match reports whether a request path is served by a route URI.
func Match(uri, path string) bool { return Pattern(uri).MatchString(path) }

// ── recognising a request ──

// Identify names the provider of a request and its event type.
func Identify(h http.Header, body []byte, hint string) (Provider, string) {
	for _, p := range Providers {
		if p.Header != "" && h.Get(p.Header) != "" && p.ID != "spatie" {
			return p, eventOf(p.ID, h, body)
		}
	}
	if h.Get("Signature") != "" {
		p, _ := Get("spatie")
		if hint != "" {
			if hp, ok := Get(hint); ok {
				p = hp
			}
		}
		return p, eventOf(p.ID, h, body)
	}
	if hint != "" {
		if p, ok := Get(hint); ok {
			return p, eventOf(p.ID, h, body)
		}
	}
	return Provider{}, eventOf("", h, body)
}

func eventOf(id string, h http.Header, body []byte) string {
	switch id {
	case "github":
		ev := h.Get("X-Github-Event")
		var b struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(body, &b) == nil && b.Action != "" {
			ev += "." + b.Action
		}
		return ev
	case "shopify":
		return h.Get("X-Shopify-Topic")
	case "lemonsqueezy":
		return h.Get("X-Event-Name")
	case "gitlab":
		return h.Get("X-Gitlab-Event")
	}
	var b map[string]any
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	for _, k := range []string{"type", "event_type", "event", "eventType", "topic"} {
		if v, ok := b[k].(string); ok && v != "" {
			return v
		}
	}
	if meta, ok := b["meta"].(map[string]any); ok {
		if v, ok := meta["event_name"].(string); ok {
			return v
		}
	}
	return ""
}

// ── signatures ──

// Check is what LaraMonster says about a request's signature.
type Check struct {
	State  string `json:"state"`  // valid, invalid, no_secret, no_signature, unsupported
	Detail string `json:"detail"` // in plain words
	Env    string `json:"env,omitempty"`
	AgeSec int    `json:"age_sec,omitempty"` // how old the signed timestamp is
}

func mac(h func() hash.Hash, key, msg []byte) []byte {
	m := hmac.New(h, key)
	m.Write(msg)
	return m.Sum(nil)
}

// Verify checks a request's signature with the secret from .env.
func Verify(p Provider, h http.Header, body []byte, env map[string]string, at time.Time) Check {
	if p.ID == "" {
		return Check{State: "unsupported", Detail: "LaraMonster doesn't know who sent this, so it can't check a signature."}
	}
	key, secret := Secret(p, env)
	need := func() Check {
		k := ""
		if len(p.Env) > 0 {
			k = p.Env[0]
		}
		return Check{State: "no_secret", Env: k, Detail: fmt.Sprintf("Add the signing secret from %s to .env as %s to check signatures here (your app needs it too).", p.Name, k)}
	}
	ok := func(age int) Check {
		return Check{State: "valid", Env: key, AgeSec: age, Detail: "The signature matches " + key + ": the request really comes from " + p.Name + " and wasn't changed."}
	}
	bad := Check{State: "invalid", Env: key, Detail: "The signature doesn't match " + key + ". Your app will reject it: the secret in .env is probably from another endpoint or mode (test vs live)."}
	switch p.ID {
	case "stripe":
		sig := h.Get("Stripe-Signature")
		if sig == "" {
			return Check{State: "no_signature", Detail: "No Stripe-Signature header."}
		}
		if secret == "" {
			return need()
		}
		t, v1 := "", []string{}
		for _, part := range strings.Split(sig, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			switch k {
			case "t":
				t = v
			case "v1":
				v1 = append(v1, v)
			}
		}
		want := hex.EncodeToString(mac(sha256.New, []byte(secret), []byte(t+"."+string(body))))
		for _, v := range v1 {
			if hmac.Equal([]byte(v), []byte(want)) {
				c := ok(age(t, at))
				if c.AgeSec > 300 {
					c.Detail += fmt.Sprintf(" It was signed %s ago: Stripe's library refuses signatures older than 5 minutes, so a replay needs a fresh signature.", ageText(c.AgeSec))
				}
				return c
			}
		}
		return bad
	case "github":
		sig := h.Get("X-Hub-Signature-256")
		if sig == "" {
			return Check{State: "no_signature", Detail: "No X-Hub-Signature-256 header: the webhook has no secret set on GitHub."}
		}
		if secret == "" {
			return need()
		}
		want := "sha256=" + hex.EncodeToString(mac(sha256.New, []byte(secret), body))
		if hmac.Equal([]byte(sig), []byte(want)) {
			return ok(0)
		}
		return bad
	case "shopify":
		if secret == "" {
			return need()
		}
		want := base64.StdEncoding.EncodeToString(mac(sha256.New, []byte(secret), body))
		if hmac.Equal([]byte(h.Get("X-Shopify-Hmac-Sha256")), []byte(want)) {
			return ok(0)
		}
		return bad
	case "paddle":
		sig := h.Get("Paddle-Signature")
		if secret == "" {
			return need()
		}
		ts, h1 := "", ""
		for _, part := range strings.Split(sig, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			if k == "ts" {
				ts = v
			} else if k == "h1" {
				h1 = v
			}
		}
		if hmac.Equal([]byte(h1), []byte(hex.EncodeToString(mac(sha256.New, []byte(secret), []byte(ts+":"+string(body)))))) {
			return ok(age(ts, at))
		}
		return bad
	case "lemonsqueezy":
		if secret == "" {
			return need()
		}
		if hmac.Equal([]byte(h.Get("X-Signature")), []byte(hex.EncodeToString(mac(sha256.New, []byte(secret), body)))) {
			return ok(0)
		}
		return bad
	case "slack":
		if secret == "" {
			return need()
		}
		ts := h.Get("X-Slack-Request-Timestamp")
		want := "v0=" + hex.EncodeToString(mac(sha256.New, []byte(secret), []byte("v0:"+ts+":"+string(body))))
		if hmac.Equal([]byte(h.Get("X-Slack-Signature")), []byte(want)) {
			return ok(age(ts, at))
		}
		return bad
	case "svix":
		if secret == "" {
			return need()
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err != nil {
			return Check{State: "invalid", Env: key, Detail: "The secret in .env isn't a Svix secret (whsec_…)."}
		}
		ts := h.Get("Svix-Timestamp")
		want := base64.StdEncoding.EncodeToString(mac(sha256.New, raw, []byte(h.Get("Svix-Id")+"."+ts+"."+string(body))))
		for _, s := range strings.Fields(h.Get("Svix-Signature")) {
			if _, v, _ := strings.Cut(s, ","); hmac.Equal([]byte(v), []byte(want)) {
				return ok(age(ts, at))
			}
		}
		return bad
	case "gitlab":
		if secret == "" {
			return need()
		}
		if hmac.Equal([]byte(h.Get("X-Gitlab-Token")), []byte(secret)) {
			return ok(0)
		}
		return bad
	case "telegram":
		if secret == "" {
			return need()
		}
		if hmac.Equal([]byte(h.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(secret)) {
			return ok(0)
		}
		return bad
	case "spatie":
		if secret == "" {
			return need()
		}
		if hmac.Equal([]byte(h.Get("Signature")), []byte(hex.EncodeToString(mac(sha256.New, []byte(secret), body)))) {
			return ok(0)
		}
		return bad
	case "twilio":
		return Check{State: "unsupported", Detail: "Twilio signs the full public URL with its parameters; check it in your app with Twilio's RequestValidator."}
	}
	return Check{State: "unsupported", Detail: p.Name + " signatures aren't checked by LaraMonster yet."}
}

func age(ts string, at time.Time) int {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return max(0, int(at.Unix()-n))
}

func ageText(sec int) string {
	switch {
	case sec < 120:
		return fmt.Sprintf("%d seconds", sec)
	case sec < 7200:
		return fmt.Sprintf("%d minutes", sec/60)
	default:
		return fmt.Sprintf("%d hours", sec/3600)
	}
}

// Sign adds a fresh signature for a provider to the headers, as the
// provider would send it now. It reports false when it can't (no secret,
// or a provider that signs differently).
func Sign(p Provider, h http.Header, body []byte, env map[string]string, now time.Time) bool {
	_, secret := Secret(p, env)
	if secret == "" {
		return false
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	switch p.ID {
	case "stripe":
		h.Set("Stripe-Signature", "t="+ts+",v1="+hex.EncodeToString(mac(sha256.New, []byte(secret), []byte(ts+"."+string(body)))))
	case "github":
		h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac(sha256.New, []byte(secret), body)))
		h.Set("X-Hub-Signature", "sha1="+hex.EncodeToString(mac(sha1.New, []byte(secret), body)))
	case "shopify":
		h.Set("X-Shopify-Hmac-Sha256", base64.StdEncoding.EncodeToString(mac(sha256.New, []byte(secret), body)))
	case "paddle":
		h.Set("Paddle-Signature", "ts="+ts+";h1="+hex.EncodeToString(mac(sha256.New, []byte(secret), []byte(ts+":"+string(body)))))
	case "lemonsqueezy":
		h.Set("X-Signature", hex.EncodeToString(mac(sha256.New, []byte(secret), body)))
	case "slack":
		h.Set("X-Slack-Request-Timestamp", ts)
		h.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac(sha256.New, []byte(secret), []byte("v0:"+ts+":"+string(body)))))
	case "svix":
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err != nil {
			return false
		}
		id := h.Get("Svix-Id")
		if id == "" {
			id = "msg_laramonster" + ts
			h.Set("Svix-Id", id)
		}
		h.Set("Svix-Timestamp", ts)
		h.Set("Svix-Signature", "v1,"+base64.StdEncoding.EncodeToString(mac(sha256.New, key, []byte(id+"."+ts+"."+string(body)))))
	case "gitlab":
		h.Set("X-Gitlab-Token", secret)
	case "telegram":
		h.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	case "spatie":
		h.Set("Signature", hex.EncodeToString(mac(sha256.New, []byte(secret), body)))
	default:
		return false
	}
	return true
}
