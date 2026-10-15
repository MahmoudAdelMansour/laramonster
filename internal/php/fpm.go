package php

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"laramonster/internal/paths"
	"laramonster/internal/proc"
)

// worker is one php-fpm master (unix) or php-cgi process (Windows).
type worker struct {
	cmd  *exec.Cmd
	addr string // caddy upstream address
	done chan struct{}
}

type pool struct {
	inst    Install
	args    []string // the -d settings it was started with
	workers []*worker
}

// Manager runs one PHP FastCGI pool per PHP version that a site needs.
type Manager struct {
	mu    sync.Mutex
	pools map[string]*pool
	// Sendmail, when set, routes PHP's mail() (e.g. to Mailpit).
	Sendmail string
	// Prepend maps a version to an auto_prepend_file (LaraMonster's event
	// agent) for pools that serve a site with event tracking on.
	Prepend map[string]string
	// Ini holds php.ini settings by pool key: a version ("8.4") or a site
	// with settings of its own ("8.4@shop", see SiteKey).
	Ini map[string]map[string]string
	// Sites maps a site with settings of its own to its pool key. Such a
	// site gets its own php-fpm, so its settings never reach other sites
	// (PHP_VALUE over FastCGI leaks between sites: php bug #53611).
	Sites map[string]string
}

// SiteKey is the pool key of a site that has php.ini settings of its own.
func SiteKey(version, site string) string { return version + "@" + site }

// IniArgs turns settings into php -d arguments, sorted by name.
func IniArgs(settings map[string]string) []string {
	names := make([]string, 0, len(settings))
	for k := range settings {
		names = append(names, k)
	}
	slices.Sort(names)
	var out []string
	for _, k := range names {
		out = append(out, "-d", k+"="+settings[k])
	}
	return out
}

func (m *Manager) args(key, version string) []string {
	var extra []string
	if m.Sendmail != "" {
		extra = []string{"-d", "sendmail_path=" + m.Sendmail}
	}
	if pre := m.Prepend[version]; pre != "" {
		extra = append(extra, "-d", "auto_prepend_file="+pre)
	}
	return append(extra, IniArgs(m.Ini[key])...)
}

func NewManager() *Manager { return &Manager{pools: map[string]*pool{}} }

// Ensure starts pools for the given installs and stops any others.
func (m *Manager) Ensure(installs []Install) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	want := map[string]Install{}
	for _, i := range installs {
		want[i.Version] = i
	}
	for _, k := range m.Sites {
		version, _, _ := strings.Cut(k, "@")
		if i, ok := want[version]; ok {
			want[k] = i
		}
	}
	for k, p := range m.pools {
		version, _, _ := strings.Cut(k, "@")
		if i, ok := want[k]; !ok || i.FPM != p.inst.FPM || !slices.Equal(p.args, m.args(k, version)) || p.exited() {
			p.stop()
			delete(m.pools, k)
		}
	}
	var errs []error
	for k, i := range want {
		if _, ok := m.pools[k]; ok {
			continue
		}
		version, site, _ := strings.Cut(k, "@")
		args := m.args(k, version)
		p, err := startPool(i, k, args)
		if err != nil {
			name := "php " + version
			if site != "" {
				name += " for " + site
			}
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		m.pools[k] = p
	}
	return errors.Join(errs...)
}

// Configure sets the php.ini settings by pool key and the sites with
// pools of their own; Ensure applies them.
func (m *Manager) Configure(ini map[string]map[string]string, sites map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Ini, m.Sites = ini, sites
}

// OwnPool reports whether a site runs in a pool of its own.
func (m *Manager) OwnPool(site string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.Sites[site]
	return ok
}

// UpstreamsFor returns the FastCGI addresses for a site: its own pool when
// it has settings of its own, else its version's.
func (m *Manager) UpstreamsFor(version, site string) []string {
	m.mu.Lock()
	k, own := m.Sites[site]
	m.mu.Unlock()
	if own {
		if ups := m.Upstreams(k); len(ups) > 0 {
			return ups
		}
	}
	return m.Upstreams(version)
}

// Upstreams returns the FastCGI addresses Caddy should use for a pool key.
func (m *Manager) Upstreams(version string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pools[version]
	if !ok {
		return nil
	}
	var out []string
	for _, w := range p.workers {
		out = append(out, w.addr)
	}
	return out
}

// Versions returns the versions with a running pool.
func (m *Manager) Versions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{} // an empty list, not null, for the dashboard
	for v := range m.pools {
		out = append(out, v)
	}
	return out
}

// PIDs returns each pool's root processes (php-fpm masters, php-cgi
// workers) by version, for resource monitoring.
func (m *Manager) PIDs() map[string][]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]int{}
	for v, p := range m.pools {
		for _, w := range p.workers {
			if w.cmd.Process != nil && !p.exited() {
				out[v] = append(out[v], w.cmd.Process.Pid)
			}
		}
	}
	return out
}

// StatusSockets returns the php-fpm socket of each version (unix only;
// php-cgi on Windows has no status page).
func (m *Manager) StatusSockets() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for v, p := range m.pools {
		if len(p.workers) == 1 && strings.HasPrefix(p.workers[0].addr, "unix/") {
			out[v] = strings.TrimPrefix(p.workers[0].addr, "unix/")
		}
	}
	return out
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for v, p := range m.pools {
		p.stop()
		delete(m.pools, v)
	}
}

// key names a pool's files: php84, or php84@shop for a site's own pool.
func key(k string) string { return "php" + strings.ReplaceAll(k, ".", "") }

func LogFile(version string) string {
	return filepath.Join(paths.Logs(), key(version)+"-fpm.log")
}

func startPool(i Install, k string, extra []string) (*pool, error) {
	if runtime.GOOS == "windows" {
		p, err := startCGI(i, k, extra)
		if p != nil {
			p.args = extra
		}
		return p, err
	}
	w, err := startFPM(i, k, extra)
	if err != nil {
		return nil, err
	}
	return &pool{inst: i, args: extra, workers: []*worker{w}}, nil
}

const fpmConf = `[global]
error_log = %[1]s
daemonize = no

[laramonster]
listen = %[2]s
listen.mode = 0660
pm = dynamic
pm.max_children = 10
pm.start_servers = 2
pm.min_spare_servers = 1
pm.max_spare_servers = 4
catch_workers_output = yes
clear_env = no
; Read by LaraMonster over the socket to show per-site load.
pm.status_path = ` + StatusPath + `
`

func startFPM(i Install, poolKey string, extra []string) (*worker, error) {
	k := key(poolKey)
	sock := filepath.Join(paths.Run(), k+".sock")
	logFile := LogFile(poolKey)
	conf := filepath.Join(paths.Run(), k+"-fpm.conf")
	if err := os.WriteFile(conf, fmt.Appendf(nil, fpmConf, logFile, sock), 0o644); err != nil {
		return nil, err
	}
	_ = os.Remove(sock)

	args := []string{"--fpm-config", conf, "--nodaemonize"}
	if i.IniDir != "" {
		args = append(args, "-c", filepath.Join(i.IniDir, "php.ini"))
	}
	args = append(args, extra...)
	w, err := spawn(i, logFile, "unix/"+sock, i.FPM, args...)
	if err != nil {
		return nil, err
	}
	if err := waitFor(w, func() bool { _, err := os.Stat(sock); return err == nil }); err != nil {
		w.stop()
		return nil, fmt.Errorf("%w, see %s", err, logFile)
	}
	return w, nil
}

// On Windows there is no php-fpm: run several php-cgi processes on loopback
// ports and let Caddy balance across them.
const cgiWorkers = 4

func startCGI(i Install, poolKey string, extra []string) (*pool, error) {
	p := &pool{inst: i}
	logFile := LogFile(poolKey)
	workers := cgiWorkers
	if strings.Contains(poolKey, "@") {
		workers = 2 // one site only
	}
	for n := 0; n < workers; n++ {
		port, err := freePort()
		if err != nil {
			p.stop()
			return nil, err
		}
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		w, err := spawn(i, logFile, addr, i.FPM, append([]string{"-b", addr}, extra...)...)
		if err != nil {
			p.stop()
			return nil, err
		}
		p.workers = append(p.workers, w)
		if err := waitFor(w, func() bool {
			c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				c.Close()
			}
			return err == nil
		}); err != nil {
			p.stop()
			return nil, fmt.Errorf("%w, see %s", err, logFile)
		}
	}
	return p, nil
}

func spawn(i Install, logFile, addr, bin string, args ...string) (*worker, error) {
	out, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), i.Env()...)
	// php-cgi exits after this many requests; 0 disables that on Windows.
	cmd.Env = append(cmd.Env, "PHP_FCGI_MAX_REQUESTS=0")
	proc.Bind(cmd)
	if err := cmd.Start(); err != nil {
		out.Close()
		return nil, err
	}
	w := &worker{cmd: cmd, addr: addr, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		out.Close()
		close(w.done)
		if err != nil {
			log.Printf("php %s (%s) exited: %v", i.Version, filepath.Base(bin), err)
		}
	}()
	return w, nil
}

func waitFor(w *worker, ready func() bool) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return nil
		}
		if w.exited() {
			return errors.New("exited on startup")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("did not start listening in time")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (p *pool) exited() bool {
	for _, w := range p.workers {
		if w.exited() {
			return true
		}
	}
	return false
}

func (p *pool) stop() {
	for _, w := range p.workers {
		w.stop()
	}
}

func (w *worker) exited() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *worker) stop() {
	if w.exited() {
		return
	}
	proc.Terminate(w.cmd.Process)
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		_ = w.cmd.Process.Kill()
		<-w.done
	}
	if sock, ok := strings.CutPrefix(w.addr, "unix/"); ok {
		_ = os.Remove(sock)
	}
}
