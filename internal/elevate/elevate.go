// Package elevate runs a shell script with administrator rights: sudo in a
// terminal, or the OS password dialog when started from the desktop app.
package elevate

import (
	"errors"
	"fmt"
	"io"
	"laramonster/internal/proc"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"
)

// interactive reports whether stdin is a real terminal (not /dev/null, which
// is also a character device) so sudo can ask for a password.
func interactive() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// Run executes script as root/administrator and waits for it, attached to
// this process's terminal.
func Run(script string) error { return RunTo(script, nil) }

// RunTo is Run with the script's output sent to out (nil = this terminal).
func RunTo(script string, out io.Writer) error {
	if script == "" {
		return nil
	}
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows":
		// -PassThru and exit: without them a failed script still looks done.
		ps := "$p = Start-Process powershell -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList '-NoProfile','-Command'," + psQuote(script) + "; exit $p.ExitCode"
		cmd = proc.Command("powershell", "-NoProfile", "-Command", ps)
	case os.Geteuid() == 0:
		cmd = exec.Command("sh", "-c", script)
	case interactive():
		cmd = exec.Command("sudo", "sh", "-c", script)
	case runtime.GOOS == "darwin":
		as := `do shell script "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(script) + `" with administrator privileges`
		cmd = exec.Command("osascript", "-e", as)
	default:
		if _, err := exec.LookPath("pkexec"); err != nil {
			return errors.New("administrator rights needed: run this from a terminal (sudo) or install polkit (pkexec)")
		}
		cmd = exec.Command("pkexec", "sh", "-c", script)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}
	// osascript exits with 1 both when the dialog is cancelled and when the
	// script fails: keep its message to tell them apart.
	// The same for PowerShell: the UAC prompt's No is an error there too.
	var osaErr strings.Builder
	switch {
	case runtime.GOOS == "windows" && out == nil:
		cmd.Stderr = &osaErr // the desktop app has no stderr to copy to
	case cmd.Args[0] == "osascript" || runtime.GOOS == "windows":
		cmd.Stderr = io.MultiWriter(cmd.Stderr, &osaErr)
	}
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch {
		case cmd.Args[0] == "pkexec" && (ee.ExitCode() == 126 || ee.ExitCode() == 127):
			return errors.New("the password prompt was cancelled")
		case cmd.Args[0] == "osascript":
			return osascriptError(osaErr.String(), err)
		case runtime.GOOS == "windows" && strings.Contains(strings.ToLower(osaErr.String()), "canceled by the user"):
			return errors.New("the administrator prompt was cancelled: click again and choose Yes")
		case runtime.GOOS == "windows":
			return fmt.Errorf("the administrator step failed (exit code %d)", ee.ExitCode())
		}
	}
	return err
}

// osascriptError explains a failed "do shell script … with administrator
// privileges": -128 is the dialog's Cancel button, anything else is the
// script's own error.
func osascriptError(stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	if strings.Contains(msg, "(-128)") || strings.Contains(strings.ToLower(msg), "user canceled") {
		return errors.New("the password prompt was cancelled")
	}
	// "execution error: <the script's output> (1)"
	msg = strings.TrimPrefix(msg, "0:")
	if i := strings.Index(msg, "execution error: "); i >= 0 {
		msg = msg[i+len("execution error: "):]
	}
	msg = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(msg), "(1)"))
	if msg == "" {
		return err
	}
	return errors.New(msg)
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
