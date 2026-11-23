package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type job struct {
	Origin   string
	Timeout  int    // seconds
	Resolver string // empty uses system DNS
	DNSOnly  bool   `json:",omitempty"`
}

type result struct {
	Status   int
	Resolved bool `json:",omitempty"`
	Reason   string
	Detail   string
	Millis   int64
}

func newProbeTransport() *http.Transport {
	return &http.Transport{
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 1 << 20,
	}
}

func originHostname(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	if u.User != nil || (u.EscapedPath() != "" && u.EscapedPath() != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ""
	}
	return u.Hostname()
}

func probe(transport *http.Transport, j job, dnsClient *workerDNS, emit func(dnsEvent)) (r result) {
	start := time.Now()
	defer func() { r.Millis = time.Since(start).Milliseconds() }()
	host := originHostname(j.Origin)
	if host == "" {
		return result{Reason: "input", Detail: "expected an HTTP(S) origin"}
	}
	timeout := time.Duration(j.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, ipErr := netip.ParseAddr(host)
	if j.DNSOnly && ipErr == nil {
		return result{Reason: skippedLiteral}
	}
	if ipErr != nil && (j.DNSOnly || j.Resolver != "") {
		ips, err := dnsClient.lookup(ctx, j.Resolver, host, timeout, j.DNSOnly, emit)
		if err != nil {
			return lookupFailure(err, j.DNSOnly)
		}
		if j.DNSOnly {
			return result{Resolved: true}
		}
		transport = transport.Clone()
		transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			return dialResolved(ctx, network, address, ips)
		}
		defer transport.CloseIdleConnections()
	}
	var handshakeFailed atomic.Bool
	trace := &httptrace.ClientTrace{TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
		handshakeFailed.Store(err != nil)
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodHead, j.Origin, nil)
	if err != nil {
		return result{Reason: "input", Detail: err.Error()}
	}
	req.Header.Set("User-Agent", "Webster/1.0 (origin reachability check)")
	resp, err := transport.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		return result{Status: resp.StatusCode}
	}
	// Keep the individual network error as well as its aggregate category.
	r.Detail = err.Error()
	var dns *net.DNSError
	isDNS := errors.As(err, &dns)
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		r.Reason = "timeout"
	case isDNS:
		r.Reason = "dns"
	case handshakeFailed.Load():
		r.Reason = "tls"
	case strings.Contains(r.Detail, "malformed HTTP") || strings.Contains(r.Detail, "HTTP response headers exceeded"):
		r.Reason = "protocol"
	default:
		r.Reason = "connection"
	}
	return r
}

type workerMessage struct {
	DNS    *dnsEvent `json:",omitempty"`
	Result *result   `json:",omitempty"`
}

// Each child handles one assignment at a time.
func serveWorker(in io.Reader, out io.Writer) error {
	decode, encode := json.NewDecoder(in), json.NewEncoder(out)
	transport := newProbeTransport()
	defer transport.CloseIdleConnections()
	var dnsClient workerDNS
	defer dnsClient.close()
	for {
		var j job
		if err := decode.Decode(&j); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		r := probe(transport, j, &dnsClient, func(e dnsEvent) {
			// A final Encode below also reports a broken pipe.
			_ = encode.Encode(workerMessage{DNS: &e})
		})
		if err := encode.Encode(workerMessage{Result: &r}); err != nil {
			return err
		}
	}
}
