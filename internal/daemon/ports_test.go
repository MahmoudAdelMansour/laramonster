package daemon

import (
	"net"
	"strings"
	"testing"

	"laramonster/internal/config"
)

func TestPortChecksReportsOtherPrograms(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port
	cfg := config.Default()
	cfg.HTTPPort, cfg.HTTPSPort = busy, busy+1

	r := NewRuntime()
	checks := r.portChecks(cfg)
	if !checks[0].Busy || checks[0].Use != "HTTP" {
		t.Fatalf("HTTP port %d should be busy: %+v", busy, checks[0])
	}
	if checks[0].Free == 0 || checks[0].Free == busy || checks[0].Free == cfg.HTTPSPort {
		t.Errorf("suggested port %d clashes", checks[0].Free)
	}
	if got := busyPorts(cfg, nil); len(got) == 0 || got[0] != busy {
		t.Errorf("busyPorts = %v", got)
	}
	// The web server's own ports are not a conflict.
	r.ports = []int{busy}
	if r.portChecks(cfg)[0].Busy || len(busyPorts(cfg, r.ports)) != 0 && busyPorts(cfg, r.ports)[0] == busy {
		t.Error("LaraMonster's own port reported as busy")
	}
}

func TestPortsBusyErrorNamesPorts(t *testing.T) {
	if msg := (&PortsBusyError{Ports: []int{8080}}).Error(); !strings.HasPrefix(msg, "port 8080 is already used") {
		t.Error(msg)
	}
	if msg := (&PortsBusyError{Ports: []int{8080, 8443}}).Error(); !strings.HasPrefix(msg, "ports 8080 and 8443 are already used") {
		t.Error(msg)
	}
}
