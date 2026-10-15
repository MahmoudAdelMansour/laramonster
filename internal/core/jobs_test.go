package core

import (
	"sync"
	"testing"
)

func TestJobTransferAcrossSteps(t *testing.T) {
	j := &Job{mu: &sync.Mutex{}}
	j.Step(1, 2, "PHP command line")
	j.Bytes(5<<20, 10<<20)
	if j.Progress != 0.25 || j.Transfer.Step != 1 || j.Detail != "5.0 of 10.0 MB" {
		t.Fatalf("step 1 half done: progress %v, transfer %+v, detail %q", j.Progress, j.Transfer, j.Detail)
	}
	j.Step(2, 2, "PHP-FPM")
	j.Bytes(10<<20, 10<<20)
	if j.Progress != 1 || j.Transfer.Label != "PHP-FPM" {
		t.Fatalf("step 2 done: progress %v, transfer %+v", j.Progress, j.Transfer)
	}
	j.Set(-1, "Unpacking")
	if j.Transfer != nil {
		t.Error("a new phase keeps the old transfer")
	}

	// Single file, size unknown.
	j.Bytes(3<<20, -1)
	if j.Progress != -1 || j.Transfer.Total != -1 || j.Detail != "3.0 MB" {
		t.Errorf("unknown size: progress %v, transfer %+v, detail %q", j.Progress, j.Transfer, j.Detail)
	}
}
