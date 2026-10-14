package dns

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
)

// Method is how the OS learns that *.<tld> lives on this machine.
type Method string

const (
	MethodNone     Method = "none"     // *.localhost needs nothing
	MethodResolver Method = "resolver" // macOS /etc/resolver/<tld>
	MethodResolved Method = "resolved" // Linux systemd-resolved drop-in
	MethodHosts    Method = "hosts"    // one hosts-file line per site
)

type Status struct {
	Method  Method `json:"method"`
	Working bool   `json:"working"` // the system resolves a test name to loopback
	Synced  bool   `json:"synced"`  // hosts mode: every site is in the hosts file
	Hint    string `json:"hint,omitempty"`
}

func DetectMethod(tld string) Method {
	if tld == "localhost" {
		return MethodNone
	}
	switch runtime.GOOS {
	case "darwin":
		return MethodResolver
	case "linux":
		if exec.Command("systemctl", "is-active", "--quiet", "systemd-resolved").Run() == nil {
			return MethodResolved
		}
	}
	return MethodHosts
}

// Check reports whether names under tld resolve, given the site hosts.
func Check(tld string, hosts []string) Status {
	st := Status{Method: DetectMethod(tld)}
	switch st.Method {
	case MethodNone:
		st.Working, st.Synced = true, true
	case MethodHosts:
		st.Synced = hostsSynced(hosts)
		st.Working = st.Synced
		if !st.Synced {
			st.Hint = "the hosts file is missing some sites"
		}
	default:
		st.Working = resolvesToLoopback("laramonster-check." + tld)
		st.Synced = true
		if !st.Working {
			st.Hint = "*." + tld + " is not resolved yet"
		}
	}
	return st
}

func resolvesToLoopback(name string) bool {
	addrs, err := net.LookupHost(name)
	return err == nil && slices.ContainsFunc(addrs, func(a string) bool {
		ip := net.ParseIP(a)
		return ip != nil && ip.IsLoopback()
	})
}

// SetupScript returns a privileged shell script that configures the method.
// For hosts mode it rewrites the hosts file with the given site hosts.
func SetupScript(tld string, port int, hosts []string) (Method, string, error) {
	m := DetectMethod(tld)
	switch m {
	case MethodNone:
		return m, "", nil
	case MethodResolver:
		return m, fmt.Sprintf("mkdir -p /etc/resolver && printf 'nameserver 127.0.0.1\\nport %d\\n' > /etc/resolver/%s", port, tld), nil
	case MethodResolved:
		return m, fmt.Sprintf(`mkdir -p /etc/systemd/resolved.conf.d && printf '[Resolve]\nDNS=127.0.0.1:%d\nDomains=~%s\n' > /etc/systemd/resolved.conf.d/laramonster.conf && systemctl restart systemd-resolved`, port, tld), nil
	}
	tmp, err := writeHostsTemp(hosts)
	if err != nil {
		return m, "", err
	}
	if runtime.GOOS == "windows" {
		return m, fmt.Sprintf(`Copy-Item -Force '%s' '%s'`, tmp, HostsFile()), nil
	}
	return m, fmt.Sprintf("cp '%s' '%s'", tmp, HostsFile()), nil
}

// TeardownScript undoes SetupScript.
func TeardownScript(tld string) string {
	switch DetectMethod(tld) {
	case MethodResolver:
		return "rm -f /etc/resolver/" + tld
	case MethodResolved:
		return "rm -f /etc/systemd/resolved.conf.d/laramonster.conf && systemctl restart systemd-resolved"
	}
	return ""
}

func HostsFile() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("SystemRoot") + `\System32\drivers\etc\hosts`
	}
	return "/etc/hosts"
}

const (
	beginMark = "# BEGIN LaraMonster"
	endMark   = "# END LaraMonster"
)

// renderHosts returns hosts-file content with the LaraMonster block replaced.
func renderHosts(current []byte, hosts []string) []byte {
	var out bytes.Buffer
	inBlock := false
	for _, line := range strings.SplitAfter(string(current), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == beginMark:
			inBlock = true
		case trimmed == endMark:
			inBlock = false
		case !inBlock && line != "":
			out.WriteString(line)
		}
	}
	if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		out.WriteByte('\n')
	}
	if len(hosts) > 0 {
		out.WriteString(beginMark + "\n")
		for _, h := range hosts {
			fmt.Fprintf(&out, "127.0.0.1 %s\n::1 %s\n", h, h)
		}
		out.WriteString(endMark + "\n")
	}
	return out.Bytes()
}

func hostsSynced(hosts []string) bool {
	cur, err := os.ReadFile(HostsFile())
	if err != nil {
		return false
	}
	return bytes.Equal(cur, renderHosts(cur, hosts))
}

func writeHostsTemp(hosts []string) (string, error) {
	cur, err := os.ReadFile(HostsFile())
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "laramonster-hosts-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(renderHosts(cur, hosts)); err != nil {
		return "", err
	}
	_ = f.Chmod(0o644)
	return f.Name(), nil
}
