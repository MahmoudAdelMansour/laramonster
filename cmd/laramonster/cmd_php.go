package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/php"
)

func phpCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "php [args...]",
		Short:              "Run PHP — the version of the site you are in",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			dir, _ := os.Getwd()
			cmd, err := core.PHPCommand(dir, args...)
			if err != nil {
				return err
			}
			return runAttached(cmd)
		},
	}
}

func composerCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "composer [args...]",
		Short:              "Run Composer with the site's PHP version",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			dir, _ := os.Getwd()
			cmd, err := core.ComposerCommand(dir, args...)
			if err != nil {
				return err
			}
			return runAttached(cmd)
		},
	}
}

func phpListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "php:list",
		Short: "List installed PHP versions",
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			installs := php.Discover()
			if len(installs) == 0 {
				fmt.Println("No PHP found. Install one with " + bold("laramonster php:install 8.4"))
				return nil
			}
			def := php.Default(c.PHP, installs)
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "\tVERSION\tSOURCE\tFPM")
			for _, i := range installs {
				mark, src := "", "system"
				if i.Version == def {
					mark = green("*")
				}
				if i.Managed {
					src = "laramonster"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", mark, i.Full, src, i.FPM)
			}
			return w.Flush()
		},
	}
}

func phpAvailableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "php:available",
		Short: "List PHP versions you can install",
		RunE: func(*cobra.Command, []string) error {
			list, err := php.Available()
			if err != nil {
				return err
			}
			for _, r := range list {
				state := dim("available")
				if r.Installed {
					state = green("installed")
				}
				fmt.Printf("  %-6s %-8s %s\n", r.Version, r.Full, state)
			}
			return nil
		},
	}
}

func phpInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "php:install <version...>",
		Short: "Download and install PHP versions, e.g. `php:install 8.4 8.3`",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			for _, v := range args {
				fmt.Printf("Installing PHP %s…\n", php.Normalize(v))
				rel, err := php.InstallVersion(v, progressBar())
				fmt.Print("\r\033[K")
				if err != nil {
					return err
				}
				ok("PHP %s installed in %s", rel.Full, php.ManagedDir(rel.Version))
			}
			return notify()
		},
	}
}

func phpUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "php:uninstall <version>",
		Short: "Remove a PHP version installed by LaraMonster",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := php.Uninstall(args[0]); err != nil {
				return err
			}
			ok("PHP %s uninstalled", php.Normalize(args[0]))
			return notify()
		},
	}
}

func useCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <version>",
		Short: "Set the default PHP version, e.g. `use php@8.4`",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			inst, err := core.SetDefaultPHP(args[0])
			if err != nil {
				return err
			}
			ok("default PHP is now %s", inst.Full)
			return notify()
		},
	}
}

func whichCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "which",
		Short: "Show which site and PHP the current directory uses",
		RunE: func(*cobra.Command, []string) error {
			dir, _ := os.Getwd()
			inst, err := core.PHPFor(dir)
			if err != nil {
				return err
			}
			if s, err := pickSite(nil); err == nil {
				fmt.Printf("site  %s  %s\n", bold(s.Name), blue(s.URL))
			} else {
				fmt.Println("site  " + dim("(not inside a site)"))
			}
			fmt.Printf("php   %s  %s\n", bold(inst.Full), dim(inst.CLI))
			return nil
		},
	}
}

// progressBar prints a single-line download indicator.
func progressBar() func(done, total int64) {
	return func(done, total int64) {
		mb := float64(done) / 1024 / 1024
		if total > 0 {
			pct := float64(done) / float64(total)
			n := int(pct * 30)
			fmt.Printf("\r  %s%s %3.0f%% %.1f MB", blue(strings.Repeat("█", n)), dim(strings.Repeat("░", 30-n)), pct*100, mb)
			return
		}
		fmt.Printf("\r  downloading… %.1f MB", mb)
	}
}
