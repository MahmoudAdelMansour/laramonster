// Package update asks monstersgate.com for the newest LaraMonster release
// and says whether it's newer than this build. The answer is cached for a
// few hours; development builds ("-dev") never report an update.
package update

import (
	"encoding/json"
	"net/http"
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
	info.Available = !strings.Contains(info.Current, "dev") && Newer(body.Version, info.Current)
	return info
}

// Newer reports whether version a is greater than b ("0.10.0" > "0.9.3").
// Anything after "-" (pre-release) or "+" is ignored.
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}
