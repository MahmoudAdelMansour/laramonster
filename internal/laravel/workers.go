package laravel

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"laramonster/internal/paths"
	"laramonster/internal/php"
	"laramonster/internal/proc"
)

// Worker is a `php artisan queue:work` LaraMonster runs for a site.
type Worker struct {
	ID         string    `json:"id"`
	Site       string    `json:"site"`
	Connection string    `json:"connection"`
	Queue      string    `json:"queue"`
	Tries      int       `json:"tries"`
	Started    time.Time `json:"started"`
	Running    bool      `json:"running"`
	ExitError  string    `json:"exit_error,omitempty"`
	Log        string    `json:"log"`

	cmd  *exec.Cmd
	done chan struct{}
}

// Workers runs queue workers; they stop with LaraMonster.
type Workers struct {
	mu   sync.Mutex
	list map[string]*Worker
}

func NewWorkers() *Workers { return &Workers{list: map[string]*Worker{}} }

func workerID(site, connection, queue string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(site + "-" + connection + "-" + queue)
}

// Start runs a worker for a site's queue(s); one per site+connection+queue.
func (w *Workers) Start(site string, inst php.Install, projectDir, connection, queue string, tries int) (*Worker, error) {
	if inst.CLI == "" {
		return nil, fmt.Errorf("PHP %s has no command-line binary next to it", inst.Version)
	}
	if queue == "" {
		queue = "default"
	}
	if tries <= 0 {
		tries = 1
	}
	id := workerID(site, connection, queue)
	w.mu.Lock()
	defer w.mu.Unlock()
	if old, ok := w.list[id]; ok && old.Running {
		return old, nil
	}
	if err := Install(); err != nil {
		return nil, err
	}
	logFile := filepath.Join(paths.Logs(), "worker-"+id+".log")
	out, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	args := []string{"-d", "auto_prepend_file=" + AgentFile(), "artisan", "queue:work"}
	if connection != "" {
		args = append(args, connection)
	}
	args = append(args, "--queue="+queue, fmt.Sprintf("--tries=%d", tries), "--sleep=1", "--no-interaction")
	cmd := proc.Command(inst.CLI, args...)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), inst.Env()...)
	cmd.Stdout, cmd.Stderr = out, out
	proc.Bind(cmd)
	if err := cmd.Start(); err != nil {
		out.Close()
		return nil, err
	}
	wk := &Worker{ID: id, Site: site, Connection: connection, Queue: queue, Tries: tries, Started: time.Now(), Running: true, Log: logFile, cmd: cmd, done: make(chan struct{})}
	w.list[id] = wk
	go func() {
		err := cmd.Wait()
		out.Close()
		w.mu.Lock()
		wk.Running = false
		if err != nil && !strings.Contains(err.Error(), "signal") {
			wk.ExitError = err.Error()
		}
		w.mu.Unlock()
		close(wk.done)
	}()
	return wk, nil
}

func (w *Workers) Stop(id string) {
	w.mu.Lock()
	wk, ok := w.list[id]
	w.mu.Unlock()
	if !ok || !wk.Running {
		return
	}
	proc.Terminate(wk.cmd.Process)
	select {
	case <-wk.done:
	case <-time.After(10 * time.Second):
		_ = wk.cmd.Process.Kill()
	}
}

func (w *Workers) StopAll() {
	w.mu.Lock()
	ids := make([]string, 0, len(w.list))
	for id := range w.list {
		ids = append(ids, id)
	}
	w.mu.Unlock()
	for _, id := range ids {
		w.Stop(id)
	}
}

// List returns a site's workers (all sites with site == "").
func (w *Workers) List(site string) []Worker {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []Worker{}
	for _, wk := range w.list {
		if site == "" || wk.Site == site {
			out = append(out, *wk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Tail returns the last n lines of a worker's log.
func (w *Workers) Tail(id string, n int) []string {
	w.mu.Lock()
	wk, ok := w.list[id]
	w.mu.Unlock()
	if !ok {
		return nil
	}
	f, err := os.Open(wk.Log)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines
}
