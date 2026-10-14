package dns

import (
	"strings"
	"testing"
)

func TestRenderHostsReplacesBlock(t *testing.T) {
	cur := []byte("127.0.0.1 localhost\n# BEGIN LaraMonster\n127.0.0.1 old.test\n# END LaraMonster\n10.0.0.1 nas\n")
	got := string(renderHosts(cur, []string{"blog.test"}))
	if strings.Contains(got, "old.test") {
		t.Fatalf("old entry kept:\n%s", got)
	}
	for _, want := range []string{"127.0.0.1 localhost\n", "10.0.0.1 nas\n", "127.0.0.1 blog.test\n", "::1 blog.test\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	// Rendering twice is stable, so hostsSynced works.
	if again := string(renderHosts([]byte(got), []string{"blog.test"})); again != got {
		t.Fatalf("not idempotent:\n%s\n---\n%s", got, again)
	}
	// No hosts removes the block entirely.
	if empty := string(renderHosts([]byte(got), nil)); strings.Contains(empty, "LaraMonster") {
		t.Fatalf("block not removed:\n%s", empty)
	}
}

func TestServerResolvesTLD(t *testing.T) {
	s := NewServer()
	if err := s.Apply("test", 15354); err != nil {
		t.Skip("can't bind test port:", err)
	}
	defer s.Stop()
	ip, err := Resolve(15354, "anything.deep.test")
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("got %q, %v", ip, err)
	}
	if _, err := Resolve(15354, "example.com"); err == nil {
		t.Fatal("resolved a name outside the tld")
	}
}
