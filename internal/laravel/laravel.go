// Package laravel holds LaraMonster Pro's Laravel tooling: the PHP agent
// that records events, the runner that inspects and drives a project, and
// the analysis built on top (what a job does, when it runs).
package laravel

import (
	"bytes"
	"context"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"laramonster/internal/paths"
	"laramonster/internal/php"
	"laramonster/internal/proc"
)

//go:embed php/*.php
var phpFiles embed.FS

const resultMark = "\n@@LARAMONSTER-RESULT@@\n"

// Dir is where the agent and runner live, next to tracking.json.
func Dir() string { return filepath.Join(paths.Root(), "agent") }

// AgentFile is the php-fpm auto_prepend_file.
func AgentFile() string { return filepath.Join(Dir(), "agent.php") }

// EventsDir holds one NDJSON file of traces per tracked project.
func EventsDir() string { return filepath.Join(paths.Root(), "events") }

// Install writes the agent and runner, replacing older copies.
func Install() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(EventsDir(), 0o755); err != nil {
		return err
	}
	entries, _ := phpFiles.ReadDir("php")
	for _, e := range entries {
		b, _ := phpFiles.ReadFile("php/" + e.Name())
		dst := filepath.Join(Dir(), e.Name())
		if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, b) {
			continue
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// TraceFile is where the agent writes a project's traces.
func TraceFile(projectDir string) string {
	sum := sha1.Sum([]byte(filepath.Clean(projectDir)))
	return filepath.Join(EventsDir(), hex.EncodeToString(sum[:8])+".ndjson")
}

// WriteTracking tells the agent which projects to record (project dir →
// trace file). An empty list turns recording off everywhere.
func WriteTracking(projectDirs []string) error {
	if err := Install(); err != nil {
		return err
	}
	m := map[string]string{}
	for _, d := range projectDirs {
		m[filepath.Clean(d)] = TraceFile(d)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := filepath.Join(Dir(), "tracking.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(Dir(), "tracking.json"))
}

// IsLaravel reports whether dir is a Laravel project the runner can boot.
func IsLaravel(dir string) bool {
	for _, f := range []string{"artisan", "bootstrap/app.php", "vendor/autoload.php"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
}

// RunError is a failure inside the project (an exception), as opposed to
// the runner not starting at all.
type RunError struct {
	Class   string   `json:"class"`
	Message string   `json:"message"`
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Trace   []string `json:"trace,omitempty"`
}

func (e *RunError) Error() string { return e.Class + ": " + e.Message }

// Run boots the project with its PHP and runs one runner command. The raw
// JSON result is returned as-is; ok=false results are returned too, so the
// caller can show the project's exception.
func Run(ctx context.Context, inst php.Install, projectDir, command string, input any, timeout time.Duration) (json.RawMessage, error) {
	if !IsLaravel(projectDir) {
		return nil, errors.New("this site isn't a Laravel project with its dependencies installed (run composer install)")
	}
	if inst.CLI == "" {
		return nil, fmt.Errorf("PHP %s has no command-line binary next to it", inst.Version)
	}
	if err := Install(); err != nil {
		return nil, err
	}
	in, err := os.CreateTemp("", "laramonster-run-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(in.Name())
	if err := json.NewEncoder(in).Encode(input); err != nil {
		in.Close()
		return nil, err
	}
	in.Close()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, inst.CLI,
		"-d", "auto_prepend_file="+AgentFile(), "-d", "memory_limit=1G", "-d", "display_errors=stderr",
		filepath.Join(Dir(), "runner.php"), projectDir, command, in.Name())
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), inst.Env()...)
	cmd.Env = append(cmd.Env, "LARAMONSTER_AGENT_CAPTURE="+projectDir)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("it took longer than %s and was stopped", timeout)
	}
	_, result, found := strings.Cut(out.String(), resultMark)
	if !found {
		msg := strings.TrimSpace(stderr.String() + "\n" + out.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		return nil, fmt.Errorf("the project couldn't start: %s", msg)
	}
	return json.RawMessage(strings.TrimSpace(result)), nil
}

// Artisan runs `php artisan <args>` in the project and returns its output.
func Artisan(ctx context.Context, inst php.Install, projectDir string, args []string, timeout time.Duration) (string, int, error) {
	if inst.CLI == "" {
		return "", -1, fmt.Errorf("PHP %s has no command-line binary next to it", inst.Version)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{"-d", "auto_prepend_file=" + AgentFile(), "artisan", "--no-interaction", "--no-ansi"}, args...)
	cmd := proc.CommandContext(ctx, inst.CLI, full...)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), inst.Env()...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
			err = nil
		} else if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("it took longer than %s and was stopped", timeout)
		}
	}
	return string(out), code, err
}
