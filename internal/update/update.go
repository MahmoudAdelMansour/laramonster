// Package update asks monstersgate.com for the newest LaraMonster release
// and says whether it's newer than this build. The answer is cached for a
// few hours; development builds ("-dev") never report an update.
package update

import (
	"encoding/json"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"laramonster/internal/account"
	"laramonster/internal/core"
)

// Info is what the dashboard shows: nothing, or "Update available: v0.3.0".
type Info struct {
	Current     string `json:"current"`
	Latest      string `json:"latest,omitempty"`
	Available   bool   `json:"available"`
	DownloadURL string `json:"download_url,omitempty"`
	NotesURL    string `json:"notes_url,omitempty"`
	CheckedAt   string `json:"checked_at,omitempty"`
}

var (
	mu     sync.Mutex
	cached Info
	at     time.Time
)

const every = 6 * time.Hour

// Check returns the cached answer, refreshing it when it's older than 6 hours.
func Check() Info {
	mu.Lock()
	defer mu.Unlock()
	if !at.IsZero() && time.Since(at) < every {
		return cached
	}
	cached, at = fetch(account.BaseURL()+"/api/laramonster/latest", core.Version, platform()), time.Now()
	return cached
}

func platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS // windows, linux
	}
}

func fetch(url, current, plat string) Info {
	info := Info{Current: strings.TrimPrefix(current, "v"), CheckedAt: time.Now().Format(time.RFC3339)}
	client := &http.Client{Timeout: 8 * time.Second}
	res, err := client.Get(url)
	if err != nil {
		return info
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return info
	}
	var body struct {
		Version   string `json:"version"`
		NotesURL  string `json:"notes_url"`
		PageURL   string `json:"page_url"`
		Downloads map[string]struct {
			URL string `json:"url"`
		} `json:"downloads"`
	}
	if json.NewDecoder(res.Body).Decode(&body) != nil || body.Version == "" {
		return info
	}
	info.Latest, info.NotesURL = body.Version, body.NotesURL
	info.DownloadURL = body.PageURL
	if d, ok := body.Downloads[plat]; ok && d.URL != "" {
		info.DownloadURL = d.URL
	}
	info.Available = !isDev(info.Current) && Newer(body.Version, info.Current)
	return info
}

// isDev reports a development build: "-dev", or a git describe version
// built between tags ("0.1.0-beta.3-5-g660226a", "…-dirty").
func isDev(v string) bool {
	return strings.Contains(v, "dev") || strings.HasSuffix(v, "-dirty") || describe.MatchString(v)
}

var describe = regexp.MustCompile(`-\d+-g[0-9a-f]+$`)

// Newer reports whether version a is greater than b, following semver:
// "0.10.0" > "0.9.3", "0.1.0-beta.6" > "0.1.0-beta.5", and a release beats
// its own betas ("0.3.0" > "0.3.0-beta.1"). Anything after "+" is ignored.
func Newer(a, b string) bool { return compare(a, b) > 0 }

func compare(a, b string) int {
	ca, pa := split(a)
	cb, pb := split(b)
	for i := range 3 {
		if ca[i] != cb[i] {
			return cmpInt(ca[i], cb[i])
		}
	}
	switch {
	case pa == "" && pb == "":
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	ia, ib := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		na, ea := strconv.Atoi(ia[i])
		nb, eb := strconv.Atoi(ib[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return cmpInt(na, nb)
			}
		case ea == nil:
			return -1 // numbers sort before words
		case eb == nil:
			return 1
		case ia[i] != ib[i]:
			return strings.Compare(ia[i], ib[i])
		}
	}
	return cmpInt(len(ia), len(ib))
}

func cmpInt(a, b int) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	}
	return 0
}

// split turns "v0.1.0-beta.5+build" into [0 1 0] and "beta.5".
func split(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	pre := ""
	if i := strings.Index(v, "-"); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out, pre
}
