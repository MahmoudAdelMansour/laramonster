package core

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Job is a long-running task (downloads, project creation) shown in the UI.
type Job struct {
	ID       int       `json:"id"`
	Title    string    `json:"title"`
	Progress float64   `json:"progress"` // 0..1, or -1 when unknown
	Detail   string    `json:"detail"`
	Done     bool      `json:"done"`
	Error    string    `json:"error,omitempty"`
	Log      []string  `json:"log"`       // the last 300 lines
	LogTotal int       `json:"log_total"` // lines ever written, for followers
	Result   any       `json:"result,omitempty"`
	Transfer *Transfer `json:"transfer,omitempty"` // set while something downloads
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	mu       *sync.Mutex
}

func (j *Job) Set(progress float64, detail string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Progress, j.Detail = progress, detail
	j.Transfer = nil // a new phase (unpacking, installing…)
}

// SetResult attaches structured output (shown by the UI when the job ends).
func (j *Job) SetResult(v any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Result = v
}

// Transfer is a download in progress, for a progress bar with sizes,
// speed and time left. Step/Steps count the files of a multi-file download.
type Transfer struct {
	Done  int64   `json:"done"`
	Total int64   `json:"total"` // 0 = unknown
	Rate  float64 `json:"rate"`  // bytes per second, smoothed
	Step  int     `json:"step,omitempty"`
	Steps int     `json:"steps,omitempty"`
	Label string  `json:"label,omitempty"` // what this step downloads
	at    time.Time
	from  int64
}

// Step starts file i of n (1-based) of a multi-file download.
func (j *Job) Step(i, n int, label string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Transfer = &Transfer{Step: i, Steps: n, Label: label}
}

// Bytes reports download progress.
func (j *Job) Bytes(done, total int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	t := j.Transfer
	if t == nil || done < t.Done { // a new file without Step
		t = &Transfer{}
		if j.Transfer != nil {
			t.Step, t.Steps, t.Label = j.Transfer.Step, j.Transfer.Steps, j.Transfer.Label
		}
		j.Transfer = t
	}
	now := time.Now()
	if t.at.IsZero() {
		t.at, t.from = now, done
	} else if dt := now.Sub(t.at).Seconds(); dt >= 0.5 {
		rate := float64(done-t.from) / dt
		if t.Rate == 0 {
			t.Rate = rate
		} else {
			t.Rate = 0.7*t.Rate + 0.3*rate
		}
		t.at, t.from = now, done
	}
	t.Done, t.Total = done, total
	frac := -1.0
	if total > 0 {
		frac = float64(done) / float64(total)
		if t.Steps > 1 {
			frac = (float64(t.Step-1) + frac) / float64(t.Steps)
		}
	}
	j.Progress = frac
	if total > 0 {
		j.Detail = fmt.Sprintf("%.1f of %.1f MB", mb(done), mb(total))
	} else {
		j.Detail = fmt.Sprintf("%.1f MB", mb(done))
	}
}

func mb(n int64) float64 { return float64(n) / 1024 / 1024 }

// Write lets a job collect command output.
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			j.Log = append(j.Log, line)
			j.LogTotal++
			if !strings.HasPrefix(line, "==> ") || j.Detail == "" {
				j.Detail = line
			}
		}
	}
	if len(j.Log) > 300 {
		j.Log = j.Log[len(j.Log)-300:]
	}
	return len(p), nil
}

type Jobs struct {
	mu   sync.Mutex
	next int
	list []*Job
}

// Run starts fn in the background and returns its job.
func (js *Jobs) Run(title string, fn func(*Job) error, after func()) *Job {
	js.mu.Lock()
	js.next++
	j := &Job{ID: js.next, Title: title, Progress: -1, Started: time.Now(), mu: &sync.Mutex{}}
	js.list = append(js.list, j)
	// Keep the last 20 jobs.
	if len(js.list) > 20 {
		js.list = js.list[len(js.list)-20:]
	}
	js.mu.Unlock()

	go func() {
		err := fn(j)
		j.mu.Lock()
		j.Done, j.Finished, j.Progress = true, time.Now(), 1
		if err != nil {
			j.Error = err.Error()
		}
		j.mu.Unlock()
		if after != nil {
			after()
		}
	}()
	return j
}

// Snapshot returns copies of all jobs, newest first.
func (js *Jobs) Snapshot() []Job {
	js.mu.Lock()
	defer js.mu.Unlock()
	out := make([]Job, 0, len(js.list))
	for _, j := range js.list {
		j.mu.Lock()
		c := *j
		c.Log = slices.Clone(j.Log)
		if j.Transfer != nil {
			t := *j.Transfer
			c.Transfer = &t
		}
		j.mu.Unlock()
		out = append(out, c)
	}
	slices.Reverse(out)
	return out
}

// Busy reports whether a job with the same title is still running.
func (js *Jobs) Busy(title string) bool {
	js.mu.Lock()
	defer js.mu.Unlock()
	for _, j := range js.list {
		j.mu.Lock()
		busy := j.Title == title && !j.Done
		j.mu.Unlock()
		if busy {
			return true
		}
	}
	return false
}
