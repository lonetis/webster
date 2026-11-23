package main

import (
	"context"
	"crypto/tls"
	"encoding/csv"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"golang.org/x/net/dns/dnsmessage"
)

func TestResolverAddresses(t *testing.T) {
	got, err := parseResolvers(" 1.1.1.1, 8.8.8.8:5353,2606:4700:4700::1111,[::1]:5353")
	want := []string{"1.1.1.1:53", "8.8.8.8:5353", "[2606:4700:4700::1111]:53", "[::1]:5353"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("addresses = %v, error = %v", got, err)
	}
	for _, invalid := range []string{"", "8.8.8.8,", "resolver.test", "127.0.0.1:0", "[::1]:65536"} {
		if _, err := parseResolvers(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func testDNS(t *testing.T, nxdomain bool) string {
	t.Helper()
	return testDNSReplies(t, func(m *dnsmessage.Message) {
		q := m.Questions[0]
		if nxdomain {
			m.RCode = dnsmessage.RCodeNameError
		} else if q.Type == dnsmessage.TypeA {
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: q.Class},
				Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}}}
		}
	})
}

func testDNSReplies(t *testing.T, answer func(*dnsmessage.Message)) string {
	t.Helper()
	sock, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { sock.Close(); <-done })
	go func() {
		defer close(done)
		var buf [2048]byte
		for {
			n, client, err := sock.ReadFrom(buf[:])
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) != 1 {
				continue
			}
			m.Response, m.RecursionAvailable = true, true
			m.Additionals = nil
			answer(&m)
			wire, err := m.Pack()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := sock.WriteTo(wire, client); err != nil {
				return
			}
		}
	}()
	return sock.LocalAddr().String()
}

func TestResolverPreservesHTTPSHostname(t *testing.T) {
	const hostname = "webster.example.com"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || host != hostname || r.TLS.ServerName != hostname || r.Method != http.MethodHead {
			t.Errorf("unexpected HTTP Host %q, TLS SNI %q, method %s", r.Host, r.TLS.ServerName, r.Method)
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	upstream := testDNS(t, false)
	transport := newProbeTransport(upstream)
	defer transport.CloseIdleConnections()
	transport.TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	origin := strings.Replace(server.URL, "127.0.0.1", hostname, 1)
	got := probe(transport, job{Origin: origin, Timeout: 3, Resolver: upstream}, nil)
	if got.Status != 204 {
		t.Fatalf("HTTPS result: %+v", got)
	}
}

func TestWorkersRotateDNSAndSwitchBackToSystem(t *testing.T) {
	resolvers := []string{testDNS(t, false), testDNS(t, true)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	origin := strings.Replace(server.URL, "127.0.0.1", "webster-dns.test", 1)
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	rows := fmt.Sprintf("origin,rank\n%s,1\n%s,2\n%s,3\n%s,4\n%s,5\n", origin, origin, server.URL, origin, origin)
	if err := os.WriteFile(input, []byte(rows), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := settings{targets: 5, processes: 2, timeout: 3, resolvers: resolvers}
	s, err := startScan(context.Background(), input, dir, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.finish()
	a := app{settings: cfg, scan: s}
	toggle := func() { a.key(context.Background(), tcell.NewEventKey(tcell.KeyRune, 'd', tcell.ModNone)) }
	toggle() // first two assignments start on separate workers
	toggle() // the next free worker gets a system-DNS job
	for s.completed < 5 {
		select {
		case c := <-s.reports:
			s.accept(c)
			if s.err != nil {
				t.Fatal(s.err)
			}
			if c.dns == nil && s.completed == 1 {
				toggle()
			} // resume rotation for the remaining jobs
		case <-time.After(8 * time.Second):
			t.Fatal("timed out waiting for worker")
		}
	}
	s.finish()
	file, err := os.Open(s.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 || records[0][7] != "dns_resolver" {
		t.Fatalf("unexpected CSV rows: %v", records)
	}
	want := map[string]string{"1": resolvers[0], "2": resolvers[1], "3": "system", "4": resolvers[0], "5": resolvers[1]}
	for _, row := range records[1:] {
		if want[row[1]] != row[7] {
			t.Fatalf("incorrect assigned resolver: %v", row)
		}
		if row[7] == resolvers[1] {
			if row[4] != "dns" || !strings.Contains(row[5], resolvers[1]) {
				t.Fatalf("expected NXDOMAIN from %s: %v", resolvers[1], row)
			}
		} else if row[3] != "204" {
			t.Fatalf("expected reachable: %v", row)
		}
	}
}
