// Package trust installs LaraMonster's local certificate authority (created by
// the embedded Caddy) into the system and browser trust stores.
package trust

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/smallstep/truststore"

	"laramonster/internal/paths"
)

func RootCert() string {
	return filepath.Join(paths.Data(), "pki", "authorities", "local", "root.crt")
}

func load() (*x509.Certificate, error) {
	b, err := os.ReadFile(RootCert())
	if err != nil {
		return nil, errors.New("the local CA doesn't exist yet: start LaraMonster first")
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("invalid root certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// Exists reports whether the CA has been generated.
func Exists() bool { _, err := load(); return err == nil }

// Trusted reports whether the OS trusts the CA right now. It must not use
// x509.SystemCertPool: Go caches that for the life of the process, so a CA
// installed while LaraMonster runs would never show as trusted.
func Trusted() bool {
	cert, err := load()
	if err != nil {
		return false
	}
	if runtime.GOOS != "linux" {
		// macOS and Windows verify against the live platform store.
		_, err = cert.Verify(x509.VerifyOptions{})
		return err == nil
	}
	for _, bundle := range linuxBundles {
		b, err := os.ReadFile(bundle)
		if err != nil {
			continue
		}
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(b) {
			if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err == nil {
				return true
			}
		}
	}
	return false
}

// Same files Go's crypto/x509 reads on Linux.
var linuxBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// firefoxProfiles lists the Firefox profiles that have their own NSS store.
func firefoxProfiles() []string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		p, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles", "*"))
		return p
	}
	p, _ := filepath.Glob(filepath.Join(home, ".mozilla", "firefox", "*.default*"))
	return p
}

// NeedsBrowserStores reports whether browsers keep their own certificate
// stores here: always on Linux (Chrome and Firefox), on macOS only when
// Firefox is installed (Safari and Chrome use the system keychain), never on
// Windows.
func NeedsBrowserStores() bool {
	switch runtime.GOOS {
	case "linux":
		return true
	case "darwin":
		return len(firefoxProfiles()) > 0
	}
	return false
}

// BrowserStatus reports trust in the NSS stores Firefox and (on Linux)
// Chrome use. "system" means the browsers here use the OS store;
// "missing-certutil" means they can't be checked or updated.
func BrowserStatus() string {
	if !NeedsBrowserStores() {
		return "system"
	}
	cert, err := load()
	if err != nil {
		return "unknown"
	}
	certutil, err := exec.LookPath("certutil")
	if err != nil {
		return "missing-certutil"
	}
	var dbs []string
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "linux" {
		dbs = append(dbs, filepath.Join(home, ".pki", "nssdb"))
	}
	dbs = append(dbs, firefoxProfiles()...)
	fp := fmt.Sprintf("%X", sha256.Sum256(cert.Raw))
	found := 0
	for _, db := range dbs {
		if _, err := os.Stat(filepath.Join(db, "cert9.db")); err != nil {
			continue
		}
		if nssHas(certutil, db, fp) {
			found++
		} else {
			return "partial"
		}
	}
	if found == 0 {
		return "none"
	}
	return "trusted"
}

// nssHas reports whether an NSS database holds a certificate with the given
// SHA-256 fingerprint under any LaraMonster nickname (truststore names them
// differently depending on how they were installed).
func nssHas(certutil, db, fp string) bool {
	list, err := exec.Command(certutil, "-L", "-d", "sql:"+db).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(list), "\n") {
		if !strings.Contains(strings.ToLower(line), "laramonster") {
			continue
		}
		// Lines are "<nickname>   <trust flags>"; the flags are the last field.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), fields[len(fields)-1]))
		out, _ := exec.Command(certutil, "-L", "-d", "sql:"+db, "-n", name, "-a").Output()
		for rest := out; ; {
			var blk *pem.Block
			blk, rest = pem.Decode(rest)
			if blk == nil {
				break
			}
			if fmt.Sprintf("%X", sha256.Sum256(blk.Bytes)) == fp {
				return true
			}
		}
	}
	return false
}

// InstallSystem adds the CA to the OS store. Needs administrator rights
// (truststore uses sudo itself when not root).
func InstallSystem() error {
	cert, err := load()
	if err != nil {
		return err
	}
	return truststore.Install(cert, truststore.WithPrefix("laramonster-"))
}

// InstallBrowsers adds the CA to the NSS stores used by Firefox and, on
// Linux, Chrome. Runs as the current user; needs certutil.
func InstallBrowsers() error {
	cert, err := load()
	if err != nil {
		return err
	}
	return truststore.Install(cert, truststore.WithNoSystem(), truststore.WithFirefox())
}

func Uninstall() error {
	cert, err := load()
	if err != nil {
		return err
	}
	_ = truststore.Uninstall(cert, truststore.WithNoSystem(), truststore.WithFirefox())
	return truststore.Uninstall(cert, truststore.WithPrefix("laramonster-"))
}
