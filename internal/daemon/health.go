package daemon

import (
	"net"
	"net/url"
	"slices"
	"time"

	"laramonster/internal/sites"
)

type probe struct {
	up bool
	at time.Time
}

// withHealth says, per site, whether it would answer right now: off, up, or
// down with a reason the UI can show.
func (r *Runtime) withHealth(list []sites.Site) []sites.Site {
	pools := r.fpm.Versions()
	for i := range list {
		s := &list[i]
		switch {
		case !s.Enabled:
			s.Health = "off"
		case s.Proxy != "":
			if r.reachable(s.Proxy) {
				s.Health = "up"
			} else {
				s.Health, s.Reason = "down", "Nothing is answering at "+s.Proxy+". Start your server."
			}
		case s.PHP != "" && !slices.Contains(pools, s.PHP):
			s.Health, s.Reason = "down", "PHP "+s.PHP+" is not running."
		default:
			s.Health = "up"
		}
	}
	return list
}

// reachable dials the target, at most every few seconds per target: the
// dashboard polls status every two seconds.
func (r *Runtime) reachable(target string) bool {
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	if p, ok := r.health[target]; ok && time.Since(p.at) < 4*time.Second {
		return p.up
	}
	up := false
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		if c, err := net.DialTimeout("tcp", net.JoinHostPort(u.Hostname(), port), 400*time.Millisecond); err == nil {
			c.Close()
			up = true
		}
	}
	if r.health == nil {
		r.health = map[string]probe{}
	}
	r.health[target] = probe{up, time.Now()}
	return up
}
