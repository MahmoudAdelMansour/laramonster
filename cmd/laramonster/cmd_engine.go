package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"laramonster/internal/autostart"
	"laramonster/internal/client"
	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/daemon"
	"laramonster/internal/paths"
)

func startCmd() *cobra.Command {
	var background bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start LaraMonster (foreground, or in the background with -b)",
		RunE: func(*cobra.Command, []string) error {
			if background {
				if client.Running() {
					fmt.Println("LaraMonster is already running")
					return nil
				}
				return startBackground()
			}
			return daemon.Run(context.Background(), nil)
		},
	}
	cmd.Flags().BoolVarP(&background, "background", "b", false, "run in the background")
	return cmd
}

// startBackground detaches the engine and waits until it serves, failing
// fast with the engine's own error message if it exits.
func startBackground() error {
	outFile := filepath.Join(paths.Logs(), "laramonster.out")
	var offset int64
	if fi, err := os.Stat(outFile); err == nil {
		offset = fi.Size()
	}
	if _, err := autostart.Detach(); err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if client.Ready() {
			ok("LaraMonster is running")
			return nil
		}
		if msg := engineError(outFile, offset); msg != "" {
			return errors.New(msg)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("LaraMonster did not start in time, see %s", outFile)
}

// engineError returns the "error: …" line a failed engine printed.
func engineError(file string, offset int64) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	_, _ = f.Seek(offset, 0)
	b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	for _, line := range strings.Split(string(b), "\n") {
		if msg, found := strings.CutPrefix(stripANSI(line), "error: "); found {
			return msg
		}
	}
	return ""
}

func stripANSI(s string) string {
	for {
		i := strings.Index(s, "\033[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

func waitRunning(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client.Ready() {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("LaraMonster did not start in time")
}

func stopDaemon() error {
	if !client.Running() {
		return nil
	}
	if autostart.Managed(daemonPID()) {
		return autostart.StopManaged()
	}
	if _, err := client.Call("POST", "/stop", nil); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && client.Running() {
		time.Sleep(150 * time.Millisecond)
	}
	// The control socket closes before Caddy releases the web ports; wait
	// for those too so an immediate start doesn't find them busy.
	if c, err := config.Load(); err == nil {
		for time.Now().Before(deadline) && (portOpen(c.HTTPPort) || portOpen(c.HTTPSPort)) {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

// daemonPID is the PID of the running engine (0 if none).
func daemonPID() int {
	if !client.Running() {
		return 0
	}
	st, err := fetchStatus()
	if err != nil {
		return 0
	}
	return st.PID
}

func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop LaraMonster and every service it runs",
		RunE: func(*cobra.Command, []string) error {
			if !client.Running() {
				fmt.Println("LaraMonster is not running")
				return nil
			}
			if err := stopDaemon(); err != nil {
				return err
			}
			ok("LaraMonster stopped")
			return nil
		},
	}
}

func restartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart LaraMonster in the background",
		RunE: func(*cobra.Command, []string) error {
			managed := autostart.Managed(daemonPID())
			if err := stopDaemon(); err != nil {
				return err
			}
			if managed {
				if err := exec.Command("systemctl", "--user", "start", "laramonster.service").Run(); err != nil {
					return err
				}
				if err := waitRunning(20 * time.Second); err != nil {
					return err
				}
				ok("LaraMonster restarted")
				return nil
			}
			return startBackground()
		},
	}
}

func reloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Re-read the config and reload sites and PHP",
		RunE: func(*cobra.Command, []string) error {
			running, err := client.Reload()
			if !running {
				return errors.New("LaraMonster is not running, start it with `laramonster start -b`")
			}
			if err == nil {
				ok("reloaded")
			}
			return err
		},
	}
}

func fetchStatus() (*daemon.Status, error) {
	body, err := client.Call("GET", "/status", nil)
	if err != nil {
		return nil, err
	}
	var st daemon.Status
	return &st, json.Unmarshal(body, &st)
}

func statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what LaraMonster is running",
		RunE: func(*cobra.Command, []string) error {
			if !client.Running() {
				fmt.Println(dim("●") + " LaraMonster is not running. Start it with " + bold("laramonster start -b"))
				return nil
			}
			st, err := fetchStatus()
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(st, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("%s %s %s\n", green("●"), bold("LaraMonster "+st.Version), dim(fmt.Sprintf("(pid %d)", st.PID)))
			if st.DashboardURL != "" {
				fmt.Printf("  dashboard  %s\n", blue(st.DashboardURL))
			}
			ports := fmt.Sprintf("http %d, https %d", st.Config.HTTPPort, st.Config.HTTPSPort)
			if len(st.PortsBusy) > 0 {
				ports += " " + yellow(fmt.Sprintf("— %v used by another program: sites aren't served (see `laramonster port`)", st.PortsBusy))
			}
			fmt.Printf("  ports      %s\n", ports)
			fmt.Printf("  sites      %d on *.%s\n", len(st.Sites), st.Config.TLD)
			fmt.Printf("  php        default %s, serving %s\n", st.DefaultPHP, strings.Join(st.Pools, ", "))
			var running []string
			for _, s := range st.Services {
				if s.Running {
					running = append(running, fmt.Sprintf("%s:%d", s.Name, s.Port))
				}
			}
			if len(running) == 0 {
				running = []string{"none"}
			}
			fmt.Printf("  services   %s\n", strings.Join(running, ", "))
			fmt.Printf("  dns        %s %s\n", st.DNS.Method, yesNo(st.DNS.Working, "working", "needs `laramonster dns setup`"))
			fmt.Printf("  https      %s\n", yesNo(st.TLS.Trusted, "trusted", "not trusted — run `laramonster trust`"))
			for _, w := range st.Warnings {
				fmt.Println(yellow("  ! ") + w)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print raw JSON")
	return cmd
}

func yesNo(b bool, yes, no string) string {
	if b {
		return green(yes)
	}
	return yellow(no)
}

func dashboardCmd() *cobra.Command {
	var browser bool
	cmd := &cobra.Command{
		Use:       "dashboard [on|off]",
		Aliases:   []string{"ui"},
		Short:     "Open the LaraMonster app; on/off toggles the browser dashboard",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				on := args[0] == "on"
				if !on && args[0] != "off" {
					return errors.New("use `dashboard on` or `dashboard off`")
				}
				if err := core.UpdateSettings(core.Settings{BrowserDashboard: &on}); err != nil {
					return err
				}
				c, _ := config.Load()
				if on {
					ok("browser dashboard on: %s", blue(core.DashboardURL(c)))
				} else {
					ok("browser dashboard off")
				}
				return notify()
			}
			if !browser {
				if app := appPath(); app != nil {
					return detach(app)
				}
			}
			c, err := config.Load()
			if err != nil {
				return err
			}
			if !c.BrowserDashboard {
				if browser {
					return errors.New("the browser dashboard is off, turn it on with `laramonster dashboard on`")
				}
				return errors.New("the LaraMonster app is not installed; use `laramonster dashboard on` to manage it from your browser instead")
			}
			if !client.Running() {
				return errors.New("LaraMonster is not running, start it with `laramonster start -b`")
			}
			url := core.DashboardURL(c)
			fmt.Println(blue(url))
			return core.Open(url)
		},
	}
	cmd.Flags().BoolVar(&browser, "browser", false, "open the browser dashboard instead of the app")
	return cmd
}

// appPath returns a command that launches the desktop app, or nil if it isn't
// installed. A running app just comes to the front (single instance).
func appPath() *exec.Cmd {
	if runtime.GOOS == "darwin" {
		for _, dir := range []string{"/Applications", filepath.Join(os.Getenv("HOME"), "Applications")} {
			if _, err := os.Stat(filepath.Join(dir, "LaraMonster.app")); err == nil {
				return exec.Command("open", "-a", "LaraMonster")
			}
		}
	}
	names := []string{"laramonster-app", "LaraMonster.exe", "laramonster-app.exe"}
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		for _, n := range names {
			if p := filepath.Join(filepath.Dir(self), n); isFile(p) {
				return exec.Command(p)
			}
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return exec.Command(p)
		}
	}
	return nil
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func logsCmd() *cobra.Command {
	var lines int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [name]",
		Short: "Show a log (laramonster, caddy, php84-fpm, mysql…); no name lists them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				for _, n := range daemon.LogNames() {
					fmt.Println(n)
				}
				return nil
			}
			res, err := daemon.TailLog(args[0], lines)
			if err != nil {
				return err
			}
			for _, l := range res["lines"].([]string) {
				fmt.Println(l)
			}
			if !follow {
				return nil
			}
			return followFile(filepath.Join(paths.Logs(), args[0]+".log"))
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "number of lines")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	return cmd
}

func followFile(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	pos, _ := f.Seek(0, 2)
	buf := make([]byte, 32<<10)
	for {
		if fi, err := os.Stat(p); err == nil && fi.Size() < pos {
			pos, _ = f.Seek(0, 0) // rotated or truncated
		}
		n, _ := f.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			pos += int64(n)
			continue
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// notify tells the daemon about config changes.
func notify() error {
	running, err := client.Reload()
	if !running {
		fmt.Println(dim("(LaraMonster is not running — changes apply on `laramonster start -b`)"))
		return nil
	}
	return err
}
