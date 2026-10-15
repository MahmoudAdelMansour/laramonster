package services

import (
	"net"
	"testing"
)

func TestFreePortSkipsBusyPorts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port
	if !PortInUse(busy) {
		t.Fatalf("port %d should be in use", busy)
	}
	free, err := FreePort(busy - 1)
	if err != nil {
		t.Fatal(err)
	}
	if free == busy || free < busy-1 {
		t.Errorf("FreePort(%d) = %d", busy-1, free)
	}
}
