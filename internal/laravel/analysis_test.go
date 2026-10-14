package laravel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func loadJobs(t *testing.T) ([]Job, []ScheduleEntry) {
	t.Helper()
	b, err := os.ReadFile("testdata/jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Jobs     []Job           `json:"jobs"`
		Schedule []ScheduleEntry `json:"schedule"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	return res.Jobs, res.Schedule
}

func find(jobs []Job, name string) Job {
	for _, j := range jobs {
		if strings.HasSuffix(j.Class, name) {
			return j
		}
	}
	return Job{}
}

func texts(steps []Step) string {
	var s []string
	for _, x := range steps {
		s = append(s, x.Text)
	}
	return strings.Join(s, " | ")
}

func TestAnalyzeJobDescribesWhatItDoesAndWhen(t *testing.T) {
	jobs, schedule := loadJobs(t)

	report := AnalyzeJob(find(jobs, "GenerateDailyReport"), schedule, nil, "database", time.UTC)
	does := texts(report.Does)
	for _, want := range []string{"Creates rows in the reports table", "Creates files on the local disk", "Calls an external API: POST https://hooks.example.com/reports", "Deletes User records that match a condition"} {
		if !strings.Contains(does, want) {
			t.Errorf("GenerateDailyReport: missing %q in %q", want, does)
		}
	}
	if len(report.Schedule) != 1 || !strings.HasPrefix(report.Schedule[0], "Every day at 02:00") {
		t.Errorf("schedule: %v", report.Schedule)
	}
	queue := strings.Join(report.Queue, " | ")
	for _, want := range []string{"default queue of the database (the app's default) connection", "Doesn't retry", "Only one copy can wait"} {
		if !strings.Contains(queue, want) {
			t.Errorf("queue: missing %q in %q", want, queue)
		}
	}

	receipt := AnalyzeJob(find(jobs, "SendReceiptEmail"), schedule, FindReferences("testdata/app", `App\Jobs\SendReceiptEmail`, "app/Jobs/SendReceiptEmail.php"), "database", time.UTC)
	if receipt.Summary != "Emails the customer a receipt for their order." {
		t.Errorf("summary should come from the docblock: %q", receipt.Summary)
	}
	if d := texts(receipt.Does); !strings.Contains(d, "Sends an email") || !strings.Contains(d, "Writes to the log") {
		t.Errorf("receipt does: %q", d)
	}
	queue = strings.Join(receipt.Queue, " | ")
	for _, want := range []string{"emails queue", "Tries up to 3 times, waiting 1 minute between tries", "stopped after 2 minutes", "Models are fetched fresh"} {
		if !strings.Contains(queue, want) {
			t.Errorf("receipt queue: missing %q in %q", want, queue)
		}
	}
	if len(receipt.From) != 2 || receipt.From[0].File != "app/Http/Controllers/OrderController.php" || receipt.From[0].Line != 6 {
		t.Errorf("dispatch sites: %+v", receipt.From)
	}

	failing := AnalyzeJob(find(jobs, "FailingJob"), schedule, nil, "database", time.UTC)
	if !strings.Contains(texts(failing.Does), "Can fail with RuntimeException") || !strings.Contains(failing.Runs[0], "only runs when you dispatch it") {
		t.Errorf("failing job: %+v", failing)
	}
}

func TestCronHuman(t *testing.T) {
	for expr, want := range map[string]string{
		"* * * * *":   "Every minute",
		"*/5 * * * *": "Every 5 minutes",
		"0 * * * *":   "Every hour",
		"15 * * * *":  "Every hour at :15",
		"0 */6 * * *": "Every 6 hours",
		"30 2 * * *":  "Every day at 02:30",
		"0 9 * * 1-5": "Weekdays at 09:00",
		"0 9 * * 1":   "Every Monday at 09:00",
		"0 0 1 * *":   "Monthly on day 1 at 00:00",
		"5 4 * 2 3,5": "cron 5 4 * 2 3,5",
		"not a cron":  "cron not a cron",
	} {
		if got := CronHuman(expr); got != want {
			t.Errorf("%q: got %q, want %q", expr, got, want)
		}
	}
}

func TestStripCommentsKeepsStrings(t *testing.T) {
	src := "Http::post('https://x.test/a#b', ['a' => \"it's // fine\"]); // gone\n/* gone\ntoo */ #[Attr] # gone\nok();"
	got := stripComments(src)
	for _, keep := range []string{"https://x.test/a#b", `"it's // fine"`, "#[Attr]", "ok();"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q: %q", keep, got)
		}
	}
	if strings.Contains(got, "gone") {
		t.Errorf("comment kept: %q", got)
	}
}

func TestAnalyzeJobFollowsHelpersServicesAndScheduledCommands(t *testing.T) {
	job := Job{
		Class:        `App\Jobs\PruneTokens`,
		Queued:       true,
		HandleParams: []string{`App\Services\FeedStore $feeds`},
		HandleSource: "public function handle(FeedStore $feeds) { if ($this->batch()?->cancelled()) { return; } $feeds->pushToFeeds($ids); $this->expire($tx); }",
		Helpers:      map[string]string{"expire": "private function expire($tx) { Token::where('id', $tx)->delete(); $this->log(); }", "log": "private function log() { Log::info('x'); }"},
	}
	schedule := []ScheduleEntry{{Expression: "0 3 * * *", Command: "'/usr/bin/php' 'artisan' tokens:prune --days=7"}}
	from := FindReferences("testdata/app", job.Class, "app/Jobs/PruneTokens.php")
	a := AnalyzeJob(job, schedule, from, "redis", time.UTC)

	does := texts(a.Does)
	for _, want := range []string{"Stops early if its batch was cancelled", "Calls FeedStore->pushToFeeds()", "Deletes Token records that match a condition", "Writes to the log"} {
		if !strings.Contains(does, want) {
			t.Errorf("missing %q in %q", want, does)
		}
	}
	if len(a.Schedule) != 1 || a.Schedule[0] != "Via the tokens:prune command: Every day at 03:00" {
		t.Errorf("a job dispatched by a scheduled command should say so: %v (from %+v)", a.Schedule, from)
	}

	if refs := FindReferences("testdata/app", `App\Jobs\FanOutChunk`, "app/Jobs/FanOutChunk.php"); len(refs) != 1 || refs[0].File != "app/Jobs/FanOut.php" {
		t.Errorf("a job created inside a batch counts as dispatched: %+v", refs)
	}
}
