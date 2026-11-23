package main

import (
	"context"
	"encoding/binary"
	"io"
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
			tr := newProbeTransport(dns)
			defer tr.CloseIdleConnections()
			var stats resolverStats
			probe(tr, job{Origin: "http://status.webster.test", Timeout: 3, Resolver: dns}, func(e dnsEvent) { stats.record(e, 5) })
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
	tr := newProbeTransport(endpoint)
	defer tr.CloseIdleConnections()
	var stats resolverStats
	r := probe(tr, job{Origin: "http://silent.webster.test", Timeout: 1, Resolver: endpoint}, func(e dnsEvent) { stats.record(e, 0) })
	if r.Reason != "timeout" || stats.attempts == 0 || stats.attempts != stats.timeouts || stats.replies != 0 {
		t.Fatalf("result %+v, stats %+v", r, stats)
	}
	httpd := httptest.NewServer(http.NotFoundHandler())
	defer httpd.Close()
	r = probe(tr, job{Origin: httpd.URL, Timeout: 1, Resolver: endpoint}, func(e dnsEvent) { t.Errorf("unexpected event for IP literal: %+v", e) })
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
	s, err := startScan(context.Background(), path, dir, settings{targets: 1, processes: 1, timeout: 2, rotatingDNS: true, resolvers: []string{endpoint}}, 1)
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

func TestDNSTCPSplitFrame(t *testing.T) {
	// Feed the two-byte TCP length and DNS body in arbitrary pieces.
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	var stats resolverStats
	o := dnsObserver{ctx: context.Background(), emit: func(e dnsEvent) { stats.record(e, 0) }, open: make(map[*dnsAttempt]bool)}
	q := dnsmessage.Question{Name: dnsmessage.MustNewName("test.example."), Class: dnsmessage.ClassINET, Type: dnsmessage.TypeAAAA}
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: 123, Response: true, RCode: dnsmessage.RCodeRefused}, Questions: []dnsmessage.Question{q}}
	wire, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(wire)))
	frame = append(frame, wire...)
	c := dnsWireConn{Conn: client, attempt: o.begin(), tcp: true, id: 123, question: q}
	go func() { _, _ = peer.Write(frame) }()
	buf := make([]byte, 3)
	for remaining := len(frame); remaining > 0; {
		n, err := io.ReadFull(&c, buf[:min(remaining, len(buf))])
		if err != nil {
			t.Fatal(err)
		}
		remaining -= n
	}
	c.Close()
	if stats.refused != 1 || stats.replies != 1 || stats.finished != 1 {
		t.Fatalf("%+v", stats)
	}
}
