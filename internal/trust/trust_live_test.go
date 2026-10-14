package trust

import (
	"os"
	"testing"
)

// Run with LARAMONSTER_LIVE_TRUST=1 to check the real install's CA.
func TestLiveTrust(t *testing.T) {
	if os.Getenv("LARAMONSTER_LIVE_TRUST") == "" {
		t.Skip("set LARAMONSTER_LIVE_TRUST=1")
	}
	t.Logf("exists=%v trusted=%v browsers=%s", Exists(), Trusted(), BrowserStatus())
}
