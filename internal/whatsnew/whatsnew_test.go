package whatsnew

import (
	"slices"
	"testing"

	"laramonster/internal/update"
)

func versions(rs []Release) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Version)
	}
	return out
}

func TestSince(t *testing.T) {
	for _, c := range []struct {
		current, seen string
		want          []string
	}{
		{"v0.2.2", "0.2.0", []string{"0.2.2", "0.2.1"}},
		{"0.2.2", "0.2.2", nil},
		{"0.2.1", "0.2.0", []string{"0.2.1"}},     // a newer entry isn't shown before its release
		{"0.2.2", "", []string{"0.2.2", "0.2.1"}}, // nothing seen yet: the two newest
		{"0.1.0-dev", "", nil},                    // development builds predate every release
		{"0.3.0-beta.1", "0.2.1", []string{"0.2.2"}},
	} {
		if got := versions(Since(c.current, c.seen)); !slices.Equal(got, c.want) {
			t.Errorf("Since(%q, %q) = %v, want %v", c.current, c.seen, got, c.want)
		}
	}
}

func TestReleasesAreNewestFirstAndComplete(t *testing.T) {
	for i, r := range Releases {
		if i > 0 && !update.Newer(Releases[i-1].Version, r.Version) {
			t.Errorf("%s is listed before %s", Releases[i-1].Version, r.Version)
		}
		if r.Title == "" || r.Date == "" || len(r.Items) == 0 {
			t.Errorf("%s needs a title, a date and items", r.Version)
		}
		for _, it := range r.Items {
			if it.Title == "" || it.Text == "" || it.Icon == "" || len(it.Where) == 0 {
				t.Errorf("%s: %q needs a title, text, icon and where to find it", r.Version, it.Title)
			}
			if !slices.Contains([]string{"new", "improved", "fixed"}, it.Kind) {
				t.Errorf("%s: %q has kind %q", r.Version, it.Title, it.Kind)
			}
		}
	}
}
