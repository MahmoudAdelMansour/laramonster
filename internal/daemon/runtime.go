package daemon

import (
	"fmt"
	"laramonster/internal/account"
	"laramonster/internal/ipc"
	"laramonster/internal/laravel"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/dns"
	"laramonster/internal/paths"
	"laramonster/internal/php"
	"laramonster/internal/server"
	"laramonster/internal/services"
	"laramonster/internal/share"
	"laramonster/internal/sites"
	"laramonster/internal/trust"
)

// Runtime owns everything the daemon runs: Caddy, PHP pools, services, DNS.
type Runtime struct {
	mu       sync.Mutex
	fpm      *php.Manager
	svc      *services.Manager
	dns      *dns.Server
	jobs     *core.Jobs
	sites    []sites.Site
	installs []php.Install
	broken   []php.Broken // installed by LaraMonster but not starting
	warnings []string
	ready    atomic.Bool // set once the first reload has finished
	ports    []int       // ports Caddy currently listens on

	tlsMu sync.Mutex
	tls   TLSStatus
	tlsAt time.Time

	healthMu sync.Mutex
	health   map[string]probe // proxy target -> last check

	mon *monitor

	acct    *account.Client
	workers *laravel.Workers
	shares  *share.Manager // public links (Cloudflare tunnels)
}

func NewRuntime() *Runtime {
	r := &Runtime{
		fpm:  php.NewManager(),
		svc:  services.NewManager(),
		dns:  dns.NewServer(),
		jobs: &core.Jobs{},
	}
	r.mon = newMonitor(r)
	r.acct = account.New()
	r.workers = laravel.NewWorkers()
	r.shares = share.NewManager()
	r.shares.Reload = r.reloadQuiet
	return r
}

// Reload re-reads the config and applies it. Warnings don't fail the reload;
// only a Caddy error does.
func (r *Runtime) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Caddy binds with SO_REUSEPORT on Linux, so a second engine (or another
	// web server) on the same port would silently share traffic. Refuse, but
	// set up everything else: the dashboard explains it and switches ports.
	var busy error
	if ports := busyPorts(cfg, r.ports); len(ports) > 0 {
		busy = &PortsBusyError{Ports: ports}
	}
	installs := php.Discover()
	list := sites.List(cfg, php.Default(cfg.PHP, installs))

	var warnings []string
	var needed []php.Install
	seen := map[string]bool{}
	for i, s := range list {
		if !s.Enabled || s.Proxy != "" {
			continue // served without PHP
		}
		if s.PHP == "" {
			warnings = append(warnings, "no PHP installed: sites are served as static files")
			continue
		}
		inst, ok := php.Find(installs, s.PHP)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s wants PHP %s, which is not installed", s.Name, s.PHP))
			list[i].PHP = ""
			continue
		}
		if !seen[inst.Version] {
			seen[inst.Version] = true
			needed = append(needed, inst)
		}
	}
	r.fpm.Sendmail = ""
	if d, ok := services.Get("mailpit"); ok {
		if bin := d.Find(); bin != "" {
			r.fpm.Sendmail = fmt.Sprintf("%s sendmail -S 127.0.0.1:%d", bin, cfg.Service(d.Name, d.DefaultPort).Port)
		}
	}
	// Event Tracker (Pro): pools that serve a tracked Laravel site load the
	// agent, and the agent is told which projects to record.
	r.fpm.Prepend = map[string]string{}
	var tracked []string
	if r.acct.Pro() {
		for _, s := range list {
			if cfg.Tracking[s.Name] && s.Enabled && s.Proxy == "" && laravel.IsLaravel(s.Path) {
				tracked = append(tracked, s.Path)
				if s.PHP != "" {
					r.fpm.Prepend[s.PHP] = laravel.AgentFile()
				}
			}
		}
	}
	if err := laravel.WriteTracking(tracked); err != nil {
		warnings = append(warnings, "event tracker: "+err.Error())
	}
	// Debugger (free): the same agent, for every site with it on.
	var debugged []laravel.DebugSite
	for _, s := range list {
		if cfg.Debugging[s.Name] && s.Enabled && s.Proxy == "" && laravel.IsLaravel(s.Path) {
			debugged = append(debugged, laravel.DebugSite{Dir: s.Path, Echo: !cfg.DebugQuiet[s.Name]})
			if s.PHP != "" {
				r.fpm.Prepend[s.PHP] = laravel.AgentFile()
			}
		}
	}
	if err := laravel.WriteDebug(debugged); err != nil {
		warnings = append(warnings, "debugger: "+err.Error())
	}
	r.fpm.Extensions = map[string][]string{}
	for _, inst := range needed {
		r.fpm.Extensions[inst.Version] = core.ExtensionArgs(cfg, inst)
	}
	r.applyWebhookWatch(cfg, list)
	r.fpm.Configure(core.PoolIni(cfg, list))
	if err := r.fpm.Ensure(needed); err != nil {
		warnings = append(warnings, err.Error())
	}

	dnsPort := cfg.DNSPort
	if cfg.TLD == "localhost" {
		dnsPort = 0
	}
	if err := r.dns.Apply(cfg.TLD, dnsPort); err != nil {
		warnings = append(warnings, err.Error())
	}

	var proxies []server.Proxy
	if cfg.BrowserDashboard {
		p := server.Proxy{Name: server.DashboardName, Upstream: ipc.Upstream()}
		if t := ipc.Token(); t != "" {
			p.Headers = map[string]string{ipc.Header: t}
		}
		proxies = append(proxies, p)
	}
	if d, ok := services.Get("mailpit"); ok && r.svc.Running("mailpit") {
		port := cfg.Service(d.Name, d.DefaultPort).Port
		proxies = append(proxies, server.Proxy{Name: server.MailName, Upstream: fmt.Sprintf("127.0.0.1:%d", d.WebPort(port))})
	}

	caddyfile := server.Caddyfile(cfg, list, r.fpm.UpstreamsFor, dedupe(proxies)) +
		server.ShareBlocks(list, r.fpm.UpstreamsFor, r.shares.Blocks())
	// Kept for debugging: what Caddy is actually running.
	_ = os.WriteFile(filepath.Join(paths.Run(), "Caddyfile"), []byte(caddyfile), 0o644)
	if busy != nil {
		// Caddy keeps the ports it has, if any.
		r.sites, r.installs, r.broken = list, installs, php.FindBroken(installs)
		r.warnings = slices.Compact(warnings)
		return busy
	}
	if err := server.Apply(caddyfile); err != nil {
		return err
	}
	r.sites, r.installs, r.broken = list, installs, php.FindBroken(installs)
	r.ports = []int{cfg.HTTPPort, cfg.HTTPSPort}
	r.warnings = slices.Compact(warnings)
	for _, w := range r.warnings {
		log.Print("warning: ", w)
	}
	return nil
}

func dedupe(p []server.Proxy) []server.Proxy {
	return slices.CompactFunc(p, func(a, b server.Proxy) bool { return a.Name == b.Name })
}

// StartEnabledServices starts every enabled service (in the background).
func (r *Runtime) StartEnabledServices() {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	for _, d := range services.Registry {
		sc := cfg.Services[d.Name]
		if sc == nil || !sc.Enabled || d.Find() == "" {
			continue
		}
		go func() {
			if err := r.svc.Start(d, cfg.Service(d.Name, d.DefaultPort).Port); err != nil {
				log.Printf("%s: %v", d.Name, err)
			}
			if d.Name == "mailpit" {
				_ = r.Reload()
			}
		}()
	}
}

func (r *Runtime) Shutdown() {
	r.shares.StopAll()
	r.workers.StopAll()
	r.svc.StopAll()
	r.fpm.StopAll()
	r.dns.Stop()
	_ = server.Stop()
}

type TLSStatus struct {
	Exists   bool   `json:"exists"`
	Trusted  bool   `json:"trusted"`
	Browsers string `json:"browsers"` // trusted, partial, none, missing-certutil, system
	Root     string `json:"root"`
}

type Status struct {
	Version      string           `json:"version"`
	PID          int              `json:"pid"`
	Home         string           `json:"home"`
	Config       *config.Config   `json:"config"`
	DefaultPHP   string           `json:"default_php"`
	DashboardURL string           `json:"dashboard_url"`
	Onboarded    bool             `json:"onboarded"`
	Pro          bool             `json:"pro"` // LaraMonster Pro features are available
	Sites        []sites.Site     `json:"sites"`
	PHP          []php.Install    `json:"php"`
	PHPBroken    []php.Broken     `json:"php_broken"`
	Pools        []string         `json:"pools"`
	Services     []services.State `json:"services"`
	DNS          dns.Status       `json:"dns"`
	TLS          TLSStatus        `json:"tls"`
	Mail         MailStatus       `json:"mail"`
	Warnings     []string         `json:"warnings"`
	Jobs         []core.Job       `json:"jobs"`
	Shares       []share.Link     `json:"shares"`
	PortsBusy    []int            `json:"ports_busy"` // web server ports another program holds
	OS           string           `json:"os"`
}

func (r *Runtime) Status() Status {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	r.mu.Lock()
	list := slices.Clone(r.sites)
	installs := slices.Clone(r.installs)
	broken := slices.Clone(r.broken)
	warnings := slices.Clone(r.warnings)
	ours := slices.Clone(r.ports)
	r.mu.Unlock()

	st := Status{
		Version:      core.Version,
		PID:          os.Getpid(),
		Home:         paths.Root(),
		Config:       cfg,
		DefaultPHP:   php.Default(cfg.PHP, installs),
		DashboardURL: core.DashboardURL(cfg),
		// Installs that already serve sites predate the welcome screen.
		Onboarded: cfg.Onboarded || len(cfg.Paths) > 0 || len(cfg.Links) > 0,
		Pro:       r.acct.Pro(),
		Sites:     r.withHealth(list),
		PHP:       installs,
		PHPBroken: broken,
		Pools:     r.fpm.Versions(),
		DNS:       dns.Check(cfg.TLD, core.HostNames(cfg.TLD, list)),
		TLS:       r.tlsStatus(),
		Mail:      r.mailStatus(),
		Warnings:  warnings,
		Jobs:      r.jobs.Snapshot(),
		Shares:    r.shares.List(),
		PortsBusy: busyPorts(cfg, ours),
		OS:        runtime.GOOS,
	}
	if st.PortsBusy == nil {
		st.PortsBusy = []int{}
	}
	if st.PHPBroken == nil {
		st.PHPBroken = []php.Broken{}
	}
	if st.Warnings == nil {
		st.Warnings = []string{}
	}
	for _, d := range services.Registry {
		sc := cfg.Services[d.Name]
		port, enabled := d.DefaultPort, false
		if sc != nil {
			enabled = sc.Enabled
			if sc.Port != 0 {
				port = sc.Port
			}
		}
		st.Services = append(st.Services, r.svc.State(d, port, enabled))
	}
	return st
}

// tlsStatus is cached briefly: the NSS check shells out to certutil and the
// dashboard polls every two seconds.
func (r *Runtime) tlsStatus() TLSStatus {
	r.tlsMu.Lock()
	defer r.tlsMu.Unlock()
	if time.Since(r.tlsAt) > 10*time.Second {
		r.tls = TLSStatus{Exists: trust.Exists(), Trusted: trust.Trusted(), Browsers: trust.BrowserStatus(), Root: paths.OnDisk(trust.RootCert())}
		r.tlsAt = time.Now()
	}
	return r.tls
}

// invalidateTLS forces the next status to re-check trust.
func (r *Runtime) invalidateTLS() {
	r.tlsMu.Lock()
	r.tlsAt = time.Time{}
	r.tlsMu.Unlock()
}

func portBusy(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
