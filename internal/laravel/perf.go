package laravel

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// PerfReport is what recorded web requests say about a site's speed.
type PerfReport struct {
	Requests   int           `json:"requests"`    // web requests analysed
	AvgMS      float64       `json:"avg_ms"`      // across all of them
	P95MS      float64       `json:"p95_ms"`      //
	AvgQueries float64       `json:"avg_queries"` //
	Routes     []RouteStat   `json:"routes"`      // slowest first
	NPlusOne   []NPlusOne    `json:"n_plus_one"`  // worst first
	Slow       []SlowQuery   `json:"slow"`        // slowest first
	Packages   []PackageCost `json:"packages"`    // costliest first
}

type RouteStat struct {
	Route      string  `json:"route"` // "GET posts/{post}"
	Count      int     `json:"count"`
	AvgMS      float64 `json:"avg_ms"`
	P95MS      float64 `json:"p95_ms"`
	MaxMS      float64 `json:"max_ms"`
	AvgQueries float64 `json:"avg_queries"`
	MaxQueries int     `json:"max_queries"`
	MaxMemory  int64   `json:"max_memory"`
	Slowest    string  `json:"slowest"` // trace id of the slowest request
}

// NPlusOne is one query run many times from one line in a single request:
// usually a relation loaded inside a loop.
type NPlusOne struct {
	SQL        string   `json:"sql"`
	Src        string   `json:"src"`
	Pkg        string   `json:"pkg,omitempty"`
	PerRequest int      `json:"per_request"` // most runs in one request
	Requests   int      `json:"requests"`    // requests where it happened
	MS         float64  `json:"ms"`          // time in the worst request
	Routes     []string `json:"routes"`
	Relation   string   `json:"relation,omitempty"` // guessed relation to eager load
	Trace      string   `json:"trace"`              // the worst request
}

type SlowQuery struct {
	SQL      string  `json:"sql"`
	Src      string  `json:"src,omitempty"`
	Pkg      string  `json:"pkg,omitempty"`
	Conn     string  `json:"conn,omitempty"`
	Count    int     `json:"count"`
	AvgMS    float64 `json:"avg_ms"`
	MaxMS    float64 `json:"max_ms"`
	Bindings []any   `json:"bindings,omitempty"`
}

// PackageCost is time third-party code spent in queries and listeners.
type PackageCost struct {
	Package    string  `json:"package"`
	Queries    int     `json:"queries"`
	QueryMS    float64 `json:"query_ms"`
	Listeners  int     `json:"listeners"`
	ListenerMS float64 `json:"listener_ms"`
	Requests   int     `json:"requests"` // requests it took part in
	Share      float64 `json:"share"`    // of all request time, 0..1
}

// Thresholds: a query repeated this often in one request is an N+1
// suspect; one this slow (ms) is a slow query.
const (
	NPlusOneMin = 5
	SlowQueryMS = 100.0
)

var numberSeg = regexp.MustCompile(`/\d+(/|$)`)

func routeKey(t Trace) string {
	method := t.Method
	if method == "" {
		method = "GET"
	}
	if t.Route != "" {
		return method + " /" + strings.TrimPrefix(t.Route, "/")
	}
	uri := strings.SplitN(t.URI, "?", 2)[0]
	if uri == "" {
		uri = strings.TrimPrefix(t.Title, method+" ")
	}
	for numberSeg.MatchString(uri) {
		uri = numberSeg.ReplaceAllString(uri, "/{id}$1")
	}
	return method + " /" + strings.TrimPrefix(uri, "/")
}

var readQuery = regexp.MustCompile(`(?is)^\s*(select|with)\b`)

// AnalyzePerf summarises recorded traces. pkgOf maps a listener's class to
// its Composer package (nil: listeners aren't attributed).
func AnalyzePerf(traces []Trace, pkgOf func(class string) string) PerfReport {
	var r PerfReport
	type routeAcc struct {
		times   []float64
		queries int
		maxQ    int
		mem     int64
		slowest Trace
	}
	routes := map[string]*routeAcc{}
	n1 := map[string]*NPlusOne{}
	slow := map[string]*SlowQuery{}
	pkgs := map[string]*PackageCost{}
	var all []float64
	total, queries := 0.0, 0

	for _, t := range traces {
		if t.Kind != "request" {
			continue
		}
		r.Requests++
		all = append(all, t.Duration)
		total += t.Duration
		queries += t.Queries
		key := routeKey(t)
		acc := routes[key]
		if acc == nil {
			acc = &routeAcc{}
			routes[key] = acc
		}
		acc.times = append(acc.times, t.Duration)
		acc.queries += t.Queries
		acc.maxQ = max(acc.maxQ, t.Queries)
		acc.mem = max(acc.mem, t.Memory)
		if acc.slowest.ID == "" || t.Duration > acc.slowest.Duration {
			acc.slowest = t
		}

		seenPkg := map[string]bool{}
		for _, q := range t.SQL {
			// Lazy loading reads; repeated writes are a different problem.
			if q.N >= NPlusOneMin && q.Src != "" && readQuery.MatchString(q.SQL) {
				k := q.SQL + "|" + q.Src
				s := n1[k]
				if s == nil {
					s = &NPlusOne{SQL: q.SQL, Src: q.Src, Pkg: q.Pkg, Relation: guessRelation(q.SQL)}
					n1[k] = s
				}
				s.Requests++
				if !slices.Contains(s.Routes, key) {
					s.Routes = append(s.Routes, key)
				}
				if q.N > s.PerRequest {
					s.PerRequest, s.MS, s.Trace = q.N, q.MS, t.ID
				}
			}
			if q.Max >= SlowQueryMS {
				k := q.SQL + "|" + q.Src
				s := slow[k]
				if s == nil {
					s = &SlowQuery{SQL: q.SQL, Src: q.Src, Pkg: q.Pkg, Conn: q.Conn, Bindings: q.Bindings}
					slow[k] = s
				}
				s.AvgMS = (s.AvgMS*float64(s.Count) + q.MS) / float64(s.Count+q.N)
				s.Count += q.N
				s.MaxMS = max(s.MaxMS, q.Max)
			}
			if q.Pkg != "" {
				p := pkgCost(pkgs, q.Pkg)
				p.Queries += q.N
				p.QueryMS += q.MS
				if !seenPkg[q.Pkg] {
					seenPkg[q.Pkg] = true
					p.Requests++
				}
			}
		}
		if pkgOf != nil {
			for _, e := range t.Events {
				for _, l := range e.Ran {
					name := strings.SplitN(l.Name, "@", 2)[0]
					pkg := pkgOf(name)
					if pkg == "" {
						continue
					}
					p := pkgCost(pkgs, pkg)
					p.Listeners++
					p.ListenerMS += l.Duration
					if !seenPkg[pkg] {
						seenPkg[pkg] = true
						p.Requests++
					}
				}
			}
		}
	}
	if r.Requests == 0 {
		r.Routes, r.NPlusOne, r.Slow, r.Packages = []RouteStat{}, []NPlusOne{}, []SlowQuery{}, []PackageCost{}
		return r
	}
	r.AvgMS = round2(total / float64(r.Requests))
	r.P95MS = percentile(all, 95)
	r.AvgQueries = round2(float64(queries) / float64(r.Requests))

	for k, a := range routes {
		sum := 0.0
		for _, d := range a.times {
			sum += d
		}
		r.Routes = append(r.Routes, RouteStat{
			Route: k, Count: len(a.times), AvgMS: round2(sum / float64(len(a.times))), P95MS: percentile(a.times, 95),
			MaxMS: round2(slices.Max(a.times)), AvgQueries: round2(float64(a.queries) / float64(len(a.times))),
			MaxQueries: a.maxQ, MaxMemory: a.mem, Slowest: a.slowest.ID,
		})
	}
	sort.Slice(r.Routes, func(i, j int) bool { return r.Routes[i].AvgMS > r.Routes[j].AvgMS })
	for _, s := range n1 {
		r.NPlusOne = append(r.NPlusOne, *s)
	}
	sort.Slice(r.NPlusOne, func(i, j int) bool { return r.NPlusOne[i].PerRequest > r.NPlusOne[j].PerRequest })
	for _, s := range slow {
		s.AvgMS = round2(s.AvgMS)
		r.Slow = append(r.Slow, *s)
	}
	sort.Slice(r.Slow, func(i, j int) bool { return r.Slow[i].MaxMS > r.Slow[j].MaxMS })
	for _, p := range pkgs {
		p.QueryMS, p.ListenerMS = round2(p.QueryMS), round2(p.ListenerMS)
		if total > 0 {
			p.Share = round2((p.QueryMS + p.ListenerMS) / total)
		}
		r.Packages = append(r.Packages, *p)
	}
	sort.Slice(r.Packages, func(i, j int) bool {
		return r.Packages[i].QueryMS+r.Packages[i].ListenerMS > r.Packages[j].QueryMS+r.Packages[j].ListenerMS
	})
	r.NPlusOne, r.Slow = nonEmpty(r.NPlusOne), nonEmpty(r.Slow)
	r.Packages = nonEmpty(r.Packages)
	return r
}

func pkgCost(m map[string]*PackageCost, name string) *PackageCost {
	p := m[name]
	if p == nil {
		p = &PackageCost{Package: name}
		m[name] = p
	}
	return p
}

func nonEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	// Nearest rank: the smallest value with p% of the values at or below it.
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return round2(s[max(0, min(i, len(s)-1))])
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

var lazyQuery = regexp.MustCompile("(?i)from\\s+[`\"']?(\\w+)[`\"']?\\s+where\\s+(?:[`\"']?\\w+[`\"']?\\.)?[`\"']?(\\w+)[`\"']?\\s*(?:=|in)\\s*\\(?\\?")

// guessRelation names the relation an N+1 query most likely lazy-loads:
// "where comments.post_id = ?" is a post's comments; "where users.id = ?"
// is a belongs-to user.
func guessRelation(sql string) string {
	m := lazyQuery.FindStringSubmatch(sql)
	if m == nil {
		return ""
	}
	table, col := strings.ToLower(m[1]), strings.ToLower(m[2])
	if col == "id" {
		return camel(singular(table))
	}
	return camel(table)
}

func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "sses"), strings.HasSuffix(s, "xes"):
		return strings.TrimSuffix(s, "es")
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return strings.TrimSuffix(s, "s")
	}
	return s
}

func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// PackageResolver maps PHP classes to Composer packages using the
// project's vendor/composer/installed.json autoload prefixes.
func PackageResolver(projectDir string) func(string) string {
	b, err := os.ReadFile(filepath.Join(projectDir, "vendor", "composer", "installed.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Packages []struct {
			Name     string `json:"name"`
			Autoload struct {
				PSR4 map[string]any `json:"psr-4"`
			} `json:"autoload"`
		} `json:"packages"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	type prefix struct{ ns, pkg string }
	var list []prefix
	for _, p := range doc.Packages {
		if strings.HasPrefix(p.Name, "laravel/framework") || strings.HasPrefix(p.Name, "symfony/") {
			continue
		}
		for ns := range p.Autoload.PSR4 {
			if ns != "" {
				list = append(list, prefix{ns, p.Name})
			}
		}
	}
	// Longest prefix first.
	sort.Slice(list, func(i, j int) bool { return len(list[i].ns) > len(list[j].ns) })
	return func(class string) string {
		class = strings.TrimPrefix(class, "\\")
		for _, p := range list {
			if strings.HasPrefix(class, p.ns) {
				return p.pkg
			}
		}
		return ""
	}
}
