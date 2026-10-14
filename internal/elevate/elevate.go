// Package elevate runs a shell script with administrator rights: sudo in a
// terminal, or the OS password dialog when started from the desktop app.
package elevate

import (
	"errors"
	"io"
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
		ps := "Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile','-Command'," + psQuote(script)
		cmd = exec.Command("powershell", "-NoProfile", "-Command", ps)
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
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch {
		case cmd.Args[0] == "pkexec" && (ee.ExitCode() == 126 || ee.ExitCode() == 127):
			return errors.New("the password prompt was cancelled")
		case cmd.Args[0] == "osascript" && ee.ExitCode() == 1:
			return errors.New("the password prompt was cancelled")
		}
	}
	return err
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
