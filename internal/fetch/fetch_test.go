package fetch

import "testing"

func TestSafeJoinRejectsTraversal(t *testing.T) {
	if _, err := safeJoin("/tmp/x", "../etc/passwd"); err == nil {
		t.Fatal("traversal allowed")
	}
	if p, err := safeJoin("/tmp/x", "bin/php"); err != nil || p != "/tmp/x/bin/php" {
		t.Fatalf("%q %v", p, err)
	}
}
