package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"laramonster/internal/client"
	"laramonster/internal/share"
)

// shareCmd opens a public HTTPS link to a site (Cloudflare tunnel).
func shareCmd() *cobra.Command {
	var user, password string
	var minutes int
	c := &cobra.Command{
		Use:   "share [name]",
		Short: "Open a site to the internet with a public https:// link",
		Long: "Opens a public https:// link to a site through Cloudflare (free, no account), to show it to someone,\n" +
			"try it on your phone or receive webhooks. The link stays open until `laramonster unshare`,\n" +
			"--minutes runs out, or LaraMonster stops.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := siteArg(args)
			if err != nil {
				return err
			}
			if !client.Running() {
				return errors.New("LaraMonster isn't running: start it with `laramonster start -b`")
			}
			body := map[string]any{"site": name, "minutes": minutes}
			if user != "" || password != "" {
				body["protect"], body["user"], body["password"] = true, user, password
			}
			if _, err := client.Call("POST", "/shares/start", body); err != nil {
				return err
			}
			fmt.Println(dim("Opening a public link for " + name + "…"))
			for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
				data, err := client.Call("GET", "/shares", nil)
				if err != nil {
					return err
				}
				var res struct{ Links []share.Link }
				_ = json.Unmarshal(data, &res)
				for _, l := range res.Links {
					if l.Site != name {
						continue
					}
					switch l.Status {
					case "ready":
						ok("%s is public at %s", name, blue(l.URL))
						if l.User != "" {
							fmt.Println(dim("Visitors log in as " + l.User + "."))
						}
						fmt.Println(dim("Stop it with `laramonster unshare " + name + "`."))
						return nil
					case "error":
						return errors.New(l.Error)
					}
				}
			}
			return errors.New("Cloudflare didn't answer in time: check your internet connection and try again")
		},
	}
	c.Flags().StringVar(&user, "user", "", "ask visitors for this username (with --password)")
	c.Flags().StringVar(&password, "password", "", "the password visitors need")
	c.Flags().IntVar(&minutes, "minutes", 0, "close the link after this many minutes (0: until stopped)")
	return c
}

func unshareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unshare [name]",
		Short: "Close a site's public link",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := siteArg(args)
			if err != nil {
				return err
			}
			if _, err := client.Call("POST", "/shares/stop", map[string]string{"site": name}); err != nil {
				return err
			}
			ok("%s is no longer public", name)
			return nil
		},
	}
}
