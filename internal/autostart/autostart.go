// Package autostart starts LaraMonster in the background and registers it to
// launch at login (systemd user unit, launchd agent, or a Windows logon task).
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"laramonster/internal/paths"
)

const (
	unitName  = "laramonster.service"
	agentName = "com.monstersgate.laramonster"
	taskName  = "LaraMonster"
)

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Detach starts `laramonster start` as a background process.
func Detach() (int, error) {
	exe, err := executable()
	if err != nil {
		return 0, err
	}
	if err := paths.Ensure(); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(filepath.Join(paths.Logs(), "laramonster.out"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	cmd := exec.Command(exe, "start")
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func home() string { h, _ := os.UserHomeDir(); return h }

func unitPath() string  { return filepath.Join(home(), ".config", "systemd", "user", unitName) }
func agentPath() string { return filepath.Join(home(), "Library", "LaunchAgents", agentName+".plist") }

func xdgPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home(), ".config")
	}
	return filepath.Join(dir, "autostart", "laramonster.desktop")
}

// Enabled reports whether LaraMonster is registered to start at login.
func Enabled() bool {
	switch runtime.GOOS {
	case "linux":
		if _, err := os.Stat(xdgPath()); err == nil {
			return true
		}
		return exec.Command("systemctl", "--user", "is-enabled", "--quiet", unitName).Run() == nil
	case "darwin":
		_, err := os.Stat(agentPath())
		return err == nil
	case "windows":
		return exec.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
	}
	return false
}

// Enable registers this binary's engine (`laramonster start`) to start at
// login and starts it now.
func Enable() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	return EnableEngine(exe, true)
}

// EnableEngine registers `<cli> start` to run at login. With now=false it
// only takes effect at the next login, for when an engine already runs.
func EnableEngine(cli string, now bool) error {
	return register(cli, []string{"start"}, now)
}

// EnableApp starts the desktop app in the tray at login. Used when the CLI
// isn't installed; the app then runs the engine itself.
func EnableApp(app string) error {
	if runtime.GOOS != "linux" {
		return register(app, []string{"--background"}, false)
	}
	// A GUI app belongs in XDG autostart, where the session's display is set.
	entry := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=LaraMonster\nExec=\"%s\" --background\nIcon=laramonster\nX-GNOME-Autostart-enabled=true\nNoDisplay=true\n", app)
	if err := os.MkdirAll(filepath.Dir(xdgPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(xdgPath(), []byte(entry), 0o644)
}

func register(exe string, args []string, now bool) error {
	switch runtime.GOOS {
	case "linux":
		quoted := `"` + exe + `"`
		for _, a := range args {
			quoted += " " + a
		}
		unit := fmt.Sprintf(`[Unit]
Description=LaraMonster local PHP development environment
After=network.target

[Service]
ExecStart=%s
Environment=LARAMONSTER_HOME=%s
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, quoted, paths.Root())
		if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(unitPath(), []byte(unit), 0o644); err != nil {
			return err
		}
		if err := run("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if now {
			return run("systemctl", "--user", "enable", "--now", unitName)
		}
		return run("systemctl", "--user", "enable", unitName)
	case "darwin":
		argXML := "<string>" + xmlEscape(exe) + "</string>"
		for _, a := range args {
			argXML += "<string>" + xmlEscape(a) + "</string>"
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key><array>%s</array>
	<key>EnvironmentVariables</key><dict><key>LARAMONSTER_HOME</key><string>%s</string></dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, agentName, argXML, xmlEscape(paths.Root()),
			xmlEscape(filepath.Join(paths.Logs(), "laramonster.out")), xmlEscape(filepath.Join(paths.Logs(), "laramonster.out")))
		if err := os.MkdirAll(filepath.Dir(agentPath()), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(agentPath(), []byte(plist), 0o644); err != nil {
			return err
		}
		if now {
			return run("launchctl", "load", "-w", agentPath())
		}
		return nil // launchd picks up LaunchAgents at the next login
	case "windows":
		return run("schtasks", "/Create", "/F", "/TN", taskName, "/SC", "ONLOGON", "/RL", "LIMITED", "/TR", fmt.Sprintf(`"%s" %s`, exe, strings.Join(args, " ")))
	}
	return fmt.Errorf("autostart is not supported on %s", runtime.GOOS)
}

// Disable removes the login registration. With stop it also stops the
// managed engine; without, a running engine keeps running until logout.
func Disable(stop bool) error {
	switch runtime.GOOS {
	case "linux":
		_ = os.Remove(xdgPath())
		if _, err := os.Stat(unitPath()); err != nil {
			return nil
		}
		if !stop {
			return run("systemctl", "--user", "disable", unitName)
		}
		_ = run("systemctl", "--user", "disable", "--now", unitName)
		_ = os.Remove(unitPath())
		return run("systemctl", "--user", "daemon-reload")
	case "darwin":
		if stop {
			_ = run("launchctl", "unload", "-w", agentPath())
		}
		return os.Remove(agentPath())
	case "windows":
		return run("schtasks", "/Delete", "/F", "/TN", taskName)
	}
	return nil
}

// Managed reports whether the engine with the given PID is the one systemd
// supervises (so `stop` must go through systemd, or it would be restarted).
// Another LaraMonster (e.g. a different LARAMONSTER_HOME) is never touched.
func Managed(pid int) bool {
	if runtime.GOOS != "linux" || pid <= 0 {
		return false
	}
	out, err := exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", unitName).Output()
	return err == nil && strings.TrimSpace(string(out)) == strconv.Itoa(pid)
}

func StopManaged() error { return run("systemctl", "--user", "stop", unitName) }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
