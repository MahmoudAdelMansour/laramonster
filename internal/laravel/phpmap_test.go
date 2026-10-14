package laravel

import (
	"encoding/json"
	"testing"
)

// PHP's json_encode writes an empty array as [], which broke Jobs & Queues
// for every project with a job that sets nothing ("cannot unmarshal array
// into Go struct field Job.jobs.settings").
func TestJobReadsPHPEmptyArraysAsEmptyMaps(t *testing.T) {
	var jobs []Job
	in := `[{"class":"App\\Jobs\\A","settings":[],"helpers":[]},{"class":"App\\Jobs\\B","settings":{"tries":3,"queue":"emails"},"helpers":{"send":"function send() {}"}},{"class":"App\\Jobs\\C","settings":null}]`
	if err := json.Unmarshal([]byte(in), &jobs); err != nil {
		t.Fatal(err)
	}
	if jobs[0].Settings == nil || len(jobs[0].Settings) != 0 || len(jobs[0].Helpers) != 0 {
		t.Fatalf("empty: %+v", jobs[0])
	}
	if jobs[1].Settings["tries"] != float64(3) || jobs[1].Settings["queue"] != "emails" || jobs[1].Helpers["send"] == "" {
		t.Fatalf("filled: %+v", jobs[1])
	}
	if jobs[2].Settings == nil {
		t.Fatal("null settings should be an empty map")
	}
	var bad Job
	if err := json.Unmarshal([]byte(`{"settings":[1,2]}`), &bad); err == nil {
		t.Fatal("a non-empty list is not a settings map")
	}
}
