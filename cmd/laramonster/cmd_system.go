package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"laramonster/internal/autostart"
	"laramonster/internal/client"
	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/dns"
	"laramonster/internal/elevate"
	"laramonster/internal/paths"
	"laramonster/internal/php"
	"laramonster/internal/services"
	"laramonster/internal/trust"
)

// --- services ---

func requireDaemon() error {
	if !client.Running() {
		return errors.New("LaraMonster is not running, start it with `laramonster start -b`")
	}
	return nil
}

func servicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "services",
		Short: "List services (databases, cache, mail, search)",
		RunE: func(*cobra.Command, []string) error {
			var states []services.State
			if client.Running() {
				st, err := fetchStatus()
				if err != nil {
					return err
				}
				states = st.Services
			} else {
				c, _ := config.Load()
				m := services.NewManager()
				for _, d := range services.Registry {
					sc := c.Service(d.Name, d.DefaultPort)
					states = append(states, m.State(d, sc.Port, sc.Enabled))
				}
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATUS\tPORT\tAUTOSTART\tBINARY")
			for _, s := range states {
				status := dim("stopped")
				switch {
				case s.Running:
					status = green("running")
				case !s.Installed && s.CanDownload:
					status = yellow("not installed (downloadable)")
				case !s.Installed:
					status = yellow("not installed")
				case s.Error != "":
					status = red("error")
				}
				auto := ""
				if s.Enabled {
					auto = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", s.Name, status, s.Port, auto, s.Binary)
			}
			return w.Flush()
		},
	}
}

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service <start|stop|restart|enable|disable|install|env|port> <name> [port]",
		Short: "Control a service, e.g. `service start mysql`",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(_ *cobra.Command, args []string) error {
			action, name := args[0], args[1]
			d, found := services.Get(name)
			if !found {
				var names []string
				for _, d := range services.Registry {
					names = append(names, d.Name)
				}
				return fmt.Errorf("unknown service %q (available: %v)", name, names)
			}
			if action == "env" {
				c, _ := config.Load()
				fmt.Println(d.Env(c.Service(d.Name, d.DefaultPort).Port))
				return nil
			}
			if action == "install" {
				fmt.Printf("Downloading %s…\n", d.Title)
				err := services.NewManager().Install(d, progressBar())
				fmt.Print("\r\033[K")
				if err != nil {
					return err
				}
				ok("%s installed", d.Title)
				return nil
			}
			if err := requireDaemon(); err != nil {
				return err
			}
			var body any
			switch action {
			case "enable", "disable":
				body = map[string]bool{"on": action == "enable"}
				action = "enable"
			case "port":
				if len(args) < 3 {
					return errors.New("usage: service port <name> <port>")
				}
				port, err := strconv.Atoi(args[2])
				if err != nil {
					return err
				}
				body = map[string]int{"port": port}
			}
			if _, err := client.Call("POST", "/services/"+d.Name+"/"+action, body); err != nil {
				return err
			}
			ok("%s: %s done", d.Title, args[0])
			if action == "start" {
				c, _ := config.Load()
				fmt.Println(dim(d.Env(c.Service(d.Name, d.DefaultPort).Port)))
			}
			return nil
		},
	}
	return cmd
}

// --- setup ---

func setupCmd() *cobra.Command {
	var noPath, noAutostart bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "One-time setup: php/composer shims on PATH, autostart, start engine",
		RunE: func(*cobra.Command, []string) error {
			fmt.Println(blue(bold("LaraMonster setup")))
			if err := paths.Ensure(); err != nil {
				return err
			}
			dir, err := core.InstallShims()
			if err != nil {
				return err
			}
			ok("php and composer shims written to %s", dir)
			if !noPath {
				changed, err := core.AddToPath()
				if err != nil {
					fmt.Println(yellow("! ") + err.Error())
				} else if len(changed) > 0 {
					ok("added to PATH in %v (open a new terminal)", changed)
				} else {
					ok("PATH already configured")
				}
			}
			if len(php.Discover()) == 0 {
				fmt.Println(yellow("! ") + "no PHP found yet: run " + bold("laramonster php:install 8.4"))
			}
			if !noAutostart {
				if client.Running() && !autostart.Managed(daemonPID()) {
					_ = stopDaemon()
				}
				if err := autostart.Enable(); err != nil {
					fmt.Println(yellow("! ") + "autostart: " + err.Error())
				} else {
					ok("LaraMonster starts at login")
				}
			}
			if !client.Running() {
				if _, err := autostart.Detach(); err != nil {
					return err
				}
			}
			if err := waitRunning(15 * time.Second); err != nil {
				return err
			}
			ok("engine running")
			fmt.Println(dim("Next: `laramonster park ~/Code`, `laramonster trust`, and for .test domains `laramonster tld test && laramonster dns setup`."))
			return nil
		},
	}
	cmd.Flags().BoolVar(&noPath, "no-path", false, "don't edit shell profiles")
	cmd.Flags().BoolVar(&noAutostart, "no-autostart", false, "don't start LaraMonster at login")
	return cmd
}

// --- dns ---

func dnsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Configure name resolution for *.<tld>",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether *.<tld> resolves",
		RunE: func(*cobra.Command, []string) error {
			c, list, err := core.Sites()
			if err != nil {
				return err
			}
			st := dns.Check(c.TLD, core.HostNames(c.TLD, list))
			fmt.Printf("tld     .%s\nmethod  %s\nstatus  %s\n", c.TLD, st.Method, yesNo(st.Working, "working", "not working"))
			if st.Hint != "" {
				fmt.Println("hint    " + st.Hint)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "setup",
		Short: "Point *.<tld> at LaraMonster (asks for your password)",
		RunE: func(*cobra.Command, []string) error {
			c, list, err := core.Sites()
			if err != nil {
				return err
			}
			method, script, err := dns.SetupScript(c.TLD, c.DNSPort, core.HostNames(c.TLD, list))
			if err != nil {
				return err
			}
			if method == dns.MethodNone {
				ok(".localhost needs no DNS setup")
				return nil
			}
			fmt.Printf("Configuring %s (%s)…\n", bold("*."+c.TLD), method)
			if err := elevate.Run(script); err != nil {
				return err
			}
			if method == dns.MethodHosts {
				ok("hosts file updated — re-run after adding sites")
			} else {
				ok("*.%s now resolves to this machine", c.TLD)
			}
			return notify()
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "Undo `dns setup`",
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			if dns.DetectMethod(c.TLD) == dns.MethodHosts {
				_, script, err := dns.SetupScript(c.TLD, c.DNSPort, nil)
				if err != nil {
					return err
				}
				return elevate.Run(script)
			}
			return elevate.Run(dns.TeardownScript(c.TLD))
		},
	})
	return cmd
}

// --- trust ---

func trustCmd() *cobra.Command {
	var systemOnly bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Trust LaraMonster's local certificate authority (HTTPS)",
		RunE: func(*cobra.Command, []string) error {
			if systemOnly {
				return trust.InstallSystem()
			}
			if !trust.Exists() {
				return errors.New("the local CA is created on first start: run `laramonster start -b` first")
			}
			fmt.Println("Adding the LaraMonster root certificate to your trust stores…")
			if err := trust.InstallSystem(); err != nil {
				return err
			}
			if err := trust.InstallBrowsers(); err != nil {
				fmt.Println(yellow("! ") + "browser store (Firefox/Chrome NSS): " + err.Error())
			}
			ok("trusted — restart your browser")
			return nil
		},
	}
	cmd.Flags().BoolVar(&systemOnly, "system-only", false, "only the OS store (used by the desktop app)")
	_ = cmd.Flags().MarkHidden("system-only")
	return cmd
}

func untrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untrust",
		Short: "Remove LaraMonster's certificate authority from your trust stores",
		RunE: func(*cobra.Command, []string) error {
			if err := trust.Uninstall(); err != nil {
				return err
			}
			ok("removed")
			return nil
		},
	}
}

// --- autostart ---

func autostartCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "autostart [on|off]",
		Short:     "Start LaraMonster when you log in",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(_ *cobra.Command, args []string) error {
			switch argOr(args, "") {
			case "":
				fmt.Println(yesNo(autostart.Enabled(), "autostart is on", "autostart is off"))
				return nil
			case "on":
				if client.Running() && !autostart.Managed(daemonPID()) {
					_ = stopDaemon()
				}
				if err := autostart.Enable(); err != nil {
					return err
				}
				ok("LaraMonster will start at login")
			case "off":
				if err := autostart.Disable(true); err != nil {
					return err
				}
				ok("autostart disabled")
			default:
				return errors.New("use `autostart on` or `autostart off`")
			}
			return nil
		},
	}
}

// --- settings ---

func tldCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tld [tld]",
		Short: "Show or set the top-level domain (localhost, test…)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				c, err := config.Load()
				if err == nil {
					fmt.Println(c.TLD)
				}
				return err
			}
			if err := core.UpdateSettings(core.Settings{TLD: &args[0]}); err != nil {
				return err
			}
			c, _ := config.Load()
			ok("sites are now served on *.%s", c.TLD)
			if dns.DetectMethod(c.TLD) != dns.MethodNone {
				fmt.Println(dim("Run `laramonster dns setup` so *." + c.TLD + " resolves."))
			}
			return notify()
		},
	}
}

func portCmd() *cobra.Command {
	var https bool
	cmd := &cobra.Command{
		Use:   "port [port]",
		Short: "Show or set the HTTP (or --https) port",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				fmt.Printf("http %d, https %d\n", c.HTTPPort, c.HTTPSPort)
				return nil
			}
			port, err := strconv.Atoi(args[0])
			if err != nil {
				return errors.New("invalid port")
			}
			s := core.Settings{HTTPPort: &port}
			if https {
				s = core.Settings{HTTPSPort: &port}
			}
			if err := core.UpdateSettings(s); err != nil {
				return err
			}
			ok("port set to %d", port)
			if port < 1024 && runtime.GOOS == "linux" {
				fmt.Println(dim("Ports below 1024 need: sudo setcap cap_net_bind_service=+ep $(readlink -f $(which laramonster))"))
			}
			return notify()
		},
	}
	cmd.Flags().BoolVar(&https, "https", false, "set the HTTPS port")
	return cmd
}
