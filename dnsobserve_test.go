package main

import (
	"context"

	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSStatuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rcode  dnsmessage.RCode
		ede    byte
		policy bool
	}{
		{"REFUSED", dnsmessage.RCodeRefused, 18, true},
		{"SERVFAIL", dnsmessage.RCodeServerFailure, 3, false},
		{"NXDOMAIN", dnsmessage.RCodeNameError, 0, false},
		{"NXDOMAIN", dnsmessage.RCodeNameError, 15, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dns := testDNSReplies(t, func(m *dnsmessage.Message) {
				m.RCode = tc.rcode
				var opt dnsmessage.ResourceHeader
				opt.SetEDNS0(1232, tc.rcode, false)
				m.Additionals = []dnsmessage.Resource{{Header: opt, Body: &dnsmessage.OPTResource{Options: []dnsmessage.Option{{Code: 15, Data: []byte{0, tc.ede}}}}}}
			})
			tr := newProbeTransport()
			defer tr.CloseIdleConnections()
			var stats resolverStats
			probe(tr, job{Origin: "http://status.webster.test", Timeout: 3, Resolver: dns}, testClient(t), func(e dnsEvent) { stats.record(e, 5) })
			if stats.replies == 0 || stats.finished != stats.attempts || stats.replies != stats.finished {
				t.Fatalf("wrong attempt/reply counts: %+v", stats)
			}
			if (stats.policy > 0) != tc.policy || !strings.HasPrefix(stats.last, tc.name) {
				t.Fatalf("wrong resolver counters: %+v", stats)
			}
			if stats.window(14).replies != stats.replies || stats.window(15).finished != 0 {
				t.Fatal("incorrect rolling window")
			}
		})
	}
}

func TestDNSTimeoutAndLiteral(t *testing.T) {
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	endpoint := silent.LocalAddr().String()
	tr := newProbeTransport()
	defer tr.CloseIdleConnections()
	var stats resolverStats
	r := probe(tr, job{Origin: "http://silent.webster.test", Timeout: 1, Resolver: endpoint}, testClient(t), func(e dnsEvent) { stats.record(e, 0) })
	if r.Reason != "timeout" || stats.attempts == 0 || stats.attempts != stats.timeouts || stats.replies != 0 {
		t.Fatalf("result %+v, stats %+v", r, stats)
	}
	httpd := httptest.NewServer(http.NotFoundHandler())
	defer httpd.Close()
	r = probe(tr, job{Origin: httpd.URL, Timeout: 1, Resolver: endpoint}, testClient(t), func(e dnsEvent) { t.Errorf("unexpected event for IP literal: %+v", e) })
	if r.Status != 404 {
		t.Fatalf("%+v", r)
	}
}

func TestDNSUpdatesBeforeHTTPCompletes(t *testing.T) {
	endpoint := testDNS(t, false)
	httpd := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer httpd.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "origins.csv")
	origin := strings.Replace(httpd.URL, "127.0.0.1", "slow.webster.test", 1)
	if err := os.WriteFile(path, []byte("origin,rank\n"+origin+",1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	providers["fixture"] = endpoint
	t.Cleanup(func() { delete(providers, "fixture") })
	s, err := startScan(context.Background(), path, settings{targets: 1, processes: 1, timeout: 2, mode: "recursive", provider: "fixture"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.finish()
	s.fill()
	sawReply := false
	for s.completed < 1 {
		select {
		case c := <-s.reports:
			s.accept(c)
			if s.err != nil {
				t.Fatal(s.err)
			}
			if c.dns != nil && c.dns.Reply && s.completed == 0 {
				sawReply = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing worker report")
		}
	}
	stats := s.resolverStats[endpoint]
	if !sawReply || stats.timeouts != 0 || stats.replies != stats.attempts || s.reasons["timeout"] != 1 {
		t.Fatalf("DNS/HTTP misattribution: %+v %v", stats, s.reasons)
	}
}
