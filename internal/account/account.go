// Package account signs LaraMonster in to monstersgate.com and keeps the
// LaraMonster Pro license state: active, expired, revoked, or signed out
// (and why: another device signed in, the user signed out, an admin did).
//
// The license is checked at start-up and every few hours. Offline, Pro
// keeps working for GraceOffline after the last successful check.
package account

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"laramonster/internal/core"
	"laramonster/internal/paths"
)

const (
	DefaultURL   = "https://monstersgate.com"
	CheckEvery   = 6 * time.Hour
	GraceOffline = 72 * time.Hour
)

// License is what monstersgate.com says about the license in use.
type License struct {
	Key       string `json:"key"`
	Status    string `json:"status"` // active, expired, revoked
	Term      string `json:"term"`
	Perpetual bool   `json:"perpetual"`
	ExpiresAt string `json:"expires_at"`
	Plan      string `json:"plan"`
}

type Who struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type Device struct {
	Name     string `json:"device_name"`
	Platform string `json:"platform"`
	At       string `json:"at"`
}

// state is what's kept on disk (0600), next to config.json.
type state struct {
	Token      string    `json:"token,omitempty"`
	Account    *Who      `json:"account,omitempty"`
	License    *License  `json:"license,omitempty"`
	LastOK     time.Time `json:"last_ok,omitempty"`
	LastCheck  time.Time `json:"last_check,omitempty"`
	Code       string    `json:"code,omitempty"` // why Pro isn't available: replaced, signed_out, admin, expired, revoked
	Message    string    `json:"message,omitempty"`
	ReplacedBy *Device   `json:"replaced_by,omitempty"`
	Offline    bool      `json:"offline,omitempty"`
	AccountURL string    `json:"account_url,omitempty"`
	BuyURL     string    `json:"buy_url,omitempty"`
}

// Status is the account as the UI shows it.
type Status struct {
	SignedIn    bool      `json:"signed_in"`
	Pro         bool      `json:"pro"`
	State       string    `json:"state"` // active, offline, expired, revoked, replaced, signed_out, admin, none
	Message     string    `json:"message,omitempty"`
	Account     *Who      `json:"account,omitempty"`
	License     *License  `json:"license,omitempty"`
	ReplacedBy  *Device   `json:"replaced_by,omitempty"`
	LastOK      time.Time `json:"last_ok,omitempty"`
	GraceUntil  time.Time `json:"grace_until,omitempty"`
	Device      string    `json:"device"`
	AccountURL  string    `json:"account_url"`
	BuyURL      string    `json:"buy_url"`
	Server      string    `json:"server"`
	CheckingNow bool      `json:"checking"`
}

// Client talks to monstersgate.com for one LaraMonster home.
type Client struct {
	mu       sync.Mutex
	st       state
	http     *http.Client
	checking bool
	now      func() time.Time
}

func New() *Client {
	c := &Client{http: &http.Client{Timeout: 15 * time.Second}, now: time.Now}
	c.load()
	return c
}

// BaseURL is monstersgate.com, or LARAMONSTER_ACCOUNT_URL for testing.
func BaseURL() string {
	if u := strings.TrimRight(os.Getenv("LARAMONSTER_ACCOUNT_URL"), "/"); u != "" {
		return u
	}
	return DefaultURL
}

func file() string       { return filepath.Join(paths.Root(), "account.json") }
func deviceFile() string { return filepath.Join(paths.Root(), "device-id") }

// DeviceID identifies this computer to monstersgate.com; it survives sign-outs.
func DeviceID() string {
	if b, err := os.ReadFile(deviceFile()); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b))
	}
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	_ = paths.Ensure()
	_ = os.WriteFile(deviceFile(), []byte(id+"\n"), 0o600)
	return id
}

func DeviceName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "This computer"
}

func (c *Client) load() {
	if b, err := os.ReadFile(file()); err == nil {
		_ = json.Unmarshal(b, &c.st)
	}
}

func (c *Client) save() {
	_ = paths.Ensure()
	b, _ := json.MarshalIndent(c.st, "", "  ")
	tmp := file() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, file())
	}
}

// Status reports whether Pro is available right now, and why not.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st
	s := Status{
		SignedIn: st.Token != "", Account: st.Account, License: st.License, ReplacedBy: st.ReplacedBy,
		LastOK: st.LastOK, Message: st.Message, Device: DeviceName(), Server: BaseURL(), CheckingNow: c.checking,
		AccountURL: firstOr(st.AccountURL, BaseURL()+"/student/laramonster"),
		BuyURL:     firstOr(st.BuyURL, BaseURL()+"/laramonster"),
	}
	switch {
	case st.Token == "" && st.Code != "":
		s.State = st.Code // replaced, signed_out, admin
	case st.Token == "":
		s.State = "none"
	case st.Code == "expired" || st.Code == "revoked":
		s.State = st.Code
	case st.License == nil:
		s.State = "none"
	default:
		s.GraceUntil = st.LastOK.Add(GraceOffline)
		if c.now().After(s.GraceUntil) {
			s.State = "offline"
			s.Message = "LaraMonster couldn't reach monstersgate.com for 3 days. Connect to the internet to keep using Pro."
		} else {
			s.State = "active"
			s.Pro = true
			if st.Offline {
				s.Message = "Offline: Pro keeps working until " + s.GraceUntil.Local().Format("Mon 2 Jan 15:04") + "."
			}
		}
	}
	return s
}

// Pro reports whether Pro features may run.
func (c *Client) Pro() bool { return c.Status().Pro }

// ProblemError is a refusal from monstersgate.com, with a sentence to show.
type ProblemError struct {
	Code    string
	Message string
}

func (e *ProblemError) Error() string { return e.Message }

type reply struct {
	Token      string   `json:"token"`
	Account    *Who     `json:"account"`
	License    *License `json:"license"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	ReplacedBy *Device  `json:"replaced_by"`
	Replaced   *Device  `json:"replaced"`
	AccountURL string   `json:"account_url"`
	BuyURL     string   `json:"buy_url"`
}

// SignIn activates this device with an account (email+password) and/or a
// license key. It returns the device that was signed out to make room, if any.
func (c *Client) SignIn(email, password, key string) (*Device, error) {
	body := map[string]string{
		"email": strings.TrimSpace(email), "password": password, "key": strings.TrimSpace(key),
		"device_id": DeviceID(), "device_name": DeviceName(), "platform": runtime.GOOS, "app_version": core.Version,
	}
	for k, v := range body {
		if v == "" {
			delete(body, k)
		}
	}
	r, status, err := c.post("/api/laramonster/activate", "", body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &ProblemError{Code: r.Code, Message: firstOr(r.Message, fmt.Sprintf("monstersgate.com answered %d", status))}
	}
	c.mu.Lock()
	c.st = state{Token: r.Token, Account: r.Account, License: r.License, LastOK: c.now(), LastCheck: c.now(), AccountURL: r.AccountURL, BuyURL: r.BuyURL}
	c.save()
	c.mu.Unlock()
	return r.Replaced, nil
}

// Check asks monstersgate.com whether this device's license still holds.
// Network failures keep the last state (offline grace).
func (c *Client) Check() error {
	c.mu.Lock()
	token := c.st.Token
	if token == "" || c.checking {
		c.mu.Unlock()
		return nil
	}
	c.checking = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.checking = false; c.mu.Unlock() }()

	r, status, err := c.post("/api/laramonster/status", token, map[string]string{"device_id": DeviceID(), "app_version": core.Version})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.LastCheck = c.now()
	if err != nil {
		c.st.Offline = true
		c.save()
		return err
	}
	c.st.Offline = false
	c.st.AccountURL, c.st.BuyURL = firstOr(r.AccountURL, c.st.AccountURL), firstOr(r.BuyURL, c.st.BuyURL)
	switch status {
	case http.StatusOK:
		c.st.Account, c.st.License, c.st.LastOK = r.Account, r.License, c.now()
		c.st.Code, c.st.Message, c.st.ReplacedBy = "", "", nil
	case http.StatusUnauthorized:
		// This device was signed out: another device signed in, the user
		// signed out from the website, or an admin did.
		c.st = state{Code: firstOr(r.Code, "signed_out"), Message: r.Message, ReplacedBy: r.ReplacedBy, Account: c.st.Account, AccountURL: c.st.AccountURL, BuyURL: c.st.BuyURL}
	case http.StatusForbidden:
		c.st.Code, c.st.Message = r.Code, r.Message
		if c.st.License != nil {
			c.st.License.Status = r.Code
		}
	default:
		c.st.Offline = true
	}
	c.save()
	return nil
}

// SignOut ends this device's session (best effort online) and forgets it.
func (c *Client) SignOut() {
	c.mu.Lock()
	token := c.st.Token
	c.mu.Unlock()
	if token != "" {
		_, _, _ = c.post("/api/laramonster/sign-out", token, map[string]string{})
	}
	c.mu.Lock()
	c.st = state{Code: "", AccountURL: c.st.AccountURL, BuyURL: c.st.BuyURL}
	c.save()
	c.mu.Unlock()
}

// Run checks the license now and every CheckEvery until stop closes.
func (c *Client) Run(stop <-chan struct{}) {
	t := time.NewTimer(5 * time.Second)
	for {
		select {
		case <-stop:
			t.Stop()
			return
		case <-t.C:
			_ = c.Check()
			t.Reset(CheckEvery)
		}
	}
}

func (c *Client) post(path, token string, body any) (*reply, int, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, BaseURL()+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "LaraMonster/"+core.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, errors.New("couldn't reach monstersgate.com, check your internet connection")
	}
	defer resp.Body.Close()
	var r reply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil && resp.StatusCode != http.StatusOK {
		return &reply{}, resp.StatusCode, nil
	}
	if resp.StatusCode == http.StatusUnprocessableEntity && r.Code == "" && r.Message != "" {
		r.Code = "invalid"
	}
	return &r, resp.StatusCode, nil
}

func firstOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
