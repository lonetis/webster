package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/zmap/zdns/v2/src/zdns"
	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSOnlyScan(t *testing.T) {
	var connections, queries atomic.Int32
	origin := httptest.NewUnstartedServer(http.NotFoundHandler())
	origin.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			connections.Add(1)
		}
	}
	origin.Start()
	defer origin.Close()

	endpoint := testDNSReplies(t, func(m *dnsmessage.Message) {
		q := m.Questions[0]
		queries.Add(1)
		if q.Type != dnsmessage.TypeA || !m.RecursionDesired {
			t.Errorf("unexpected query: %+v", m)
		}
		switch q.Name.String() {
		case "has-a.webster.test.":
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: q.Class},
				Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}}}
		case "no-a.webster.test.":
			// An empty NOERROR reply means there is no record of this type.
		case "missing.webster.test.":
			m.RCode = dnsmessage.RCodeNameError
		default:
			t.Errorf("unexpected name: %s", q.Name)
		}
	})
	providers["fixture"] = endpoint
	t.Cleanup(func() { delete(providers, "fixture") })

	path := filepath.Join(t.TempDir(), "origins.csv")
	var input strings.Builder
	input.WriteString("origin,rank\n")
	for i, name := range []string{"has-a.webster.test", "no-a.webster.test", "missing.webster.test", "127.0.0.1", "[::1]"} {
		fmt.Fprintf(&input, "%s,%d\n", strings.Replace(origin.URL, "127.0.0.1", name, 1), i+1)
	}
	input.WriteString("invalid,6\n")
	if err := os.WriteFile(path, []byte(input.String()), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := settings{targets: 6, processes: 2, timeout: 2, mode: "recursive", pool: providerPool{"fixture"}}
	a := app{settings: cfg, input: path}
	ctx := context.Background()
	toggle := tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone)
	a.key(ctx, toggle)
	a.key(ctx, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	s := a.scan
	if s == nil {
		t.Fatalf("setup failed: %s", a.message)
	}
	defer s.finish()
	a.key(ctx, toggle)
	if !s.dnsOnly {
		t.Fatal("check type changed after the run started")
	}
	for s.completed < cfg.targets {
		select {
		case report := <-s.reports:
			s.accept(report)
			if s.err != nil {
				t.Fatal(s.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing worker completion")
		}
	}
	if s.outcomeTotals() != "1 resolved · 3 failed · 2 skipped" || len(s.statuses) != 0 ||
		s.reasons["NXDOMAIN"] != 1 || s.reasons["NODATA"] != 1 || s.reasons["input"] != 1 {
		t.Fatalf("summary %s, reasons %v, HTTP statuses %v", s.outcomeTotals(), s.reasons, s.statuses)
	}
	stats := s.resolverStats[endpoint]
	if queries.Load() != 3 || stats == nil || stats.finished != 3 || stats.nxdomain != 1 || connections.Load() != 0 {
		t.Fatalf("DNS queries %d, DNS stats %+v, origin connections %d", queries.Load(), stats, connections.Load())
	}
}

func TestDNSOnlyOtherModes(t *testing.T) {
	// A nil HTTP transport also catches an accidental attempt to dial the origin.
	r := probe(nil, job{Origin: "https://localhost", Timeout: 3, DNSOnly: true}, testClient(t), nil)
	if !r.Resolved || r.Status != 0 {
		t.Fatalf("system lookup: %+v", r)
	}

	var queries atomic.Int32
	root := testDNSReplies(t, func(m *dnsmessage.Message) {
		queries.Add(1)
		q := m.Questions[0]
		if m.RecursionDesired || q.Type != dnsmessage.TypeA {
			t.Errorf("unexpected iterative query: %+v", m)
		}
		m.Authoritative, m.RecursionAvailable = true, false
		m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: q.Class},
			Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}}}
	})
	addr := netip.MustParseAddrPort(root)
	w := testClient(t)
	w.config = zdns.NewResolverConfig()
	w.config.RootNameServersV4 = []zdns.NameServer{{IP: addr.Addr().AsSlice(), Port: addr.Port()}}
	r = probe(nil, job{Origin: "https://iterative.test", Timeout: 3, DNSOnly: true, Resolver: "iterative"}, w, nil)
	if !r.Resolved || r.Status != 0 || queries.Load() != 1 {
		t.Fatalf("iterative lookup: %+v (%d queries)", r, queries.Load())
	}
}

func TestDNSOnlyTimeout(t *testing.T) {
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	var stats resolverStats
	j := job{Origin: "http://silent.webster.test", DNSOnly: true, Timeout: 1, Resolver: silent.LocalAddr().String()}
	r := probe(nil, j, testClient(t), func(e dnsEvent) { stats.record(e, 0) })
	if r.Reason != "timeout" || r.Resolved || r.Millis < 900 || stats.attempts != 1 || stats.timeouts != 1 {
		t.Fatalf("result %+v, stats %+v", r, stats)
	}
}
