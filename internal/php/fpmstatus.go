package php

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// StatusPath is php-fpm's status page in LaraMonster's pools. It is only
// reachable over the pool socket: Caddy never routes it (it is not a file).
const StatusPath = "/laramonster-fpm-status"

// FPMStatus is php-fpm's "full" status: one entry per worker process.
type FPMStatus struct {
	Accepted  int          `json:"accepted conn"`
	Active    int          `json:"active processes"`
	Total     int          `json:"total processes"`
	MaxActive int          `json:"max active processes"`
	MaxHit    int          `json:"max children reached"`
	Processes []FPMProcess `json:"processes"`
}

type FPMProcess struct {
	PID      int     `json:"pid"`
	State    string  `json:"state"` // Idle, Running, Reading headers…
	Requests int     `json:"requests"`
	Duration int64   `json:"request duration"` // µs: current request when Running, else the last one
	Method   string  `json:"request method"`
	URI      string  `json:"request uri"`
	Script   string  `json:"script"`
	CPU      float64 `json:"last request cpu"`
	Memory   int64   `json:"last request memory"`
}

// ReadFPMStatus asks a php-fpm pool for its status over FastCGI.
func ReadFPMStatus(sock string) (*FPMStatus, error) {
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	const id = 1
	var req bytes.Buffer
	writeRecord(&req, 1, id, []byte{0, 1, 0, 0, 0, 0, 0, 0}) // BEGIN_REQUEST, responder, close
	var params bytes.Buffer
	for _, kv := range [][2]string{
		{"SCRIPT_NAME", StatusPath}, {"SCRIPT_FILENAME", StatusPath}, {"REQUEST_URI", StatusPath + "?json&full"},
		{"QUERY_STRING", "json&full"}, {"REQUEST_METHOD", "GET"}, {"SERVER_PROTOCOL", "HTTP/1.1"}, {"GATEWAY_INTERFACE", "CGI/1.1"},
	} {
		writePair(&params, kv[0], kv[1])
	}
	writeRecord(&req, 4, id, params.Bytes()) // PARAMS
	writeRecord(&req, 4, id, nil)
	writeRecord(&req, 5, id, nil) // empty STDIN
	if _, err := conn.Write(req.Bytes()); err != nil {
		return nil, err
	}

	var stdout bytes.Buffer
	r := bufio.NewReader(conn)
	for {
		var h [8]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, err
		}
		n, pad := int(binary.BigEndian.Uint16(h[4:6])), int(h[6])
		body := make([]byte, n+pad)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		switch h[1] {
		case 6: // STDOUT
			stdout.Write(body[:n])
		case 3: // END_REQUEST
			_, payload, ok := bytes.Cut(stdout.Bytes(), []byte("\r\n\r\n"))
			if !ok {
				return nil, errors.New("php-fpm status: malformed response")
			}
			var st FPMStatus
			if err := json.Unmarshal(payload, &st); err != nil {
				return nil, fmt.Errorf("php-fpm status: %w", err)
			}
			return &st, nil
		}
	}
}

func writeRecord(w *bytes.Buffer, typ byte, id uint16, content []byte) {
	pad := (8 - len(content)%8) % 8
	w.Write([]byte{1, typ, byte(id >> 8), byte(id), byte(len(content) >> 8), byte(len(content)), byte(pad), 0})
	w.Write(content)
	w.Write(make([]byte, pad))
}

func writePair(w *bytes.Buffer, k, v string) {
	for _, s := range []string{k, v} {
		if n := len(s); n < 128 {
			w.WriteByte(byte(n))
		} else {
			w.Write([]byte{byte(n>>24) | 0x80, byte(n >> 16), byte(n >> 8), byte(n)})
		}
	}
	w.WriteString(k)
	w.WriteString(v)
}

// SiteOfScript is a helper for callers mapping a worker's script to a site:
// it returns the longest root that prefixes script.
func SiteOfScript(script string, roots map[string]string) string {
	best, bestLen := "", 0
	for name, root := range roots {
		if strings.HasPrefix(script, root+"/") || strings.HasPrefix(script, root+`\`) || script == root {
			if len(root) > bestLen {
				best, bestLen = name, len(root)
			}
		}
	}
	return best
}
