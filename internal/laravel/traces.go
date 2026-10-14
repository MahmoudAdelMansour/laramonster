package laravel

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
)

// Trace is one request, command or queued job as the agent recorded it.
// Events stay raw JSON: the UI renders payloads, the engine never needs to.
type Trace struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"` // request, command, job, laramonster
	Title      string          `json:"title"`
	StartedAt  string          `json:"started_at"`
	Method     string          `json:"method,omitempty"`
	URI        string          `json:"uri,omitempty"`
	Route      string          `json:"route,omitempty"`
	Action     string          `json:"action,omitempty"`
	Status     any             `json:"status,omitempty"` // HTTP code, or "done"/"failed" for jobs
	Command    string          `json:"command,omitempty"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	Job        string          `json:"job,omitempty"`
	Queue      string          `json:"queue,omitempty"`
	Connection string          `json:"connection,omitempty"`
	Attempt    int             `json:"attempt,omitempty"`
	Duration   float64         `json:"duration"`
	Queries    int             `json:"queries"`
	Memory     int64           `json:"memory"`
	Truncated  bool            `json:"truncated,omitempty"`
	Exception  json.RawMessage `json:"exception,omitempty"`
	Events     []TraceEvent    `json:"events"`
}

type TraceEvent struct {
	Name      string          `json:"name"`
	At        float64         `json:"at"`
	Duration  *float64        `json:"duration"`
	Depth     int             `json:"depth"`
	Parent    *int            `json:"parent"`
	App       bool            `json:"app"`
	Payload   json.RawMessage `json:"payload"`
	Listeners []Listener      `json:"listeners"`
	Ran       []RanListener   `json:"ran"`
	Responses json.RawMessage `json:"responses,omitempty"`
	Exception json.RawMessage `json:"exception,omitempty"`
	Source    string          `json:"source,omitempty"`
}

type Listener struct {
	Name   string `json:"name"`
	Queued bool   `json:"queued"`
}

type RanListener struct {
	Name      string          `json:"name"`
	Queued    bool            `json:"queued"`
	Duration  float64         `json:"duration"`
	Result    json.RawMessage `json:"result,omitempty"`
	Exception json.RawMessage `json:"exception,omitempty"`
}

// TraceSummary is a row in the trace list.
type TraceSummary struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	StartedAt string   `json:"started_at"`
	Status    any      `json:"status,omitempty"`
	Duration  float64  `json:"duration"`
	AppEvents []string `json:"app_events"`
	Events    int      `json:"events"`
	Failed    bool     `json:"failed"`
}

func (t Trace) Summary() TraceSummary {
	s := TraceSummary{ID: t.ID, Kind: t.Kind, Title: t.Title, StartedAt: t.StartedAt, Status: t.Status, Duration: t.Duration, Events: len(t.Events), AppEvents: []string{}}
	for _, e := range t.Events {
		if e.App && !slices.Contains(s.AppEvents, e.Name) {
			s.AppEvents = append(s.AppEvents, e.Name)
		}
		if present(e.Exception) {
			s.Failed = true
		}
	}
	if present(t.Exception) || t.Status == "failed" {
		s.Failed = true
	}
	if code, ok := t.Status.(float64); ok && code >= 500 {
		s.Failed = true
	}
	return s
}

// maxTraceFile is when a trace file is cut back to its newest traces.
const maxTraceFile = 8 << 20

// ReadTraces returns the newest traces first, at most limit.
func ReadTraces(file string, limit int) ([]Trace, error) {
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return []Trace{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxTraceFile {
		// Only the tail matters; skip to the last few MB.
		if _, err := f.Seek(fi.Size()-maxTraceFile/2, io.SeekStart); err == nil {
			_, _ = bufio.NewReader(f).ReadString('\n')
		}
	}
	var all []Trace
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var t Trace
		if json.Unmarshal([]byte(line), &t) == nil && t.ID != "" {
			all = append(all, t)
		}
	}
	slices.Reverse(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// FindTrace returns one trace by id.
func FindTrace(file, id string) (Trace, bool) {
	traces, _ := ReadTraces(file, 0)
	for _, t := range traces {
		if t.ID == id {
			return t, true
		}
	}
	return Trace{}, false
}

// TrimTraces keeps a trace file under its size limit (the newest half).
func TrimTraces(file string) error {
	fi, err := os.Stat(file)
	if err != nil || fi.Size() <= maxTraceFile {
		return nil
	}
	traces, err := ReadTraces(file, 1000)
	if err != nil {
		return err
	}
	slices.Reverse(traces)
	tmp := file + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	for _, t := range traces {
		b, _ := json.Marshal(t)
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Rename(tmp, file)
}

// ClearTraces deletes a project's recorded traces.
func ClearTraces(file string) error {
	err := os.Remove(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
