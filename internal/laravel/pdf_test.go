package laravel

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestGuidesRenderAsPDF(t *testing.T) {
	b, _ := os.ReadFile("testdata/events.json")
	var res struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(b, &res); err != nil || len(res.Events) == 0 {
		t.Fatalf("events fixture: %v", err)
	}
	traces, err := ReadTraces("testdata/trace.ndjson", 0)
	if err != nil || len(traces) != 1 {
		t.Fatalf("trace fixture: %v %d", err, len(traces))
	}

	event, err := EventGuide("shop", res.Events[0], &traces[0], []Location{{File: "routes/web.php", Line: 11, Code: "OrderPlaced::dispatch($user, 89.5);"}})
	if err != nil || !bytes.HasPrefix(event, []byte("%PDF")) {
		t.Fatalf("event guide: %v", err)
	}

	jobs, schedule := loadJobs(t)
	j := find(jobs, "GenerateDailyReport")
	job, err := JobGuide("shop", j, AnalyzeJob(j, schedule, nil, "database", time.UTC), nil)
	if err != nil || !bytes.HasPrefix(job, []byte("%PDF")) {
		t.Fatalf("job guide: %v", err)
	}

	if dir := os.Getenv("LARAMONSTER_PDF_OUT"); dir != "" {
		_ = os.WriteFile(dir+"/event-guide.pdf", event, 0o644)
		_ = os.WriteFile(dir+"/job-guide.pdf", job, 0o644)
	}
}
