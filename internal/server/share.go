package server

import (
	"fmt"
	"strings"

	"laramonster/internal/sites"
)

// Share is a site opened to the internet through a tunnel: a local-only
// listener on Port that serves the site as if it were reached over HTTPS
// at its public address.
type Share struct {
	Site string
	Port int
	// User and Hash (bcrypt) ask visitors for a username and password.
	User string
	Hash string
	// Open paths skip the password (webhook URLs: providers can't log in).
	Open []string
}

// ShareCounter is the traffic counter name of a site's public link.
func ShareCounter(site string) string { return "share:" + site }

// ShareBlocks renders one Caddy server per share. Sites served by PHP get
// HTTPS=on, so Laravel builds https:// links with the public host name.
func ShareBlocks(list []sites.Site, upstreams func(version, site string) []string, shares []Share) string {
	var b strings.Builder
	for _, sh := range shares {
		s, ok := sites.Find(list, sh.Site)
		if !ok || !s.Enabled {
			continue
		}
		fmt.Fprintf(&b, "http://:%d {\n\tbind 127.0.0.1\n", sh.Port)
		fmt.Fprintf(&b, "\tlaramonster_count %s\n", quote(ShareCounter(s.Name)))
		fmt.Fprintf(&b, "\tlaramonster_hooks %s \"public\"\n", quote(s.Name))
		if sh.User != "" && sh.Hash != "" {
			matcher := ""
			if len(sh.Open) > 0 {
				b.WriteString("\t@locked not path")
				for _, p := range sh.Open {
					fmt.Fprintf(&b, " %s", quote(p))
				}
				b.WriteString("\n")
				matcher = " @locked"
			}
			fmt.Fprintf(&b, "\tbasic_auth%s {\n\t\t%s %s\n\t}\n", matcher, quote(sh.User), quote(sh.Hash))
		}
		if s.Proxy != "" {
			// Tell the app it is reached over HTTPS (the tunnel ends TLS).
			block := strings.Replace(proxyBlock(s.Proxy), " {\n", " {\n\t\theader_up X-Forwarded-Proto https\n", 1)
			b.WriteString(block)
			b.WriteString("}\n\n")
			continue
		}
		fmt.Fprintf(&b, "\troot * %s\n", quote(s.Root))
		b.WriteString("\tencode zstd gzip\n")
		if ups := upstreams(s.PHP, s.Name); s.PHP != "" && len(ups) > 0 {
			b.WriteString("\tphp_fastcgi")
			for _, u := range ups {
				fmt.Fprintf(&b, " %s", quote(u))
			}
			b.WriteString(" {\n\t\tenv HTTPS on\n\t\tenv SERVER_PORT 443\n\t}\n")
		}
		b.WriteString("\tfile_server\n}\n\n")
	}
	return b.String()
}
