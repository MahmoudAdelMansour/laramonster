package daemon

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"laramonster/internal/core"
	"laramonster/internal/php"
	"laramonster/internal/server"
	"laramonster/internal/services"
	"laramonster/internal/sites"
)

// Sampling is cheap in the background (per-site request tracking for idle
// detection) and every two seconds while someone has the Monitor page open.
const (
	fastEvery  = 2 * time.Second
	slowEvery  = 30 * time.Second
	viewerIdle = 20 * time.Second
	historyLen = 90 // 3 minutes at fastEvery
	siteWindow = 30 // samples averaged for per-site CPU (1 minute)
)

// Group is a set of processes LaraMonster runs (or serves) for one purpose.
type Group struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"` // engine, app, php, service, server, jobs
	Name   string  `json:"name"`
	Ref    string  `json:"ref,omitempty"` // php version, service or site name
	CPU    float64 `json:"cpu"`           // % of the whole machine
	CPUAvg float64 `json:"cpu_avg"`       // over the last 30 seconds
	Mem    uint64  `json:"mem"`           // bytes (proportional share on Linux)
	Procs  int     `json:"procs"`
	Sites  int     `json:"sites,omitempty"` // php: active sites it serves
}

// SiteLoad is what one PHP-served site costs right now.
type SiteLoad struct {
	Name     string  `json:"name"`
	CPU      float64 `json:"cpu"`      // % of machine, 1-minute average
	Requests int     `json:"requests"` // in the last minute
	Running  int     `json:"running"`  // requests in progress
	Longest  float64 `json:"longest"`  // seconds, longest request in progress
	URI      string  `json:"uri,omitempty"`
	Memory   int64   `json:"memory"` // peak memory of its last requests
}

type Sample struct {
	T      int64   `json:"t"` // unix ms
	CPU    float64 `json:"cpu"`
	Mem    uint64  `json:"mem"`
	SysCPU float64 `json:"sys_cpu"`
	SysMem uint64  `json:"sys_mem"`
}

type SystemStats struct {
	CPUs      int     `json:"cpus"`
	CPU       float64 `json:"cpu"`
	Load1     float64 `json:"load1"`
	MemTotal  uint64  `json:"mem_total"`
	MemUsed   uint64  `json:"mem_used"`
	MemAvail  uint64  `json:"mem_available"`
	SwapUsed  uint64  `json:"swap_used"`
	SwapTotal uint64  `json:"swap_total"`
}

type MonitorReport struct {
	At              int64            `json:"at"`
	Warm            bool             `json:"warm"` // false until two samples exist (CPU needs a delta)
	System          SystemStats      `json:"system"`
	CPU             float64          `json:"cpu"`
	Mem             uint64           `json:"mem"`
	Procs           int              `json:"procs"`
	Groups          []Group          `json:"groups"`
	Sites           []SiteLoad       `json:"sites"`
	SiteLoadNote    string           `json:"site_load_note,omitempty"`
	History         []Sample         `json:"history"`
	Recommendations []Recommendation `json:"recommendations"`
}

type siteWin struct {
	cpu      []float64
	reqs     []reqMark
	total    uint64 // web server request counter at the last sample
	seen     bool
	uri      string // last request seen in progress
	memory   int64
	lastSeen time.Time
}

type reqMark struct {
	at time.Time
	n  int
}

type monitor struct {
	rt *Runtime

	mu         sync.Mutex
	started    time.Time
	lastViewer time.Time
	wake       chan struct{}
	procs      map[int32]*process.Process
	report     MonitorReport
	history    []Sample
	sites      map[string]*siteWin
	groupCPU   map[string][]float64
	samples    int

	ownMu   sync.Mutex
	ownAt   time.Time
	ownPIDs map[string]int32 // site -> pid listening on its server port
	appPID  int32
	appAt   time.Time
}

func newMonitor(rt *Runtime) *monitor {
	return &monitor{rt: rt, started: time.Now(), wake: make(chan struct{}, 1),
		procs: map[int32]*process.Process{}, sites: map[string]*siteWin{}, groupCPU: map[string][]float64{}}
}

func (m *monitor) run(stop <-chan struct{}) {
	for {
		m.sample()
		m.mu.Lock()
		every := slowEvery
		if time.Since(m.lastViewer) < viewerIdle {
			every = fastEvery
		}
		m.mu.Unlock()
		select {
		case <-stop:
			return
		case <-m.wake:
		case <-time.After(every):
		}
	}
}

// Report returns the latest numbers. A viewer (the Monitor page) switches
// sampling to every two seconds; a peek (the nav badge) doesn't.
func (m *monitor) Report(viewer bool) MonitorReport {
	m.mu.Lock()
	wasIdle := time.Since(m.lastViewer) >= viewerIdle
	if !viewer {
		wasIdle = false
	} else {
		m.lastViewer = time.Now()
	}
	r := m.report
	r.History = slices.Clone(m.history)
	m.mu.Unlock()
	// Empty lists, not null: the page reads their length.
	r.Groups, r.Sites, r.Recommendations = nonNil(r.Groups), nonNil(r.Sites), nonNil(r.Recommendations)
	r.History = nonNil(r.History)
	if wasIdle {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	return r
}

func (m *monitor) sample() {
	now := time.Now()
	ncpu := runtime.NumCPU()
	st := m.rt.Status()
	active := map[string]sites.Site{}
	roots := map[string]string{}
	for _, s := range st.Sites {
		if s.Enabled && s.Proxy == "" {
			active[s.Name] = s
			roots[s.Name] = s.Root
		}
	}

	// --- processes, grouped ---
	type member struct {
		g   *Group
		pid int32
	}
	var groups []*Group
	byID := map[string]*Group{}
	group := func(id, kind, name, ref string) *Group {
		if g, ok := byID[id]; ok {
			return g
		}
		g := &Group{ID: id, Kind: kind, Name: name, Ref: ref}
		byID[id] = g
		groups = append(groups, g)
		return g
	}
	seen := map[int32]bool{}
	var members []member
	var add func(g *Group, pid int32, deep bool)
	add = func(g *Group, pid int32, deep bool) {
		if seen[pid] {
			return
		}
		p := m.proc(pid)
		if p == nil {
			return
		}
		seen[pid] = true
		members = append(members, member{g, pid})
		if !deep {
			return
		}
		kids, _ := p.Children()
		for _, k := range kids {
			add(g, k.Pid, true)
		}
	}

	self := int32(os.Getpid())
	engine := group("engine", "engine", "LaraMonster engine", "")
	// Pools and services first, so their process trees are not counted twice.
	versions := m.rt.fpm.PIDs()
	for _, v := range sortedKeys(versions) {
		version, site, own := strings.Cut(v, "@")
		label := "PHP " + version
		if own {
			label += " · " + site + " (own settings)"
		}
		g := group("php-"+v, "php", label, version)
		for _, s := range active {
			if own && s.Name == site || !own && s.PHP == v && !m.rt.fpm.OwnPool(s.Name) {
				g.Sites++
			}
		}
		for _, pid := range versions[v] {
			add(g, int32(pid), true)
		}
	}
	svcPIDs := m.rt.svc.PIDs()
	for _, name := range sortedKeys(svcPIDs) {
		title := name
		if d, ok := services.Get(name); ok {
			title = d.Title
		}
		add(group("svc-"+name, "service", title, name), int32(svcPIDs[name]), true)
	}
	// The engine itself, then whatever else it started (jobs, the app's web view).
	add(engine, self, false)
	app := m.appProcess()
	if p := m.proc(self); p != nil {
		kids, _ := p.Children()
		for _, k := range kids {
			name, _ := k.Name()
			if isWebView(name) {
				add(group("app", "app", "LaraMonster app window", ""), k.Pid, true)
			} else {
				add(group("jobs", "jobs", "Running tasks", ""), k.Pid, true)
			}
		}
	}
	if app != 0 && app != self {
		add(group("app", "app", "LaraMonster app window", ""), app, true)
	}
	if app == self {
		engine.Name = "LaraMonster app & engine"
	}
	// The user's own servers (Node, artisan serve…): not ours, but they are
	// part of what the sites cost.
	for site, pid := range m.ownServers(st.Sites) {
		add(group("server-"+site, "server", site, site), pid, true)
	}

	cpuByPID := map[int32]float64{}
	for _, mb := range members {
		p := m.procs[mb.pid]
		c, err := p.Percent(0)
		if err != nil {
			continue
		}
		c /= float64(ncpu)
		cpuByPID[mb.pid] = c
		mb.g.CPU += c
		mb.g.Mem += memOf(p)
		mb.g.Procs++
	}
	for pid := range m.procs {
		if !seen[pid] {
			delete(m.procs, pid)
		}
	}

	// --- per-site PHP load from php-fpm's status pages ---
	note := ""
	socks := m.rt.fpm.StatusSockets()
	if len(socks) == 0 && len(versions) > 0 {
		note = "Per-site CPU needs php-fpm, which isn't available on Windows; request counts still work."
	}
	var statuses []*php.FPMStatus
	for _, sock := range socks {
		if fs, err := php.ReadFPMStatus(sock); err == nil {
			statuses = append(statuses, fs)
		}
	}
	m.mu.Lock()
	// CPU: a php-fpm worker busy with a script counts toward that script's
	// site. Requests: counted by the web server, so every kind of site
	// (PHP, static, your own server) shows up.
	sitesCPU := map[string]float64{}
	sitesMem := map[string]int64{}
	for _, fs := range statuses {
		for _, wp := range fs.Processes {
			if wp.State == "Idle" || strings.HasPrefix(wp.URI, php.StatusPath) {
				continue
			}
			if site := php.SiteOfScript(wp.Script, roots); site != "" {
				sitesCPU[site] += cpuByPID[int32(wp.PID)]
				sitesMem[site] = max(sitesMem[site], wp.Memory)
			}
		}
	}
	enabled := map[string]bool{}
	for _, s := range st.Sites {
		if s.Enabled {
			enabled[s.Name] = true
		}
	}
	traffic := server.SiteTraffic()
	var loads []SiteLoad
	for name := range enabled {
		w := m.site(name)
		t := traffic[name]
		if w.seen && t.Total > w.total {
			w.reqs = append(w.reqs, reqMark{now, int(t.Total - w.total)})
			w.lastSeen = now
		}
		if t.Running > 0 {
			w.lastSeen = now
		}
		w.total, w.seen = t.Total, true
		w.cpu = appendCapped(w.cpu, sitesCPU[name], siteWindow)
		if sitesMem[name] > 0 {
			w.memory = sitesMem[name]
		}
		cut := now.Add(-time.Minute)
		w.reqs = slices.DeleteFunc(w.reqs, func(r reqMark) bool { return r.at.Before(cut) })
		l := SiteLoad{Name: name, CPU: avg(w.cpu), Running: t.Running, Longest: t.Longest.Seconds(), URI: t.URI, Memory: w.memory}
		if l.URI == "" {
			l.URI = w.uri
		} else {
			w.uri = l.URI
		}
		for _, r := range w.reqs {
			l.Requests += r.n
		}
		if l.Requests > 0 || l.Running > 0 || l.CPU >= 0.05 {
			loads = append(loads, l)
		}
	}
	for name := range m.sites {
		if !enabled[name] {
			delete(m.sites, name)
		}
	}
	slices.SortFunc(loads, func(a, b SiteLoad) int {
		if a.CPU != b.CPU {
			return cmpDesc(a.CPU, b.CPU)
		}
		return b.Requests - a.Requests
	})

	// --- system ---
	sys := SystemStats{CPUs: ncpu}
	if c, err := cpu.Percent(0, false); err == nil && len(c) > 0 {
		sys.CPU = c[0]
	}
	if l, err := load.Avg(); err == nil {
		sys.Load1 = l.Load1
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		sys.MemTotal, sys.MemAvail = vm.Total, vm.Available
		sys.MemUsed = vm.Total - vm.Available
	}
	if sw, err := mem.SwapMemory(); err == nil {
		sys.SwapTotal, sys.SwapUsed = sw.Total, sw.Used
	}

	rep := MonitorReport{At: now.UnixMilli(), System: sys, Sites: loads, SiteLoadNote: note}
	live := map[string]bool{}
	for _, g := range groups {
		if g.Procs == 0 {
			continue
		}
		live[g.ID] = true
		m.groupCPU[g.ID] = appendCapped(m.groupCPU[g.ID], g.CPU, 15)
		g.CPUAvg = avg(m.groupCPU[g.ID])
		rep.Groups = append(rep.Groups, *g)
		if g.Kind != "server" { // the user's own servers aren't LaraMonster's
			rep.CPU += g.CPU
			rep.Mem += g.Mem
			rep.Procs += g.Procs
		}
	}
	for id := range m.groupCPU {
		if !live[id] {
			delete(m.groupCPU, id)
		}
	}
	slices.SortStableFunc(rep.Groups, func(a, b Group) int { return cmpDesc(float64(a.Mem), float64(b.Mem)) })
	m.samples++
	rep.Warm = m.samples > 1
	if rep.Warm {
		m.history = appendCapped(m.history, Sample{T: rep.At, CPU: rep.CPU, Mem: rep.Mem, SysCPU: sys.CPU, SysMem: sys.MemUsed}, historyLen)
	}
	rep.Recommendations = recommend(rep, st, m.sites, time.Since(m.started))
	m.report = rep
	m.mu.Unlock()
}

func (m *monitor) site(name string) *siteWin {
	w, ok := m.sites[name]
	if !ok {
		w = &siteWin{}
		m.sites[name] = w
	}
	return w
}

// proc returns a cached process handle: CPU percent is measured between
// two calls on the same handle.
func (m *monitor) proc(pid int32) *process.Process {
	if p, ok := m.procs[pid]; ok {
		if running, _ := p.IsRunning(); running {
			return p
		}
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		delete(m.procs, pid)
		return nil
	}
	m.procs[pid] = p
	return p
}

// appProcess finds the desktop app, which may run next to a CLI engine.
// A search runs at most every 10 seconds.
func (m *monitor) appProcess() int32 {
	m.ownMu.Lock()
	defer m.ownMu.Unlock()
	if m.appPID != 0 {
		if ok, _ := process.PidExists(m.appPID); ok {
			return m.appPID
		}
		m.appPID = 0
	}
	if time.Since(m.appAt) < 10*time.Second {
		return 0
	}
	m.appAt = time.Now()
	if exe, _ := os.Executable(); isAppBinary(exe) {
		m.appPID = int32(os.Getpid())
		return m.appPID
	}
	procs, _ := process.Processes()
	for _, p := range procs {
		if exe, err := p.Exe(); err == nil && isAppBinary(exe) {
			m.appPID = p.Pid
			break
		}
	}
	return m.appPID
}

func isAppBinary(exe string) bool {
	b := strings.ToLower(filepath.Base(exe))
	return strings.HasPrefix(b, "laramonster-app") || strings.HasPrefix(b, "laramonster-windows")
}

func isWebView(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "webkit") || strings.Contains(n, "webview") || strings.Contains(n, "webcontent")
}

// ownServers maps sites served by the user's own server to the process
// listening on that local port. Refreshed every 10 seconds.
func (m *monitor) ownServers(list []sites.Site) map[string]int32 {
	m.ownMu.Lock()
	defer m.ownMu.Unlock()
	ports := map[uint32]string{}
	for _, s := range list {
		if s.Proxy == "" || !s.Enabled {
			continue
		}
		host, port := proxyHostPort(s.Proxy)
		if (host == "127.0.0.1" || host == "localhost" || host == "::1") && port != 0 {
			ports[port] = s.Name
		}
	}
	if len(ports) == 0 {
		return nil
	}
	if time.Since(m.ownAt) < 10*time.Second && m.ownPIDs != nil {
		return m.ownPIDs
	}
	out := map[string]int32{}
	conns, _ := psnet.Connections("tcp")
	for _, c := range conns {
		if c.Status == "LISTEN" && c.Pid > 0 {
			if site, ok := ports[c.Laddr.Port]; ok {
				out[site] = c.Pid
			}
		}
	}
	m.ownPIDs, m.ownAt = out, time.Now()
	return out
}

func proxyHostPort(target string) (string, uint32) {
	norm, err := core.NormalizeProxy(target)
	if err != nil {
		return "", 0
	}
	hostport := norm[strings.Index(norm, "://")+3:]
	host, port := hostport, "80"
	if i := strings.LastIndex(hostport, ":"); i > 0 && !strings.HasSuffix(hostport, "]") {
		host, port = hostport[:i], hostport[i+1:]
	} else if strings.HasPrefix(norm, "https") {
		port = "443"
	}
	n, _ := strconv.Atoi(port)
	return strings.Trim(host, "[]"), uint32(n)
}

// memOf is the process's proportional memory on Linux (shared pages, like
// php-fpm workers forked from one master, are split instead of counted per
// process), resident memory elsewhere.
func memOf(p *process.Process) uint64 {
	if runtime.GOOS == "linux" {
		if f, err := os.Open("/proc/" + strconv.Itoa(int(p.Pid)) + "/smaps_rollup"); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if rest, ok := strings.CutPrefix(sc.Text(), "Pss:"); ok {
					kb, _ := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
					return kb * 1024
				}
			}
		}
	}
	if mi, err := p.MemoryInfo(); err == nil {
		return mi.RSS
	}
	return 0
}

func appendCapped[T any](s []T, v T, n int) []T {
	s = append(s, v)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func avg(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	t := 0.0
	for _, v := range s {
		t += v
	}
	return t / float64(len(s))
}

func cmpDesc(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
