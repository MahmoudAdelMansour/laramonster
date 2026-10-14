//go:build !windows

package ipc

import (
	"net"
	"os"

	"laramonster/internal/paths"
)

// Listen opens the engine's API endpoint; token is "" on unix.
func Listen() (ln net.Listener, token string, err error) {
	_ = os.Remove(paths.Socket())
	ln, err = net.Listen("unix", paths.Socket())
	return ln, "", err
}

// Remove deletes the endpoint files once the engine stops.
func Remove() { _ = os.Remove(paths.Socket()) }

// Token is "" on unix: file permissions protect the socket.
func Token() string { return "" }

// Upstream is the engine endpoint in Caddy's reverse_proxy syntax.
func Upstream() string { return "unix/" + paths.Socket() }

func endpoint() (string, string, error) { return "unix", paths.Socket(), nil }
