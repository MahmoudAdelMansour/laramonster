package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"laramonster/internal/core"
	"laramonster/internal/sites"
)

// Recommendation is one suggestion on the Monitor page, with buttons.
type Recommendation struct {
	ID      string      `json:"id"`
	Level   string      `json:"level"` // critical, warning, tip
	Title   string      `json:"title"`
	Detail  string      `json:"detail"`
	Actions []RecAction `json:"actions,omitempty"`
}

type RecAction struct {
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // stop-service, sites-off, site, sites-filter
	Service string   `json:"service,omitempty"`
	Sites   []string `json:"sites,omitempty"`
	Site    string   `json:"site,omitempty"`
	Filter  string   `json:"filter,omitempty"`
}

const (
	lowMemShare     = 0.10             // available memory below this share of RAM
	busySiteCores   = 0.8              // CPU cores, 1-minute average
	slowRequest     = 30.0             // seconds
	busyServiceCore = 1.0              // CPU cores, 30-second average
	bigShareOfRAM   = 0.15             // one group using this share of RAM
	idleAfter       = 10 * time.Minute // no visits for this long
	idlePoolMinSize = 40 << 20         // only worth it above 40 MB
)

func recommend(rep MonitorReport, st Status, wins map[string]*siteWin, watched time.Duration) []Recommendation {
	var out []Recommendation
	sys := rep.System
	active := []sites.Site{}
	for _, s := range st.Sites {
		if s.Enabled {
			active = append(active, s)
		}
	}
	users := serviceUsers(active)
	// Thresholds are in cores: one runaway request can only use one core,
	// which is a small share of a many-core machine.
	core := 100 / float64(max(sys.CPUs, 1))

	// 1. The computer is short of memory.
	if sys.MemTotal > 0 && float64(sys.MemAvail) < lowMemShare*float64(sys.MemTotal) {
		r := Recommendation{ID: "low-memory", Level: "critical", Title: "Your computer is low on memory",
			Detail: fmt.Sprintf("Only %s of %s is free. LaraMonster uses %s of it.", size(sys.MemAvail), size(sys.MemTotal), size(rep.Mem))}
		for _, g := range rep.Groups {
			if g.Kind == "service" && len(users[g.Ref]) == 0 {
				r.Actions = append(r.Actions, RecAction{Label: "Stop " + g.Name + " (" + size(g.Mem) + ")", Kind: "stop-service", Service: g.Ref})
			}
		}
		out = append(out, r)
	}

	// 2. The CPU is busy: say whether LaraMonster is the cause.
	if rep.Warm && avgSys(rep.History, 15) >= 85 {
		if top := topGroupCPU(rep.Groups); top != nil && rep.CPU >= 0.5*sys.CPU {
			out = append(out, Recommendation{ID: "busy-cpu", Level: "warning", Title: "LaraMonster is keeping your CPU busy",
				Detail: fmt.Sprintf("%s uses %.0f%% of your CPU right now.", top.Name, top.CPU)})
		} else {
			out = append(out, Recommendation{ID: "busy-cpu-other", Level: "tip", Title: "Your CPU is busy, mostly outside LaraMonster",
				Detail: fmt.Sprintf("LaraMonster uses %.0f%% of the %.0f%% in use. Check your system monitor for the rest.", rep.CPU, sys.CPU)})
		}
	}

	// 3. A site is causing load.
	for _, l := range rep.Sites {
		siteActions := []RecAction{{Label: "Site settings", Kind: "site", Site: l.Name}, {Label: "Switch off", Kind: "sites-off", Sites: []string{l.Name}}}
		switch {
		case l.Longest >= slowRequest:
			out = append(out, Recommendation{ID: "slow-" + l.Name, Level: "warning", Title: l.Name + " has a request running for " + duration(l.Longest),
				Detail: "Still working on " + orDash(l.URI) + ". A stuck loop, a slow query or a job running in a web request can hold a PHP worker this long.", Actions: siteActions})
		case l.CPU >= busySiteCores*core:
			out = append(out, Recommendation{ID: "busy-" + l.Name, Level: "warning", Title: fmt.Sprintf("%s is using %.0f%% of your CPU", l.Name, l.CPU),
				Detail: fmt.Sprintf("%s in the last minute, most recently %s. If you aren't using it, switch it off.", count(l.Requests, "request"), orDash(l.URI)), Actions: siteActions})
		}
	}

	// 4. A service is busy or big.
	for _, g := range rep.Groups {
		if g.Kind != "service" && g.Kind != "server" {
			continue
		}
		who := "No active site uses it."
		if u := users[g.Ref]; len(u) > 0 {
			who = "Used by " + list(u, 3) + "."
		}
		var actions []RecAction
		if g.Kind == "service" {
			actions = []RecAction{{Label: "Stop " + g.Name, Kind: "stop-service", Service: g.Ref}}
		} else {
			who = "This is your own server for " + g.Ref + "."
			actions = []RecAction{{Label: "Site settings", Kind: "site", Site: g.Ref}}
		}
		switch {
		case g.CPUAvg >= busyServiceCore*core:
			out = append(out, Recommendation{ID: "busy-" + g.ID, Level: "warning", Title: fmt.Sprintf("%s is using %.0f%% of your CPU", g.Name, g.CPUAvg),
				Detail: who + " A long query, an import or a re-index can cause this.", Actions: actions})
		case sys.MemTotal > 0 && float64(g.Mem) >= bigShareOfRAM*float64(sys.MemTotal):
			out = append(out, Recommendation{ID: "big-" + g.ID, Level: "warning", Title: fmt.Sprintf("%s is using %s of memory", g.Name, size(g.Mem)),
				Detail: fmt.Sprintf("That's %.0f%% of your RAM. %s", 100*float64(g.Mem)/float64(sys.MemTotal), who), Actions: actions})
		}
	}

	// 5. A service runs that no active site uses.
	for _, g := range rep.Groups {
		if g.Kind != "service" || g.Ref == "mailpit" || len(users[g.Ref]) > 0 || hasID(out, "big-"+g.ID) || hasID(out, "busy-"+g.ID) {
			continue
		}
		out = append(out, Recommendation{ID: "unused-" + g.Ref, Level: "tip", Title: "Stop " + g.Name + "? No active site uses it",
			Detail:  fmt.Sprintf("None of your %d active sites point at it in their .env. Stopping it frees about %s.", len(active), size(g.Mem)),
			Actions: []RecAction{{Label: "Stop " + g.Name, Kind: "stop-service", Service: g.Ref}}})
	}

	// 6. A PHP version only serves sites nobody visited lately.
	pools := 0
	for _, g := range rep.Groups {
		if g.Kind == "php" {
			pools++
		}
	}
	if pools > 1 && watched >= idleAfter {
		for _, g := range rep.Groups {
			if g.Kind != "php" || g.Mem < idlePoolMinSize {
				continue
			}
			var served []string
			idle := true
			for _, s := range active {
				if s.PHP != g.Ref || s.Proxy != "" {
					continue
				}
				served = append(served, s.Name)
				if w := wins[s.Name]; w != nil && time.Since(w.lastSeen) < idleAfter {
					idle = false
				}
			}
			if idle && len(served) > 0 {
				out = append(out, Recommendation{ID: "idle-" + g.ID, Level: "tip", Title: g.Name + " only serves sites nobody visited lately",
					Detail:  fmt.Sprintf("%s had no visits in the last %d minutes. Switching %s off stops %s and frees about %s.", list(served, 3), int(idleAfter.Minutes()), them(len(served)), g.Name, size(g.Mem)),
					Actions: []RecAction{{Label: "Switch off " + count(len(served), "site"), Kind: "sites-off", Sites: served}}})
			}
		}
	}

	// 7. Sites that don't answer.
	var down []string
	for _, s := range active {
		if s.Health == "down" {
			down = append(down, s.Name)
		}
	}
	if len(down) > 0 {
		out = append(out, Recommendation{ID: "down", Level: "warning", Title: count(len(down), "site") + map[bool]string{true: " is", false: " are"}[len(down) == 1] + " not responding",
			Detail:  list(down, 4) + ". Start their server, or switch them off if you don't need them.",
			Actions: []RecAction{{Label: "Show them", Kind: "sites-filter", Filter: "down"}}})
	}

	// 8. Active folders with nothing to serve (not projects).
	var empty []string
	for _, s := range active {
		if s.Proxy == "" && !hasIndex(s.Root) {
			empty = append(empty, s.Name)
		}
	}
	if len(empty) > 0 {
		one := len(empty) == 1
		verb := map[bool]string{true: " has", false: " have"}[one]
		out = append(out, Recommendation{ID: "no-index", Level: "tip", Title: count(len(empty), "active folder") + verb + " nothing to serve",
			Detail: list(empty, 5) + verb + " no index.php or index.html, so " + map[bool]string{true: "it's probably not a project", false: "they're probably not projects"}[one] +
				". Switch " + them(len(empty)) + " off to tidy your sites list.",
			Actions: []RecAction{{Label: "Switch " + them(len(empty)) + " off", Kind: "sites-off", Sites: empty}}})
	}
	return out
}

// serviceUsers lists, per service, the active sites whose .env uses it.
// .env files are re-read at most every 30 seconds.
var (
	usersMu  sync.Mutex
	usersAt  time.Time
	usersKey string
	usersVal map[string][]string
)

func serviceUsers(active []sites.Site) map[string][]string {
	names := make([]string, len(active))
	for i, s := range active {
		names[i] = s.Name + "=" + s.Path
	}
	key := strings.Join(names, ",")
	usersMu.Lock()
	defer usersMu.Unlock()
	if key == usersKey && time.Since(usersAt) < 30*time.Second {
		return usersVal
	}
	out := map[string][]string{}
	for _, s := range active {
		f, err := core.ReadEnv(s.Path)
		if err != nil || !f.Exists {
			continue
		}
		get := func(k string) string { v, _ := core.EnvValue(f.Content, k); return strings.ToLower(v) }
		switch get("DB_CONNECTION") {
		case "mysql", "mariadb":
			out["mysql"] = append(out["mysql"], s.Name)
		case "pgsql":
			out["postgres"] = append(out["postgres"], s.Name)
		}
		for _, k := range []string{"CACHE_STORE", "CACHE_DRIVER", "QUEUE_CONNECTION", "SESSION_DRIVER", "BROADCAST_CONNECTION", "BROADCAST_DRIVER"} {
			if get(k) == "redis" {
				out["redis"] = append(out["redis"], s.Name)
				break
			}
		}
		if get("SCOUT_DRIVER") == "meilisearch" {
			out["meilisearch"] = append(out["meilisearch"], s.Name)
		}
		if p := get("MAIL_PORT"); p == "1025" {
			out["mailpit"] = append(out["mailpit"], s.Name)
		}
	}
	usersKey, usersAt, usersVal = key, time.Now(), out
	return out
}

func hasIndex(root string) bool {
	for _, f := range []string{"index.php", "index.html", "index.htm"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			return true
		}
	}
	return false
}

func topGroupCPU(gs []Group) *Group {
	var top *Group
	for i := range gs {
		if gs[i].Kind != "server" && (top == nil || gs[i].CPU > top.CPU) {
			top = &gs[i]
		}
	}
	return top
}

func avgSys(h []Sample, n int) float64 {
	if len(h) > n {
		h = h[len(h)-n:]
	}
	if len(h) == 0 {
		return 0
	}
	t := 0.0
	for _, s := range h {
		t += s.SysCPU
	}
	return t / float64(len(h))
}

func hasID(rs []Recommendation, id string) bool {
	return slices.ContainsFunc(rs, func(r Recommendation) bool { return r.ID == id })
}

func size(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	}
}

func duration(sec float64) string {
	if sec >= 120 {
		return fmt.Sprintf("%.0f minutes", sec/60)
	}
	return fmt.Sprintf("%.0f seconds", sec)
}

func list(names []string, max int) string {
	if len(names) <= max {
		if len(names) <= 1 {
			return strings.Join(names, "")
		}
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(" and %d more", len(names)-max)
}

func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
