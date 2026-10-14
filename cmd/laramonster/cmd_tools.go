package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"laramonster/internal/client"
	"laramonster/internal/tools"
)

func toolsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "List developer tools (Composer, Node.js, databases, Git…) and install them",
		RunE: func(*cobra.Command, []string) error {
			pm, _ := tools.DetectManager()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTOOL\tSTATUS\tINSTALL VIA")
			for _, t := range tools.States() {
				status := yellow("not installed")
				if t.Installed {
					status = green("installed") + dim(" "+t.Path)
				}
				via := t.Method
				if via == "page" {
					via = "laramonster php:install"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.ID, t.Name, status, via)
			}
			w.Flush()
			if pm.Name != "" {
				fmt.Println(dim("package manager: " + pm.Name))
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install <id...>",
		Short: "Install tools, e.g. `tools install node postgres git`",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			for _, id := range args {
				t, found := tools.Get(id)
				if !found {
					return fmt.Errorf("unknown tool %q (see `laramonster tools`)", id)
				}
				if t.Page != "" {
					return fmt.Errorf("install PHP with `laramonster php:install <version>`")
				}
				fmt.Printf("Installing %s…\n", t.Name)
				err := tools.Install(t, progressBar(), os.Stdout)
				fmt.Print("\r\033[K")
				if err != nil {
					return err
				}
				ok("%s installed", t.Name)
			}
			_, _ = client.Reload()
			return nil
		},
	})
	return cmd
}
