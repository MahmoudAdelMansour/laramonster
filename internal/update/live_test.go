package update

import (
	"os"
	"testing"

	"laramonster/internal/account"
)

// Run with LARAMONSTER_LIVE_UPDATE=1 to ask the real monstersgate.com.
func TestLiveUpdateCheck(t *testing.T) {
	if os.Getenv("LARAMONSTER_LIVE_UPDATE") == "" {
		t.Skip("set LARAMONSTER_LIVE_UPDATE=1")
	}
	i := fetch(account.BaseURL()+"/api/laramonster/latest", "v0.1.1-beta.0", "linux")
	t.Logf("%+v", i)
	if i.Error != "" || i.Latest == "" {
		t.Fatalf("live check failed: %+v", i)
	}
}
