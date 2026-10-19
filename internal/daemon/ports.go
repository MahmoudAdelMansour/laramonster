package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"laramonster/internal/config"
	"laramonster/internal/services"
)

// PortsBusyError is a Reload that couldn't start the web server because
// another program listens on its ports. The engine keeps running so the
// dashboard can explain it and switch ports.
type PortsBusyError struct{ Ports []int }

func (e *PortsBusyError) Error() string {
	list := make([]string, len(e.Ports))
	for i, p := range e.Ports {
		list[i] = strconv.Itoa(p)
	}
	word := "port " + list[0] + " is"
	if len(list) > 1 {
		word = "ports " + strings.Join(list, " and ") + " are"
	}
	return word + " already used by another program (another web server, Docker or a dev server?); stop it, or pick other ports in Settings or with `laramonster port`"
}

// PortCheck is one of the web server's ports, as the setup screen shows it.
type PortCheck struct {
	Use   string `json:"use"`  // "HTTP" or "HTTPS"
	Port  int    `json:"port"` // the configured port
	Busy  bool   `json:"busy"` // another program listens on it
	Owner string `json:"owner,omitempty"`
	PID   int    `json:"pid,omitempty"`
	Free  int    `json:"free,omitempty"` // a nearby free port to use instead
}

// portChecks checks the configured HTTP and HTTPS ports. A port the
// embedded web server already listens on is LaraMonster's own, not busy.
func (r *Runtime) portChecks(cfg *config.Config) []PortCheck {
	r.mu.Lock()
	ours := slices.Clone(r.ports)
	r.mu.Unlock()
	out := []PortCheck{{Use: "HTTP", Port: cfg.HTTPPort}, {Use: "HTTPS", Port: cfg.HTTPSPort}}
	taken := []int{cfg.HTTPPort, cfg.HTTPSPort}
	for i := range out {
		c := &out[i]
		if slices.Contains(ours, c.Port) || !portBusy(c.Port) {
			continue
		}
		c.Busy = true
		c.Owner, c.PID = portOwner(c.Port)
		for p := c.Port; ; {
			free, err := services.FreePort(p)
			if err != nil {
				break
			}
			if !slices.Contains(taken, free) {
				c.Free = free
				taken = append(taken, free)
				break
			}
			p = free
		}
	}
	return out
}

// busyPorts is the configured ports another program listens on; ours are
// the ports the embedded web server listens on.
func busyPorts(cfg *config.Config, ours []int) []int {
	var busy []int
	for _, port := range []int{cfg.HTTPPort, cfg.HTTPSPort} {
		if !slices.Contains(ours, port) && portBusy(port) {
			busy = append(busy, port)
		}
	}
	return busy
}

// portOwner names the program listening on port, best effort and without
// asking for a password: the OS may hide other users' processes.
func portOwner(port int) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "windows":
		return windowsPortOwner(ctx, port)
	case "linux":
		if name, pid := ssPortOwner(ctx, port); pid != 0 {
			return name, pid
		}
	}
	return lsofPortOwner(ctx, port)
}

func output(ctx context.Context, name string, args ...string) []byte {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	b, _ := cmd.Output()
	return b
}

var ssUser = regexp.MustCompile(`\("([^"]+)",pid=(\d+)`)

// ssPortOwner reads `ss -ltnp`: users:(("nginx",pid=812,fd=6)).
func ssPortOwner(ctx context.Context, port int) (string, int) {
	m := ssUser.FindSubmatch(output(ctx, "ss", "-Hltnp", fmt.Sprintf("sport = :%d", port)))
	if m == nil {
		return "", 0
	}
	pid, _ := strconv.Atoi(string(m[2]))
	return string(m[1]), pid
}

// lsofPortOwner reads `lsof -F pc`: a "p<pid>" line, then "c<command>".
func lsofPortOwner(ctx context.Context, port int) (string, int) {
	var name string
	var pid int
	sc := bufio.NewScanner(bytes.NewReader(output(ctx, "lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-Fpc")))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "p") && pid == 0:
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "c") && name == "":
			name = line[1:]
		}
	}
	return name, pid
}

// windowsPortOwner finds the PID in `netstat -ano` and its name in tasklist.
func windowsPortOwner(ctx context.Context, port int) (string, int) {
	suffix := ":" + strconv.Itoa(port)
	var pid int
	for _, line := range strings.Split(string(output(ctx, "netstat", "-ano")), "\n") {
		f := strings.Fields(line)
		// A listening socket has no remote end ("0.0.0.0:0"); the state
		// column is translated on non-English Windows.
		if len(f) == 5 && f[0] == "TCP" && strings.HasSuffix(f[1], suffix) && strings.HasSuffix(f[2], ":0") {
			pid, _ = strconv.Atoi(f[4])
			break
		}
	}
	if pid == 0 {
		return "", 0
	}
	rows, _ := csv.NewReader(bytes.NewReader(output(ctx, "tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH"))).ReadAll()
	if len(rows) > 0 && len(rows[0]) > 1 && rows[0][1] == strconv.Itoa(pid) {
		return rows[0][0], pid
	}
	return "", pid
}
