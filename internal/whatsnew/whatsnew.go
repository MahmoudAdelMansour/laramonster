// Package whatsnew is the changelog the dashboard shows once after an
// update: what changed in each version and where to find it in the app.
//
// Add a Release at the top of Releases for every version that changes
// something people would notice. Each item says where the feature lives
// (Where), and optionally a page of the dashboard to open (Page, a hash
// route like "settings"), an action the dashboard knows (Action), and a
// documentation page (Docs, a slug on monstersgate.com/laramonster/docs).
package whatsnew

import (
	"strings"

	"laramonster/internal/update"
)

type Item struct {
	Kind   string   `json:"kind"` // new, improved, fixed
	Icon   string   `json:"icon"` // an icon of the dashboard's sprite (#i-…)
	Title  string   `json:"title"`
	Text   string   `json:"text"`
	Where  []string `json:"where,omitempty"`  // the path to it, e.g. Settings › About
	Page   string   `json:"page,omitempty"`   // dashboard page to open
	Action string   `json:"action,omitempty"` // or a dashboard action: "ports", "trust"
	Docs   string   `json:"docs,omitempty"`   // docs page, with an optional #section
	Pro    bool     `json:"pro,omitempty"`
}

type Release struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Items   []Item `json:"items"`
}

// Releases, newest first.
var Releases = []Release{
	{
		Version: "0.2.3",
		Date:    "2026-10-19",
		Title:   "Updates now update everything",
		Summary: "If LaraMonster also runs at login or from a terminal, an update now restarts that part too, so the whole app is on the new version.",
		Items: []Item{
			{Kind: "fixed", Icon: "refresh", Title: "The engine updates with the app",
				Text:  "When LaraMonster started at login (or with laramonster start), an update left the background engine on the old version: the dashboard kept saying an update was available while Update now said you were up to date. The app now replaces an older engine when it opens.",
				Where: []string{"Settings", "About", "Version"}, Page: "settings", Docs: "installation#updates"},
		},
	},
	{
		Version: "0.2.2",
		Date:    "2026-10-19",
		Title:   "What's new, right in the app",
		Summary: "After every update LaraMonster now tells you what changed and takes you straight to it.",
		Items: []Item{
			{Kind: "new", Icon: "sparkles", Title: "This window",
				Text:  "It opens once after each update, with what changed and where to find it. Click Show me on any item to go there.",
				Where: []string{"Settings", "About", "What's new"}, Page: "settings"},
			{Kind: "improved", Icon: "book", Title: "Documentation, redesigned",
				Text:  "Every page is easier to scan, search works from the keyboard (Ctrl K, arrows, Enter), and code blocks copy in one click.",
				Where: []string{"Sidebar", "Documentation"}, Docs: "introduction"},
		},
	},
	{
		Version: "0.2.1",
		Date:    "2026-10-19",
		Title:   "A setup that can't leave you stuck",
		Summary: "Busy ports are explained and fixed for you, trusted HTTPS is part of setup, and the documentation is one click away.",
		Items: []Item{
			{Kind: "new", Icon: "server", Title: "Busy ports, explained and fixed",
				Text:  "When another program holds port 8080 or 8443, LaraMonster keeps running, names the program, and moves to free ports in one click, or shows the commands to free the port on macOS, Linux and Windows.",
				Where: []string{"Settings", "Run setup again", "Ports"}, Action: "ports", Docs: "troubleshooting#port-in-use"},
			{Kind: "improved", Icon: "shield", Title: "Trusted HTTPS is part of setup",
				Text:  "Setup now makes sure your browser trusts LaraMonster's certificates, so every https:// site shows a valid padlock. If the password prompt fails, it shows the commands to do it by hand.",
				Where: []string{"Settings", "HTTPS certificates"}, Page: "settings", Docs: "https"},
			{Kind: "new", Icon: "book", Title: "Documentation in the app",
				Text:  "Guides with examples for every feature, from installation to load tests, opened from the sidebar.",
				Where: []string{"Sidebar", "Documentation"}, Docs: "introduction"},
			{Kind: "improved", Icon: "code", Title: "laramonster status shows the ports",
				Text:  "The command line now lists the HTTP and HTTPS ports, and warns when another program holds them.",
				Where: []string{"Terminal", "laramonster status"}, Docs: "cli#engine"},
		},
	},
	{
		Version: "0.2.0",
		Date:    "2026-10-16",
		Title:   "Debugger, public links and webhooks",
		Summary: "See what every request did, share a site with the world, and get webhooks right.",
		Items: []Item{
			{Kind: "new", Icon: "search", Title: "Debugger",
				Text:  "Each request, job and command of a Laravel site with its dump() calls, queries, logs and exceptions. Xdebug turns on per site.",
				Where: []string{"Sidebar", "Debugger"}, Page: "debug", Docs: "debugger"},
			{Kind: "new", Icon: "share", Title: "Public links",
				Text:  "Share a site at a public https:// address, optionally with a password and for a set time.",
				Where: []string{"Sites", "a site", "Share"}, Page: "sites", Docs: "public-links"},
			{Kind: "new", Icon: "webhook", Title: "Webhooks", Pro: true,
				Text:  "Finds your webhook routes, records every delivery with your app's answer, checks signatures and replays deliveries.",
				Where: []string{"Sidebar", "Webhooks"}, Page: "webhooks", Docs: "webhooks"},
			{Kind: "new", Icon: "gauge", Title: "Load tests as a signed-in user", Pro: true,
				Text:  "Run a load test as a guest or as any user: by ID, email, role or permission.",
				Where: []string{"Sidebar", "Performance"}, Page: "performance", Docs: "performance#signed-in"},
			{Kind: "new", Icon: "box", Title: "PHP extensions",
				Text:  "Every PHP version's extensions in one list: turn them on and off, and add Xdebug, Redis, APCu and more.",
				Where: []string{"PHP", "Extensions"}, Page: "php", Docs: "php-settings#extensions"},
		},
	},
}

// Since lists the releases a user who last saw version seen hasn't seen yet,
// up to current, newest first. With nothing seen yet (an install from before
// this window existed), it's the two newest releases.
func Since(current, seen string) []Release {
	current, seen = clean(current), clean(seen)
	var out []Release
	for _, r := range Releases {
		if update.Newer(r.Version, current) {
			continue // released after this build
		}
		if seen != "" && !update.Newer(r.Version, seen) {
			continue
		}
		out = append(out, r)
	}
	if seen == "" && len(out) > 2 {
		out = out[:2]
	}
	return out
}

// Upto is every release up to current, newest first: the full changelog.
func Upto(current string) []Release {
	current = clean(current)
	var out []Release
	for _, r := range Releases {
		if !update.Newer(r.Version, current) {
			out = append(out, r)
		}
	}
	return out
}

func clean(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
