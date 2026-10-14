package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"laramonster/internal/client"
	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/sites"
)

func parkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "park [dir]",
		Short: "Serve every folder inside a directory as <folder>.<tld>",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir, err := core.Park(argOr(args, "."))
			if err != nil {
				return err
			}
			ok("parked %s", dir)
			return notify()
		},
	}
}

func forgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget [dir]",
		Short: "Stop serving a parked directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir, err := core.Forget(argOr(args, "."))
			if err != nil {
				return err
			}
			ok("forgot %s", dir)
			return notify()
		},
	}
}

func pathsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "paths",
		Short: "List parked directories",
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			for _, p := range c.Paths {
				fmt.Println(p)
			}
			return nil
		},
	}
}

func linkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link [name]",
		Short: "Serve the current directory as <name>.<tld>",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := core.Link(argOr(args, ""), ".")
			if err != nil {
				return err
			}
			c, _ := config.Load()
			ok("linked %s", blue(sites.URL(c, name)))
			return notify()
		},
	}
}

func unlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink [name]",
		Short: "Remove a linked site (defaults to the current directory)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := siteArg(args)
			if err != nil {
				return err
			}
			if err := core.Unlink(name); err != nil {
				return err
			}
			ok("unlinked %s", name)
			return notify()
		},
	}
}

func sitesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "sites",
		Aliases: []string{"links"},
		Short:   "List all served sites",
		RunE: func(*cobra.Command, []string) error {
			c, list, err := core.Sites()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("No sites yet. Try " + bold("laramonster park") + " in your projects folder or " + bold("laramonster link") + " in a project.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SITE\tURL\tPHP\tPATH")
			for _, s := range list {
				ver := s.PHP
				if _, ok := c.Isolated[s.Name]; ok {
					ver += "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.URL, ver, s.Path)
			}
			w.Flush()
			fmt.Println(dim("* pinned with `laramonster isolate`"))
			return nil
		},
	}
}

func openCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open [name]",
		Short: "Open a site in the browser (defaults to the current directory)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			s, err := pickSite(args)
			if err != nil {
				return err
			}
			fmt.Println(blue(s.URL))
			return core.Open(s.URL)
		},
	}
}

func editCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit [name]",
		Short: "Open a site in your editor",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			s, err := pickSite(args)
			if err != nil {
				return err
			}
			return core.OpenEditor(s.Path)
		},
	}
}

func secureCmd(on bool) *cobra.Command {
	use, short := "secure [name]", "Serve a site over HTTPS with a local certificate"
	if !on {
		use, short = "unsecure [name]", "Serve a site over plain HTTP again"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := siteArg(args)
			if err != nil {
				return err
			}
			if err := core.Secure(name, on); err != nil {
				return err
			}
			c, _ := config.Load()
			ok("%s → %s", name, blue(sites.URL(c, name)))
			if on {
				fmt.Println(dim("Run `laramonster trust` once so browsers accept LaraMonster certificates."))
			}
			return notify()
		},
	}
}

func isolateCmd() *cobra.Command {
	var site string
	cmd := &cobra.Command{
		Use:   "isolate <version>",
		Short: "Pin the current site to a PHP version",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := siteArg(optional(site))
			if err != nil {
				return err
			}
			if err := core.Isolate(name, args[0]); err != nil {
				return err
			}
			ok("%s now uses PHP %s", name, args[0])
			return notify()
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "site name (defaults to the current directory)")
	return cmd
}

func unisolateCmd() *cobra.Command {
	var site string
	cmd := &cobra.Command{
		Use:   "unisolate",
		Short: "Make the current site use the default PHP version again",
		RunE: func(*cobra.Command, []string) error {
			name, err := siteArg(optional(site))
			if err != nil {
				return err
			}
			if err := core.Isolate(name, ""); err != nil {
				return err
			}
			ok("%s now uses the default PHP version", name)
			return notify()
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "site name (defaults to the current directory)")
	return cmd
}

func newCmd() *cobra.Command {
	var o core.ProjectOptions
	var noMail, noNPM, noGit bool
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a project: starter kit, domain, database, packages, all wired up",
		Example: `  laramonster new shop
  laramonster new shop --domain store --kit react --db mysql --php 8.4 --secure
  laramonster new api -p laravel/sanctum -p laravel/boost --redis`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := requireDaemon(); err != nil {
				return err
			}
			o.Name = args[0]
			o.Mail, o.NPM, o.Git = !noMail, !noNPM, !noGit
			if o.Dir == "" {
				c, err := config.Load()
				if err != nil {
					return err
				}
				// Outside a parked folder, create the project right here.
				cwd, _ := os.Getwd()
				parked := false
				for _, p := range c.Paths {
					parked = parked || p == cwd
				}
				if !parked {
					o.Dir = cwd
				}
			}
			body, err := client.Call("POST", "/projects/new", o)
			if err != nil {
				return err
			}
			var job core.Job
			if err := json.Unmarshal(body, &job); err != nil {
				return err
			}
			if err := followJob(job.ID); err != nil {
				return err
			}
			if _, list, err := core.Sites(); err == nil {
				if site, found := sites.Find(list, sites.Slug(firstNonEmpty(o.Domain, o.Name))); found {
					fmt.Println("  " + blue(site.URL))
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Domain, "domain", "", "site name, e.g. store → store.<tld> (default: the project name)")
	f.StringVarP(&o.Kit, "kit", "k", "laravel", "starter kit: laravel, react, vue, livewire, blank")
	f.StringVar(&o.PHP, "php", "", "PHP version to pin (default: the global default)")
	f.StringVar(&o.Database, "db", "sqlite", "database: sqlite, mysql, pgsql")
	f.StringArrayVarP(&o.Packages, "package", "p", nil, "composer package to add (repeatable)")
	f.BoolVar(&o.Secure, "secure", false, "serve over HTTPS")
	f.BoolVar(&o.Redis, "redis", false, "use Redis for cache, queues and sessions")
	f.BoolVar(&noMail, "no-mail", false, "don't send mail to the LaraMonster inbox")
	f.BoolVar(&noNPM, "no-npm", false, "skip npm install && npm run build")
	f.BoolVar(&noGit, "no-git", false, "skip git init")
	f.StringVar(&o.Dir, "dir", "", "where to create it (default: current directory, or the first parked folder)")
	f.BoolVarP(&verbose, "verbose", "v", false, "show all Composer/npm output")
	return cmd
}

// followJob prints a job's log until it finishes. Composer is chatty, so by
// default only stage headers, warnings and errors are shown (-v for all).
func followJob(id int) error {
	printed := 0 // lines already handled, counted against LogTotal
	for {
		st, err := fetchStatus()
		if err != nil {
			return err
		}
		var job *core.Job
		for i := range st.Jobs {
			if st.Jobs[i].ID == id {
				job = &st.Jobs[i]
			}
		}
		if job == nil {
			return errors.New("job disappeared")
		}
		fresh := job.LogTotal - printed
		if fresh > len(job.Log) {
			fresh = len(job.Log) // older lines were trimmed away
		}
		for _, l := range job.Log[len(job.Log)-fresh:] {
			switch {
			case strings.HasPrefix(l, "==> "):
				fmt.Println(blue(bold(l)))
			case strings.HasPrefix(l, "warning: "):
				fmt.Println(yellow(l))
			case verbose:
				fmt.Println(dim(l))
			}
		}
		printed = job.LogTotal
		if job.Done {
			if job.Error != "" {
				return errors.New(job.Error)
			}
			ok("%s", job.Title)
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
}

var verbose bool

// --- helpers ---

func argOr(args []string, def string) string {
	if len(args) > 0 {
		return args[0]
	}
	return def
}

func optional(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func pickSite(args []string) (sites.Site, error) {
	_, list, err := core.Sites()
	if err != nil {
		return sites.Site{}, err
	}
	if len(args) == 1 {
		if s, ok := sites.Find(list, sites.Slug(args[0])); ok {
			return s, nil
		}
		return sites.Site{}, fmt.Errorf("no site named %q", args[0])
	}
	dir, _ := os.Getwd()
	if s, ok := sites.ForPath(list, dir); ok {
		return s, nil
	}
	return sites.Site{}, errors.New("the current directory is not a LaraMonster site (pass a site name)")
}

func siteArg(args []string) (string, error) {
	s, err := pickSite(args)
	return s.Name, err
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
