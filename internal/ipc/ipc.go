// Package ipc is how the CLI, the desktop app and the dashboard proxy reach
// the engine's API.
//
// Linux and macOS: a unix socket in the run directory, protected by file
// permissions. Windows: a loopback TCP port plus a random token, both in the
// run directory. AF_UNIX sockets fail inside MSIX (Microsoft Store) packages
// ("connect: An invalid argument was supplied"), and a loopback port is
// reachable by any local process, so the engine requires the token.
package ipc

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"os"
)

// Header carries the token on Windows.
const Header = "X-LaraMonster-Token"

// Dial connects to the running engine.
func Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	network, addr, err := endpoint()
	if err != nil {
		return nil, err
	}
	return d.DialContext(ctx, network, addr)
}

// Authorize adds the token (Windows) to a request for the engine.
func Authorize(r *http.Request) {
	if t := Token(); t != "" {
		r.Header.Set(Header, t)
	}
}

// Guard rejects requests without the engine's token. A no-op where the
// endpoint is a unix socket (no token).
func Guard(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(Header)), []byte(token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
