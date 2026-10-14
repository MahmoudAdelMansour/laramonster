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
	inst     Install
	sendmail string
	prepend  string
	workers  []*worker
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
	for v, p := range m.pools {
		if i, ok := want[v]; !ok || i.FPM != p.inst.FPM || p.sendmail != m.Sendmail || p.prepend != m.Prepend[v] || p.exited() {
			p.stop()
			delete(m.pools, v)
		}
	}
	var errs []error
	for v, i := range want {
		if _, ok := m.pools[v]; ok {
			continue
		}
		p, err := startPool(i, m.Sendmail, m.Prepend[v])
		if err != nil {
			errs = append(errs, fmt.Errorf("php %s: %w", v, err))
			continue
		}
		m.pools[v] = p
	}
	return errors.Join(errs...)
}

// Upstreams returns the FastCGI addresses Caddy should use for a version.
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

func key(version string) string { return "php" + strings.ReplaceAll(version, ".", "") }

func LogFile(version string) string {
	return filepath.Join(paths.Logs(), key(version)+"-fpm.log")
}

func startPool(i Install, sendmail, prepend string) (*pool, error) {
	var extra []string
	if sendmail != "" {
		extra = []string{"-d", "sendmail_path=" + sendmail}
	}
	if prepend != "" {
		extra = append(extra, "-d", "auto_prepend_file="+prepend)
	}
	if runtime.GOOS == "windows" {
		p, err := startCGI(i, extra)
		if p != nil {
			p.sendmail, p.prepend = sendmail, prepend
		}
		return p, err
	}
	w, err := startFPM(i, extra)
	if err != nil {
		return nil, err
	}
	return &pool{inst: i, sendmail: sendmail, prepend: prepend, workers: []*worker{w}}, nil
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

func startFPM(i Install, extra []string) (*worker, error) {
	k := key(i.Version)
	sock := filepath.Join(paths.Run(), k+".sock")
	logFile := LogFile(i.Version)
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

func startCGI(i Install, extra []string) (*pool, error) {
	p := &pool{inst: i}
	logFile := LogFile(i.Version)
	for n := 0; n < cgiWorkers; n++ {
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
