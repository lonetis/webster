package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/zmap/zdns/v2/src/zdns"
	"golang.org/x/net/dns/dnsmessage"
)

func testDNS(t *testing.T, nxdomain bool) string {
	t.Helper()
	return testDNSReplies(t, func(m *dnsmessage.Message) {
		if !m.RecursionDesired {
			t.Error("recursive lookup missing RD bit")
		}
		q := m.Questions[0]
		if nxdomain {
			m.RCode = dnsmessage.RCodeNameError
		} else if q.Type == dnsmessage.TypeA {
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: q.Class, TTL: 300},
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
	transport := newProbeTransport()
	defer transport.CloseIdleConnections()
	transport.TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	origin := strings.Replace(server.URL, "127.0.0.1", hostname, 1)
	got := probe(transport, job{Origin: origin, Timeout: 3, Resolver: upstream}, testClient(t), nil)
	if got.Status != 204 {
		t.Fatalf("HTTPS result: %+v", got)
	}
}

func testClient(t *testing.T) *workerDNS {
	t.Helper()
	w := &workerDNS{}
	t.Cleanup(w.close)
	return w
}

func TestIterativeUsesRootHints(t *testing.T) {
	// This root fixture is also authoritative for the test name. It will
	// refuse a recursive query, as a real authoritative server can.
	root := testDNSReplies(t, func(m *dnsmessage.Message) {
		if m.RecursionDesired {
			t.Error("iterative query requested recursion")
			m.RCode = dnsmessage.RCodeRefused
			return
		}
		m.Authoritative, m.RecursionAvailable = true, false
		q := m.Questions[0]
		if q.Type == dnsmessage.TypeAAAA {
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: q.Class},
				Body: &dnsmessage.AAAAResource{AAAA: netip.IPv6Loopback().As16()}}}
		}
	})
	addr := netip.MustParseAddrPort(root)
	w := testClient(t)
	w.config = zdns.NewResolverConfig()
	w.config.RootNameServersV4 = []zdns.NameServer{{IP: addr.Addr().AsSlice(), Port: addr.Port()}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := w.lookup(ctx, "iterative", "V6.Example.", 3*time.Second, nil)
	if err != nil || len(ips) != 1 || ips[0] != netip.IPv6Loopback() {
		t.Fatalf("iterative lookup returned %v, %v", ips, err)
	}
}

func TestSwitchDNSDuringScan(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	// IP literals keep this test of assignment and live controls offline.
	rows := fmt.Sprintf("origin,rank\n%s,1\n%s,2\n%s,3\n%s,4\n", server.URL, server.URL, server.URL, server.URL)
	if err := os.WriteFile(input, []byte(rows), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := settings{targets: 4, processes: 1, timeout: 2, mode: "system", provider: "cloudflare"}
	s, err := startScan(context.Background(), input, cfg, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.finish()
	a := app{settings: cfg, scan: s}
	key := func(ch rune) { a.key(context.Background(), tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone)) }
	s.fill()
	for i, want := range []string{"", providers["cloudflare"], providers["google"], "iterative"} {
		switch i {
		case 0, 2:
			key('d')
		case 1:
			key('v')
		}
		select {
		case c := <-s.reports:
			if c.err != nil || c.result.Status != 404 || c.job.Resolver != want {
				t.Fatalf("assignment %d: %+v", i, c)
			}
			s.accept(c)
		case <-time.After(5 * time.Second):
			t.Fatal("no worker result")
		}
	}
}

func TestCacheIsScopedToSelectedResolver(t *testing.T) {
	w := testClient(t)
	first, second := testDNS(t, false), testDNS(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stats resolverStats
	emit := func(e dnsEvent) { stats.record(e, 0) }
	for i := 0; i < 2; i++ {
		ips, err := w.lookup(ctx, first, "cache.webster.test", 2*time.Second, emit)
		if err != nil || len(ips) != 1 || ips[0].String() != "127.0.0.1" {
			t.Fatalf("first resolver: %v, %v", ips, err)
		}
	}
	if stats.cached != 1 || stats.replies != 3 {
		t.Fatalf("cache affected measured reply rate: %+v", stats)
	}
	ips, err := w.lookup(ctx, second, "cache.webster.test", 2*time.Second, emit)
	if err == nil || len(ips) != 0 || stats.nxdomain != 1 {
		t.Fatalf("second resolver: %v, %v, %+v", ips, err, stats)
	}
}

func TestAddressAnswersFollowsOnlyOriginAlias(t *testing.T) {
	records := []any{
		zdns.Answer{Name: "other.example", RrType: 1, RrClass: 1, Answer: "192.0.2.19"},
		zdns.Answer{Name: "origin.example", RrType: 5, RrClass: 1, Answer: "Web.Example."},
		zdns.Answer{Name: "web.example", RrType: 1, RrClass: 1, Answer: "192.0.2.20"},
	}
	ips := addressAnswers("origin.example", 1, records)
	if len(ips) != 1 || ips[0].String() != "192.0.2.20" {
		t.Fatalf("unexpected HTTP peers: %v", ips)
	}
}
