// Package services runs databases, caches and tools (MySQL, Redis, Mailpit…)
// as child processes with their data kept inside LaraMonster's home.
package services

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"laramonster/internal/fetch"
	"laramonster/internal/paths"
	procutil "laramonster/internal/proc"
)

// Def describes a service LaraMonster can run.
type Def struct {
	Name        string
	Title       string
	Category    string
	DefaultPort int
	// Binaries are looked up (in order) in LaraMonster's own bin dir, then PATH.
	Binaries []string
	// Globs are extra places to look for system installs whose binaries
	// aren't on PATH (e.g. Debian's /usr/lib/postgresql/<v>/bin).
	Globs []string
	// Download fetches a managed binary when none is installed (optional).
	Download func(dir string, progress fetch.Progress) error
	// Init prepares an empty data directory (optional).
	Init func(bin, data string, port int) error
	// Args builds the command line.
	Args func(bin, data string, port int) []string
	// Env is a Laravel .env snippet for connecting to the service.
	Env func(port int) string
	// WebPort is the port of a web UI, if the service has one.
	WebPort func(port int) int
	// InstallHint tells the user how to get the binary when it can't be downloaded.
	InstallHint string
}

func (d *Def) BinDir() string  { return filepath.Join(paths.Root(), "services", d.Name, "bin") }
func (d *Def) DataDir() string { return filepath.Join(paths.Root(), "services", d.Name, "data") }
func (d *Def) LogFile() string { return filepath.Join(paths.Logs(), d.Name+".log") }

// Find returns the service binary, or "" if it isn't installed. Managed
// downloads win over system installs.
func (d *Def) Find() string {
	for _, b := range d.Binaries {
		name := b
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if p := filepath.Join(d.BinDir(), name); isFile(p) {
			return p
		}
		// Archives that unpack to <dist>/<release>/bin/<binary>.
		if m, _ := filepath.Glob(filepath.Join(d.BinDir(), "*", "bin", name)); len(m) > 0 {
			return m[len(m)-1]
		}
	}
	for _, b := range d.Binaries {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	for _, g := range d.Globs {
		if m, _ := filepath.Glob(g); len(m) > 0 {
			return m[len(m)-1]
		}
	}
	return ""
}

// Managed reports whether the binary in use was downloaded by LaraMonster.
func (d *Def) Managed() bool {
	rel, err := filepath.Rel(d.BinDir(), d.Find())
	return err == nil && !strings.HasPrefix(rel, "..")
}

// Registry lists every known service.
var Registry = []*Def{mysqlDef, postgresDef, redisDef, mailpitDef, meilisearchDef}

func Get(name string) (*Def, bool) {
	i := slices.IndexFunc(Registry, func(d *Def) bool { return d.Name == name })
	if i < 0 {
		return nil, false
	}
	return Registry[i], true
}

// State is a service as reported to the CLI and UI.
type State struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	Port        int    `json:"port"`
	WebURL      string `json:"web_url,omitempty"`
	Installed   bool   `json:"installed"`
	CanDownload bool   `json:"can_download"`
	Binary      string `json:"binary,omitempty"`
	Running     bool   `json:"running"`
	Enabled     bool   `json:"enabled"`
	Env         string `json:"env"`
	Hint        string `json:"hint,omitempty"`
	Error       string `json:"error,omitempty"`
}

type proc struct {
	cmd  *exec.Cmd
	port int
	done chan struct{}
	err  error
}

// Manager starts and stops services.
type Manager struct {
	mu    sync.Mutex
	procs map[string]*proc
	errs  map[string]string
}

func NewManager() *Manager {
	return &Manager{procs: map[string]*proc{}, errs: map[string]string{}}
}

// PIDs returns the process of each running service.
func (m *Manager) PIDs() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for name, p := range m.procs {
		if !p.exited() && p.cmd.Process != nil {
			out[name] = p.cmd.Process.Pid
		}
	}
	return out
}

func (m *Manager) Running(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[name]
	return ok && !p.exited()
}

func (m *Manager) State(d *Def, port int, enabled bool) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	bin := d.Find()
	st := State{
		Name: d.Name, Title: d.Title, Category: d.Category, Port: port,
		Installed: bin != "", CanDownload: d.Download != nil, Binary: bin,
		Enabled: enabled, Env: d.Env(port), Error: m.errs[d.Name],
	}
	if !st.Installed && !st.CanDownload {
		st.Hint = d.InstallHint
	}
	if d.WebPort != nil {
		st.WebURL = fmt.Sprintf("http://127.0.0.1:%d", d.WebPort(port))
	}
	if p, ok := m.procs[d.Name]; ok && !p.exited() {
		st.Running = true
	}
	return st
}

// Install downloads a managed binary for services that support it.
func (m *Manager) Install(d *Def, progress fetch.Progress) error {
	if d.Download == nil {
		return fmt.Errorf("%s can't be downloaded automatically: %s", d.Title, d.InstallHint)
	}
	if err := os.MkdirAll(d.BinDir(), 0o755); err != nil {
		return err
	}
	return d.Download(d.BinDir(), progress)
}

// Start runs a service on port, initialising its data directory if needed.
func (m *Manager) Start(d *Def, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.procs[d.Name]; ok && !p.exited() {
		if p.port == port {
			return nil
		}
		p.stop()
	}
	err := m.start(d, port)
	if err != nil {
		m.errs[d.Name] = err.Error()
	} else {
		delete(m.errs, d.Name)
	}
	return err
}

func (m *Manager) start(d *Def, port int) error {
	bin := d.Find()
	if bin == "" {
		return fmt.Errorf("%s is not installed", d.Title)
	}
	if portInUse(port) {
		return fmt.Errorf("port %d is already in use (another %s running?)", port, d.Title)
	}
	data := d.DataDir()
	if err := os.MkdirAll(paths.Logs(), 0o755); err != nil {
		return err
	}
	if empty(data) {
		if err := os.MkdirAll(data, 0o700); err != nil {
			return err
		}
		if d.Init != nil {
			if err := d.Init(bin, data, port); err != nil {
				_ = os.RemoveAll(data)
				return fmt.Errorf("initialising %s: %w", d.Title, err)
			}
		}
	}
	out, err := os.OpenFile(d.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	args := d.Args(bin, data, port)
	cmd := exec.Command(bin, args...)
	cmd.Dir = data
	cmd.Stdout, cmd.Stderr = out, out
	procutil.Bind(cmd)
	if err := cmd.Start(); err != nil {
		out.Close()
		return err
	}
	p := &proc{cmd: cmd, port: port, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		out.Close()
		close(p.done)
		if p.err != nil {
			log.Printf("%s exited: %v", d.Name, p.err)
		}
	}()
	m.procs[d.Name] = p

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if portInUse(port) {
			return nil
		}
		if p.exited() {
			return fmt.Errorf("%s exited on startup, see %s", d.Title, d.LogFile())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s did not open port %d in time, see %s", d.Title, port, d.LogFile())
}

func (m *Manager) Stop(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.procs[name]; ok {
		p.stop()
		delete(m.procs, name)
	}
	delete(m.errs, name)
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var wg sync.WaitGroup
	for name, p := range m.procs {
		wg.Add(1)
		go func() { defer wg.Done(); p.stop() }()
		delete(m.procs, name)
	}
	wg.Wait()
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *proc) stop() {
	if p.exited() {
		return
	}
	procutil.Terminate(p.cmd.Process)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// --- helpers ---

func portInUse(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func empty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err != nil || len(entries) == 0
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		return fmt.Errorf("%s: %w\n%s", filepath.Base(name), err, msg)
	}
	return nil
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// githubAsset downloads a release asset of a GitHub repo into dir.
func githubAsset(repo, asset, dir string, progress fetch.Progress) (string, error) {
	url := fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", repo, asset)
	dest := filepath.Join(dir, asset)
	if _, err := fetch.Download(url, dest, "", progress); err != nil {
		return "", err
	}
	return dest, nil
}

func platformKey() string { return runtime.GOOS + "/" + runtime.GOARCH }

var errUnsupported = errors.New("no download available for " + platformKey())
