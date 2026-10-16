// Package share opens a local site to the internet with a public HTTPS
// link: a Cloudflare quick tunnel (no account needed) in front of a
// local-only Caddy listener for that site (server.ShareBlocks).
package share

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"laramonster/internal/fetch"
	"laramonster/internal/paths"
	"laramonster/internal/proc"
	"laramonster/internal/server"
)

// Link is a site's public link as the UI shows it.
type Link struct {
	Site      string    `json:"site"`
	URL       string    `json:"url,omitempty"` // https://….trycloudflare.com once ready
	Status    string    `json:"status"`        // starting, ready, error
	Error     string    `json:"error,omitempty"`
	User      string    `json:"user,omitempty"` // visitors log in with it ("" = open)
	Open      []string  `json:"open,omitempty"` // paths that skip the password
	StartedAt time.Time `json:"started_at"`
	Expires   time.Time `json:"expires,omitzero"` // zero: until stopped
	Requests  uint64    `json:"requests"`         // through the link
}

// Options for a new link.
type Options struct {
	User     string
	Password string
	Open     []string
	Minutes  int // 0: until stopped
}

type tunnel struct {
	link  Link
	port  int
	hash  string
	cmd   *exec.Cmd
	timer *time.Timer
	stop  chan struct{}
}

// Manager runs the tunnels. Reload is called whenever the listeners change.
type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*tunnel
	Reload  func()
}

func NewManager() *Manager { return &Manager{tunnels: map[string]*tunnel{}} }

// Blocks is what Caddy serves for the links (local listeners).
func (m *Manager) Blocks() []server.Share {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]server.Share, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		out = append(out, server.Share{Site: t.link.Site, Port: t.port, User: t.link.User, Hash: t.hash, Open: t.link.Open})
	}
	slices.SortFunc(out, func(a, b server.Share) int { return strings.Compare(a.Site, b.Site) })
	return out
}

// List returns every link, with its request count.
func (m *Manager) List() []Link {
	traffic := server.SiteTraffic()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Link, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		l := t.link
		l.Requests = traffic[server.ShareCounter(l.Site)].Total
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b Link) int { return strings.Compare(a.Site, b.Site) })
	return out
}

// Get returns one site's link.
func (m *Manager) Get(site string) (Link, bool) {
	for _, l := range m.List() {
		if l.Site == site {
			return l, true
		}
	}
	return Link{}, false
}

var userRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// Start opens a link for a site. It returns once the tunnel process runs;
// the public URL follows a few seconds later (List shows it). The
// cloudflared binary is downloaded first if needed.
func (m *Manager) Start(site string, o Options, progress fetch.Progress) error {
	if o.User != "" || o.Password != "" {
		if !userRe.MatchString(o.User) {
			return errors.New("the username can use letters, digits and . _ @ - (up to 64)")
		}
		if len(o.Password) < 4 {
			return errors.New("the password needs at least 4 characters")
		}
	}
	for _, p := range o.Open {
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \"\n\r\t{}") {
			return fmt.Errorf("%q isn't a path like /stripe/webhook", p)
		}
	}
	bin, err := Binary(progress)
	if err != nil {
		return err
	}
	m.Stop(site)
	port, err := freePort()
	if err != nil {
		return err
	}
	t := &tunnel{port: port, stop: make(chan struct{}), link: Link{Site: site, Status: "starting", User: o.User, Open: o.Open, StartedAt: time.Now()}}
	if o.User != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(o.Password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		t.hash = string(h)
	}
	if o.Minutes > 0 {
		t.link.Expires = t.link.StartedAt.Add(time.Duration(o.Minutes) * time.Minute)
		t.timer = time.AfterFunc(time.Duration(o.Minutes)*time.Minute, func() { m.Stop(site) })
	}
	m.mu.Lock()
	m.tunnels[site] = t
	m.mu.Unlock()
	if m.Reload != nil {
		m.Reload() // the local listener must exist before visitors arrive
	}

	cmd := exec.Command(bin, "tunnel", "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", port))
	proc.Bind(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.Stop(site)
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		m.Stop(site)
		return fmt.Errorf("couldn't start the tunnel: %w", err)
	}
	m.mu.Lock()
	t.cmd = cmd
	m.mu.Unlock()
	go m.watch(t, stderr)
	return nil
}

var tunnelURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// watch reads cloudflared's log for the public URL and notices when it exits.
func (m *Manager) watch(t *tunnel, stderr io.Reader) {
	var last []string
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		if u := tunnelURL.FindString(line); u != "" {
			m.mu.Lock()
			if t.link.URL == "" {
				t.link.URL, t.link.Status = u, "ready"
			}
			m.mu.Unlock()
		}
		if strings.Contains(line, " ERR ") {
			last = append(last, line)
			if len(last) > 3 {
				last = last[1:]
			}
		}
	}
	err := t.cmd.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-t.stop:
		return // stopped on purpose
	default:
	}
	t.link.Status = "error"
	msg := "the tunnel stopped"
	if len(last) > 0 {
		msg += ": " + cleanLog(last[len(last)-1])
	} else if err != nil {
		msg += ": " + err.Error()
	}
	if t.link.URL == "" {
		msg = "Cloudflare didn't give a link (" + msg + "). Check your internet connection and try again."
	}
	t.link.Error = msg
}

// cleanLog drops cloudflared's timestamp and level.
func cleanLog(line string) string {
	if i := strings.Index(line, " ERR "); i >= 0 {
		return strings.TrimSpace(line[i+5:])
	}
	return line
}

// Stop closes a site's link.
func (m *Manager) Stop(site string) {
	m.mu.Lock()
	t, ok := m.tunnels[site]
	if ok {
		delete(m.tunnels, site)
		close(t.stop)
		if t.timer != nil {
			t.timer.Stop()
		}
		if t.cmd != nil && t.cmd.Process != nil {
			proc.Terminate(t.cmd.Process)
		}
	}
	m.mu.Unlock()
	if ok && m.Reload != nil {
		m.Reload()
	}
}

// StopAll closes every link (engine shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.tunnels))
	for n := range m.tunnels {
		names = append(names, n)
	}
	reload := m.Reload
	m.Reload = nil
	m.mu.Unlock()
	for _, n := range names {
		m.Stop(n)
	}
	m.mu.Lock()
	m.Reload = reload
	m.mu.Unlock()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ── cloudflared ──

func binName() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// ManagedBinary is where LaraMonster keeps its own cloudflared.
func ManagedBinary() string { return filepath.Join(paths.Root(), "tools", "cloudflared", binName()) }

// Installed reports the cloudflared that will be used ("" if none yet).
func Installed() string {
	if _, err := os.Stat(ManagedBinary()); err == nil {
		return ManagedBinary()
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	return ""
}

var dlMu sync.Mutex

// Binary returns cloudflared, downloading Cloudflare's official build the
// first time.
func Binary(progress fetch.Progress) (string, error) {
	if p := Installed(); p != "" {
		return p, nil
	}
	dlMu.Lock()
	defer dlMu.Unlock()
	if p := Installed(); p != "" {
		return p, nil
	}
	asset, err := assetName()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(ManagedBinary())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, asset)
	if _, err := fetch.Download("https://github.com/cloudflare/cloudflared/releases/latest/download/"+asset, file, "", progress); err != nil {
		return "", fmt.Errorf("couldn't download cloudflared: %w", err)
	}
	if strings.HasSuffix(asset, ".tgz") {
		if err := fetch.Untar(file, tmp); err != nil {
			return "", err
		}
		file = filepath.Join(tmp, "cloudflared")
	}
	if err := os.Chmod(file, 0o755); err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("xattr", "-d", "com.apple.quarantine", file).Run()
	}
	if err := os.Rename(file, ManagedBinary()); err != nil {
		return "", err
	}
	return ManagedBinary(), nil
}

func assetName() (string, error) {
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64", "386": "386", "arm": "arm"}[runtime.GOARCH]
	switch {
	case arch == "":
	case runtime.GOOS == "linux":
		return "cloudflared-linux-" + arch, nil
	case runtime.GOOS == "darwin" && (arch == "amd64" || arch == "arm64"):
		return "cloudflared-darwin-" + arch + ".tgz", nil
	case runtime.GOOS == "windows" && (arch == "amd64" || arch == "386"):
		return "cloudflared-windows-" + arch + ".exe", nil
	}
	return "", fmt.Errorf("Cloudflare has no tunnel program for %s/%s", runtime.GOOS, runtime.GOARCH)
}
