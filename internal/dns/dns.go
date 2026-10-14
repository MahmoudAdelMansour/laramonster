// Package dns runs a tiny resolver that answers *.<tld> with loopback, and
// knows how to point each OS at it (or fall back to the hosts file).
package dns

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

// Server answers A/AAAA queries for any name under the configured TLD.
type Server struct {
	mu      sync.Mutex
	tld     string
	addr    string
	servers []*dns.Server
}

func NewServer() *Server { return &Server{} }

// Apply (re)starts the server on 127.0.0.1:port for tld. Port 0 stops it.
func (s *Server) Apply(tld string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	addr := ""
	if port > 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", port)
	}
	s.tld = strings.ToLower(tld)
	if addr == s.addr {
		return nil
	}
	s.shutdown()
	s.addr = addr
	if addr == "" {
		return nil
	}
	handler := dns.HandlerFunc(s.serve)
	for _, network := range []string{"udp", "tcp"} {
		srv := &dns.Server{Addr: addr, Net: network, Handler: handler}
		started := make(chan error, 1)
		srv.NotifyStartedFunc = func() { started <- nil }
		go func() {
			if err := srv.ListenAndServe(); err != nil {
				select {
				case started <- err:
				default:
					log.Printf("dns %s: %v", network, err)
				}
			}
		}()
		if err := <-started; err != nil {
			s.shutdown()
			s.addr = ""
			return fmt.Errorf("dns server on %s: %w", addr, err)
		}
		s.servers = append(s.servers, srv)
	}
	return nil
}

func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdown()
	s.addr = ""
}

func (s *Server) shutdown() {
	for _, srv := range s.servers {
		_ = srv.Shutdown()
	}
	s.servers = nil
}

func (s *Server) serve(w dns.ResponseWriter, req *dns.Msg) {
	s.mu.Lock()
	suffix := "." + s.tld + "."
	s.mu.Unlock()

	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	for _, q := range req.Question {
		name := strings.ToLower(q.Name)
		if name != suffix[1:] && !strings.HasSuffix(name, suffix) {
			m.Rcode = dns.RcodeNameError
			continue
		}
		hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: 60}
		switch q.Qtype {
		case dns.TypeA:
			hdr.Rrtype = dns.TypeA
			m.Answer = append(m.Answer, &dns.A{Hdr: hdr, A: net.IPv4(127, 0, 0, 1)})
		case dns.TypeAAAA:
			hdr.Rrtype = dns.TypeAAAA
			m.Answer = append(m.Answer, &dns.AAAA{Hdr: hdr, AAAA: net.IPv6loopback})
		}
	}
	_ = w.WriteMsg(m)
}

// Resolve asks the local server for name, used by health checks.
func Resolve(port int, name string) (string, error) {
	c := new(dns.Client)
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	r, _, err := c.Exchange(m, fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", err
	}
	for _, a := range r.Answer {
		if rec, ok := a.(*dns.A); ok {
			return rec.A.String(), nil
		}
	}
	return "", fmt.Errorf("no answer for %s", name)
}
