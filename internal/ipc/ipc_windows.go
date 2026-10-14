//go:build windows

package ipc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	"laramonster/internal/paths"
)

func addrFile() string  { return filepath.Join(paths.Run(), "laramonster.addr") }
func tokenFile() string { return filepath.Join(paths.Run(), "laramonster.token") }

// Listen opens a loopback port and writes its address and a fresh token to
// the run directory (inside the user's profile, so only this user reads them).
func Listen() (ln net.Listener, token string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return nil, "", err
	}
	token = hex.EncodeToString(b)
	if err = os.WriteFile(tokenFile(), []byte(token), 0o600); err != nil {
		return nil, "", err
	}
	if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		return nil, "", err
	}
	if err = os.WriteFile(addrFile(), []byte(ln.Addr().String()), 0o600); err != nil {
		ln.Close()
		return nil, "", err
	}
	return ln, token, nil
}

// Remove deletes the endpoint files once the engine stops.
func Remove() {
	_ = os.Remove(addrFile())
	_ = os.Remove(tokenFile())
}

// Token is the running engine's token ("" when it isn't running).
func Token() string { return strings.TrimSpace(readFile(tokenFile())) }

// Upstream is the engine endpoint in Caddy's reverse_proxy syntax.
func Upstream() string { return strings.TrimSpace(readFile(addrFile())) }

func endpoint() (string, string, error) {
	addr := strings.TrimSpace(readFile(addrFile()))
	if addr == "" {
		return "", "", errors.New("LaraMonster is not running")
	}
	return "tcp", addr, nil
}
