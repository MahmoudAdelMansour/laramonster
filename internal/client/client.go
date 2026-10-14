// Package client talks to a running laramonster daemon over its unix socket.
package client

import (
	"os"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"laramonster/internal/paths"
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", paths.Socket())
		},
	},
}

// Call sends a request to the daemon API (path without the /api prefix)
// and returns the response body. body, if not nil, is sent as JSON.
func Call(method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://laramonster/api"+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-LaraMonster", "cli")
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return data, errors.New(e.Error)
		}
		return data, fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	return data, nil
}

func Running() bool {
	conn, err := net.DialTimeout("unix", paths.Socket(), time.Second)
	if err != nil {
		if os.Getenv("LARAMONSTER_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "debug: dial %s: %v\n", paths.Socket(), err)
		}
		return false
	}
	conn.Close()
	return true
}

// Reload asks a running daemon to re-read its config. It returns false when
// no daemon is running.
func Reload() (bool, error) {
	if !Running() {
		return false, nil
	}
	_, err := Call("POST", "/reload", nil)
	return true, err
}

// Ready reports whether the daemon is running and has finished starting.
func Ready() bool {
	if !Running() {
		return false
	}
	_, err := Call("GET", "/health", nil)
	return err == nil
}
