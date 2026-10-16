package laravel

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"laramonster/internal/paths"
)

// The Debugger (free) records dumps, queries, logs and exceptions of each
// request, job or command of a site (agent-debug.php), one JSON line each.

// DebugDir holds one NDJSON file per debugged project.
func DebugDir() string { return filepath.Join(paths.Root(), "debug") }

// DebugFile is where the agent writes a project's debugger entries.
func DebugFile(projectDir string) string {
	sum := sha1.Sum([]byte(filepath.Clean(projectDir)))
	return filepath.Join(DebugDir(), hex.EncodeToString(sum[:8])+".ndjson")
}

// DebugSite is one project the debugger records.
type DebugSite struct {
	Dir  string
	Echo bool // dumps also show in the page, as without LaraMonster
}

// WriteDebug tells the agent which projects to debug. An empty list turns
// the debugger off everywhere.
func WriteDebug(list []DebugSite) error {
	if err := Install(); err != nil {
		return err
	}
	if err := os.MkdirAll(DebugDir(), 0o755); err != nil {
		return err
	}
	type entry struct {
		Out  string `json:"out"`
		Echo bool   `json:"echo"`
	}
	m := map[string]entry{}
	for _, d := range list {
		m[filepath.Clean(d.Dir)] = entry{Out: DebugFile(d.Dir), Echo: d.Echo}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := filepath.Join(Dir(), "debug.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(Dir(), "debug.json"))
}

// DebugEntry is one recorded request, job or command. The dumps, queries
// and logs stay raw: only the UI reads them.
type DebugEntry struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"` // request, job, command
	Title      string          `json:"title"`
	StartedAt  string          `json:"started_at"`
	Method     string          `json:"method,omitempty"`
	URI        string          `json:"uri,omitempty"`
	Route      string          `json:"route,omitempty"`
	Action     string          `json:"action,omitempty"`
	Ajax       bool            `json:"ajax,omitempty"`
	User       string          `json:"user,omitempty"`
	Status     any             `json:"status,omitempty"`
	Duration   float64         `json:"duration"`
	Memory     int64           `json:"memory"`
	PHP        string          `json:"php,omitempty"`
	QueryCount int             `json:"query_count"`
	QueryMS    float64         `json:"query_ms"`
	Dumps      json.RawMessage `json:"dumps"`
	Queries    json.RawMessage `json:"queries"`
	Logs       json.RawMessage `json:"logs"`
	Exception  *DebugException `json:"exception,omitempty"`
}

type DebugException struct {
	Class    string          `json:"class"`
	Message  string          `json:"message"`
	File     string          `json:"file"`
	Line     int             `json:"line"`
	Frames   json.RawMessage `json:"frames,omitempty"`
	Previous json.RawMessage `json:"previous,omitempty"`
}

// DebugSummary is a row in the debugger's list.
type DebugSummary struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	StartedAt  string  `json:"started_at"`
	Method     string  `json:"method,omitempty"`
	Route      string  `json:"route,omitempty"`
	Ajax       bool    `json:"ajax,omitempty"`
	Status     any     `json:"status,omitempty"`
	Duration   float64 `json:"duration"`
	Memory     int64   `json:"memory"`
	QueryCount int     `json:"query_count"`
	QueryMS    float64 `json:"query_ms"`
	Dumps      int     `json:"dumps"`
	Logs       int     `json:"logs"`
	LogLevel   string  `json:"log_level,omitempty"` // the most severe one
	Exception  string  `json:"exception,omitempty"` // Class: message
	ExcAt      string  `json:"exc_at,omitempty"`    // file:line it was thrown at
	Failed     bool    `json:"failed"`
}

// DebugDump is one dump() call, with the entry it belongs to.
type DebugDump struct {
	Entry     string          `json:"entry"`
	Title     string          `json:"title"`
	StartedAt string          `json:"started_at"`
	Dump      json.RawMessage `json:"dump"`
}

// Dumps flattens the dumps of entries (newest first), at most limit.
func Dumps(entries []DebugEntry, limit int) []DebugDump {
	out := []DebugDump{}
	for _, e := range entries {
		var dumps []json.RawMessage
		_ = json.Unmarshal(e.Dumps, &dumps)
		for i := len(dumps) - 1; i >= 0; i-- {
			if len(out) >= limit {
				return out
			}
			out = append(out, DebugDump{Entry: e.ID, Title: e.Title, StartedAt: e.StartedAt, Dump: dumps[i]})
		}
	}
	return out
}

var logRank = map[string]int{"debug": 1, "info": 2, "notice": 3, "warning": 4, "error": 5, "critical": 6, "alert": 7, "emergency": 8}

func (e DebugEntry) Summary() DebugSummary {
	s := DebugSummary{ID: e.ID, Kind: e.Kind, Title: e.Title, StartedAt: e.StartedAt, Method: e.Method, Route: e.Route, Ajax: e.Ajax,
		Status: e.Status, Duration: e.Duration, Memory: e.Memory, QueryCount: e.QueryCount, QueryMS: e.QueryMS}
	var dumps, logs []struct {
		Level string `json:"level"`
	}
	_ = json.Unmarshal(e.Dumps, &dumps)
	_ = json.Unmarshal(e.Logs, &logs)
	s.Dumps, s.Logs = len(dumps), len(logs)
	for _, l := range logs {
		if logRank[l.Level] > logRank[s.LogLevel] {
			s.LogLevel = l.Level
		}
	}
	if e.Exception != nil {
		s.Exception = e.Exception.Class + ": " + e.Exception.Message
		s.ExcAt = fmt.Sprintf("%s:%d", e.Exception.File, e.Exception.Line)
		s.Failed = true
	}
	if code, ok := e.Status.(float64); ok && code >= 500 {
		s.Failed = true
	}
	if e.Status == "failed" {
		s.Failed = true
	}
	return s
}

// maxDebugFile is when a debugger file is cut back to its newest entries.
const maxDebugFile = 16 << 20

// ReadDebug returns the newest entries first, at most limit.
func ReadDebug(file string, limit int) ([]DebugEntry, error) {
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return []DebugEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxDebugFile {
		if _, err := f.Seek(fi.Size()-maxDebugFile/2, io.SeekStart); err == nil {
			_, _ = bufio.NewReader(f).ReadString('\n')
		}
	}
	var all []DebugEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e DebugEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.ID != "" {
			all = append(all, e)
		}
	}
	slices.Reverse(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// FindDebug returns one entry by id.
func FindDebug(file, id string) (DebugEntry, bool) {
	all, _ := ReadDebug(file, 0)
	for _, e := range all {
		if e.ID == id {
			return e, true
		}
	}
	return DebugEntry{}, false
}

// TrimDebug keeps a debugger file under its size limit (the newest 500).
func TrimDebug(file string) error {
	fi, err := os.Stat(file)
	if err != nil || fi.Size() <= maxDebugFile {
		return nil
	}
	all, err := ReadDebug(file, 500)
	if err != nil {
		return err
	}
	slices.Reverse(all)
	tmp := file + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	for _, e := range all {
		b, _ := json.Marshal(e)
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

// SourceLines returns the lines around line of a file inside the project,
// for an exception's code excerpt. Files outside the project are refused.
func SourceLines(projectDir, file string, line, around int) (start int, lines []string, err error) {
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(projectDir, file)
	}
	path = filepath.Clean(path)
	if rel, err := filepath.Rel(filepath.Clean(projectDir), path); err != nil || strings.HasPrefix(rel, "..") {
		return 0, nil, errors.New("only files inside the project can be shown")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	start = max(1, line-around)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan() && n <= line+around; n++ {
		if n >= start {
			l := sc.Text()
			if len(l) > 400 {
				l = l[:400] + "…"
			}
			lines = append(lines, l)
		}
	}
	return start, lines, nil
}

// LogEntry is one message from the project's storage/logs/laravel*.log.
type LogEntry struct {
	Time    string `json:"time"`
	Env     string `json:"env"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"` // context and stack trace
}

// LogFile is the newest Laravel log of a project ("" when there is none):
// laravel.log, or the newest laravel-YYYY-MM-DD.log of a daily channel.
func LogFile(projectDir string) string {
	dir := filepath.Join(projectDir, "storage", "logs")
	matches, _ := filepath.Glob(filepath.Join(dir, "laravel*.log"))
	best, bestAt := "", int64(0)
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().UnixNano() > bestAt {
			best, bestAt = m, fi.ModTime().UnixNano()
		}
	}
	return best
}

var logHead = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}[ T][\d:.]+[^\]]*)\] (\w+)\.(\w+): (.*)$`)

// ReadLog returns the newest entries of a Laravel log file first, at most
// limit, reading only its last 2 MB.
func ReadLog(file string, limit int) ([]LogEntry, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	if fi, err := f.Stat(); err == nil && fi.Size() > 2<<20 {
		if _, err := f.Seek(fi.Size()-2<<20, io.SeekStart); err == nil {
			r = bufio.NewReader(f)
			_, _ = r.ReadString('\n')
		}
	}
	var out []LogEntry
	var cur *LogEntry
	var detail strings.Builder
	end := func() {
		if cur != nil {
			d := strings.TrimSpace(detail.String())
			if len(d) > 20000 {
				d = d[:20000] + "\n…"
			}
			cur.Detail = d
			out = append(out, *cur)
		}
		detail.Reset()
	}
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if m := logHead.FindStringSubmatch(line); m != nil {
			end()
			msg := m[4]
			// The context JSON (and an exception's summary) follows the message.
			if i := strings.Index(msg, " {"); i > 0 {
				detail.WriteString(msg[i+1:] + "\n")
				msg = msg[:i]
			}
			if len(msg) > 2000 {
				msg = msg[:2000] + "…"
			}
			cur = &LogEntry{Time: m[1], Env: m[2], Level: strings.ToLower(m[3]), Message: msg}
		} else if cur != nil && line != "" {
			detail.WriteString(line + "\n")
		}
		if err != nil {
			break
		}
	}
	end()
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
