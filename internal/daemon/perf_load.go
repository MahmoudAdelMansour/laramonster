package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"laramonster/internal/core"
	"laramonster/internal/php"
	"laramonster/internal/sites"
)

// LoadOptions: Users browse Path at once, each starting a new request as
// soon as the last one answers, for Seconds.
type LoadOptions struct {
	Site    string `json:"site"`
	Path    string `json:"path"`
	Users   int    `json:"users"`
	Seconds int    `json:"seconds"`
}

// LoadPoint is one half-second of a load test, for the chart.
type LoadPoint struct {
	T       float64 `json:"t"`       // seconds since the start
	RPS     float64 `json:"rps"`     // answers per second in this slice
	P95     float64 `json:"p95"`     // ms
	Busy    int     `json:"busy"`    // PHP workers handling a request
	CPU     float64 `json:"cpu"`     // % of one core, the site's PHP processes
	MemMB   float64 `json:"mem_mb"`  // their memory
	Errors  int     `json:"errors"`  // in this slice
	Workers int     `json:"workers"` // PHP worker processes
}

// LoadResult summarises a load test.
type LoadResult struct {
	URL         string         `json:"url"`
	Users       int            `json:"users"`
	Seconds     float64        `json:"seconds"`
	Requests    int            `json:"requests"`
	RPS         float64        `json:"rps"`
	Errors      int            `json:"errors"`
	Statuses    map[string]int `json:"statuses"`
	AvgMS       float64        `json:"avg_ms"`
	P50         float64        `json:"p50"`
	P90         float64        `json:"p90"`
	P95         float64        `json:"p95"`
	P99         float64        `json:"p99"`
	MaxMS       float64        `json:"max_ms"`
	PeakCPU     float64        `json:"peak_cpu"`
	PeakMemMB   float64        `json:"peak_mem_mb"`
	MaxWorkers  int            `json:"max_workers"`  // pm.max_children
	PeakBusy    int            `json:"peak_busy"`    //
	WorkersFull bool           `json:"workers_full"` // requests had to wait for a free worker
	Series      []LoadPoint    `json:"series"`
	Findings    []string       `json:"findings"`
}

const fpmMaxChildren = 10 // see php.fpmConf

func (rt *Runtime) runLoadTest(ctx context.Context, o LoadOptions, j *core.Job) (LoadResult, error) {
	var res LoadResult
	o.Users = min(max(o.Users, 1), 200)
	o.Seconds = min(max(o.Seconds, 5), 120)
	if o.Path == "" || o.Path[0] != '/' {
		o.Path = "/" + o.Path
	}
	s, err := findSite(o.Site)
	if err != nil {
		return res, err
	}
	if !s.Enabled {
		return res, errors.New(s.Name + " is switched off")
	}
	u, err := url.Parse(strings.TrimRight(s.URL, "/") + o.Path)
	if err != nil || u.Host == "" {
		return res, errors.New("that path isn't a valid address")
	}
	res.URL, res.Users, res.Statuses = u.String(), o.Users, map[string]int{}

	client := &http.Client{
		Timeout: 30 * time.Second,
		// Never follow redirects: a login redirect would hide what the page costs.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			MaxIdleConnsPerHost: o.Users, MaxConnsPerHost: o.Users,
			// Our own local sites, signed by LaraMonster's CA.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}

	type sample struct {
		at  time.Duration
		ms  float64
		err bool
	}
	var (
		mu      sync.Mutex
		samples []sample
	)
	start := time.Now()
	deadline := start.Add(time.Duration(o.Seconds) * time.Second)
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var wg sync.WaitGroup
	for range o.Users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for runCtx.Err() == nil {
				t0 := time.Now()
				req, _ := http.NewRequestWithContext(runCtx, http.MethodGet, u.String(), nil)
				req.Header.Set("User-Agent", "LaraMonster load test")
				resp, err := client.Do(req)
				ms := float64(time.Since(t0).Microseconds()) / 1000
				if runCtx.Err() != nil {
					return // cut off at the deadline: not a real answer
				}
				status := "error"
				failed := err != nil
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					status = fmt.Sprint(resp.StatusCode)
					failed = resp.StatusCode >= 500
				}
				mu.Lock()
				samples = append(samples, sample{at: t0.Sub(start), ms: ms, err: failed})
				res.Statuses[status]++
				mu.Unlock()
			}
		}()
	}

	// Watch the site's PHP while the users browse.
	sock, pids := rt.sitePool(s)
	procs := map[int32]*process.Process{}
	lastCPU, lastAt := 0.0, time.Now()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	seen := 0
	for done := false; !done; {
		select {
		case <-runCtx.Done():
			done = true
		case now := <-tick.C:
			p := LoadPoint{T: round1(now.Sub(start).Seconds())}
			mu.Lock()
			slice := samples[seen:]
			seen = len(samples)
			mu.Unlock()
			var lat []float64
			for _, x := range slice {
				lat = append(lat, x.ms)
				if x.err {
					p.Errors++
				}
			}
			p.RPS = round1(float64(len(slice)) / now.Sub(lastAt).Seconds())
			p.P95 = pct(lat, 95)
			if sock != "" {
				if st, err := php.ReadFPMStatus(sock); err == nil {
					p.Busy, p.Workers = st.Active, st.Total
					res.WorkersFull = res.WorkersFull || st.MaxHit > 0
				}
			}
			cpu, mem := poolUsage(procs, pids)
			if lastCPU > 0 {
				p.CPU = round1((cpu - lastCPU) / now.Sub(lastAt).Seconds() * 100)
			}
			lastCPU, lastAt = cpu, now
			p.MemMB = round1(float64(mem) / (1 << 20))
			res.Series = append(res.Series, p)
			res.PeakCPU = max(res.PeakCPU, p.CPU)
			res.PeakMemMB = max(res.PeakMemMB, p.MemMB)
			res.PeakBusy = max(res.PeakBusy, p.Busy)
			elapsed := now.Sub(start).Seconds()
			j.Set(min(elapsed/float64(o.Seconds), 0.99), fmt.Sprintf("%d users · %.0f s of %d · %d requests", o.Users, elapsed, o.Seconds, seen))
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		return res, errors.New("the load test was stopped")
	}

	res.Seconds = round1(time.Since(start).Seconds())
	var all []float64
	sum := 0.0
	for _, x := range samples {
		all = append(all, x.ms)
		sum += x.ms
		if x.err {
			res.Errors++
		}
	}
	res.Requests = len(samples)
	if res.Requests == 0 {
		return res, errors.New("no request got an answer: is the site running?")
	}
	res.RPS = round1(float64(res.Requests) / float64(o.Seconds))
	res.AvgMS = round1(sum / float64(res.Requests))
	res.P50, res.P90, res.P95, res.P99 = pct(all, 50), pct(all, 90), pct(all, 95), pct(all, 99)
	res.MaxMS = round1(slices.Max(all))
	if sock != "" {
		res.MaxWorkers = fpmMaxChildren
	}
	res.Findings = loadFindings(res)
	return res, nil
}

// sitePool is the php-fpm status socket and process ids serving a site.
func (rt *Runtime) sitePool(s sites.Site) (string, []int) {
	key := s.PHP
	if rt.fpm.OwnPool(s.Name) {
		key = php.SiteKey(s.PHP, s.Name)
	}
	return rt.fpm.StatusSockets()[key], rt.fpm.PIDs()[key]
}

// poolUsage is the total CPU seconds and memory of the pool's processes
// (the php-fpm master and its workers).
func poolUsage(cache map[int32]*process.Process, roots []int) (cpu float64, mem uint64) {
	var all []*process.Process
	for _, pid := range roots {
		p, ok := cache[int32(pid)]
		if !ok {
			var err error
			if p, err = process.NewProcess(int32(pid)); err != nil {
				continue
			}
			cache[int32(pid)] = p
		}
		all = append(all, p)
		kids, _ := p.Children()
		all = append(all, kids...)
	}
	for _, p := range all {
		if t, err := p.Times(); err == nil {
			cpu += t.User + t.System
		}
		if m, err := p.MemoryInfo(); err == nil {
			mem += m.RSS
		}
	}
	return cpu, mem
}

func loadFindings(r LoadResult) []string {
	var f []string
	if r.Errors > 0 {
		f = append(f, fmt.Sprintf("%d of %d requests failed (5xx or no answer). Check the Logs page for the errors.", r.Errors, r.Requests))
	}
	if r.Statuses["302"]+r.Statuses["301"] > r.Requests/2 {
		f = append(f, "Most answers were redirects (probably to a login page): the test measured the redirect, not the page.")
	}
	if r.WorkersFull || (r.MaxWorkers > 0 && r.PeakBusy >= r.MaxWorkers) {
		f = append(f, fmt.Sprintf("All %d PHP workers were busy: further requests waited in line. That wait is part of the slow times at %d users.", r.MaxWorkers, r.Users))
	}
	if r.P99 > 3*r.P50 && r.P50 > 0 {
		f = append(f, fmt.Sprintf("The slowest 1%% took %.0f ms, %.0f× the typical %.0f ms: something is slow only sometimes (a cache miss, a lock or a slow query).", r.P99, r.P99/r.P50, r.P50))
	}
	if r.PeakCPU > 80*float64(runtime.NumCPU()) {
		f = append(f, "PHP used most of the CPU it could: the page's own work (not waiting) limits how many users it serves.")
	}
	if len(f) == 0 {
		f = append(f, fmt.Sprintf("Steady: %d users got answers in %.0f ms (95%% within %.0f ms) with no errors.", r.Users, r.P50, r.P95))
	}
	return f
}

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	sort.Float64s(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1 // nearest rank
	return round1(s[max(0, min(i, len(s)-1))])
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
