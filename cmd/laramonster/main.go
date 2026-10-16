// Command laramonster is a local PHP development environment, in the spirit
// of Laravel Herd, for Linux, macOS and Windows.
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"laramonster/internal/core"
)

func main() {
	root := &cobra.Command{
		Use:           "laramonster",
		Short:         "LaraMonster — local PHP & Laravel development environment",
		Version:       core.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddGroup(
		&cobra.Group{ID: "engine", Title: "Engine:"},
		&cobra.Group{ID: "sites", Title: "Sites:"},
		&cobra.Group{ID: "php", Title: "PHP:"},
		&cobra.Group{ID: "services", Title: "Services:"},
		&cobra.Group{ID: "system", Title: "System:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("engine", startCmd(), stopCmd(), restartCmd(), statusCmd(), reloadCmd(), dashboardCmd(), logsCmd())
	add("sites", parkCmd(), forgetCmd(), pathsCmd(), linkCmd(), unlinkCmd(), sitesCmd(), openCmd(), editCmd(),
		secureCmd(true), secureCmd(false), isolateCmd(), unisolateCmd(), newCmd(), shareCmd(), unshareCmd())
	add("php", phpCmd(), composerCmd(), phpListCmd(), phpAvailableCmd(), phpInstallCmd(), phpUninstallCmd(), useCmd(), whichCmd())
	add("services", servicesCmd(), serviceCmd(), toolsCmd())
	add("system", setupCmd(), dnsCmd(), trustCmd(), untrustCmd(), autostartCmd(), tldCmd(), portCmd())

	if err := root.Execute(); err != nil {
		// Pass through the exit code of php/composer.
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, red("error: ")+err.Error())
		os.Exit(1)
	}
}

// --- tiny terminal styling ---

func tty() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""
}

func style(code, s string) string {
	if !tty() {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func blue(s string) string   { return style("38;2;124;124;255", s) }
func green(s string) string  { return style("32", s) }
func red(s string) string    { return style("31", s) }
func yellow(s string) string { return style("33", s) }
func dim(s string) string    { return style("2", s) }
func bold(s string) string   { return style("1", s) }

func ok(format string, a ...any) { fmt.Println(green("✓ ") + fmt.Sprintf(format, a...)) }
