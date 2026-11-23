package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProbeKeepsFirstStatus(t *testing.T) {
	for _, code := range []int{200, 302, 403, 405, 503} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodHead || r.URL.Path != "/" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL)
			}
			if code == 302 {
				w.Header().Set("Location", "/elsewhere")
			}
			w.WriteHeader(code)
		}))
		got := probe(newProbeTransport(""), job{Origin: server.URL, Timeout: 5})
		server.Close()
		if got.Status != code || got.Reason != "" || calls.Load() != 1 {
			t.Fatalf("HTTP %d: %+v, calls %d", code, got, calls.Load())
		}
	}
}

func TestProbeClassifiesNetworkFailures(t *testing.T) {
	secure := httptest.NewTLSServer(http.NotFoundHandler())
	defer secure.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	dns := newProbeTransport("")
	dns.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.DNSError{Name: "missing.invalid", Err: "no such host", IsNotFound: true}
	}
	for _, tt := range []struct {
		origin, want string
		transport    *http.Transport
	}{
		{secure.URL, "tls", newProbeTransport("")},
		{closed.URL, "connection", newProbeTransport("")},
		{"https://missing.invalid", "dns", dns},
		{"https://example.org/path", "input", newProbeTransport("")},
	} {
		got := probe(tt.transport, job{Origin: tt.origin, Timeout: 5})
		if got.Reason != tt.want || got.Status != 0 || got.Detail == "" {
			t.Errorf("want %s, got %+v", tt.want, got)
		}
	}
}
