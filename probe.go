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
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type job struct {
	Origin  string
	Rank    string
	Timeout int // seconds
}

type result struct {
	Status int
	Reason string
	Detail string
	Millis int64
}

func newProbeTransport() *http.Transport {
	return &http.Transport{
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 1 << 20,
	}
}

func validOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return u.User == nil && (u.EscapedPath() == "" || u.EscapedPath() == "/") &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func probe(transport http.RoundTripper, j job) (r result) {
	start := time.Now()
	defer func() { r.Millis = time.Since(start).Milliseconds() }()
	if !validOrigin(j.Origin) {
		return result{Reason: "input", Detail: "expected an HTTP(S) origin"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(j.Timeout)*time.Second)
	defer cancel()
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
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		r.Reason = "timeout"
	case errors.As(err, &dns):
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

// Each child reads one assignment at a time and reuses its transport.
func serveWorker(in io.Reader, out io.Writer) error {
	decode, encode := json.NewDecoder(in), json.NewEncoder(out)
	transport := newProbeTransport()
	defer transport.CloseIdleConnections()
	for {
		var j job
		if err := decode.Decode(&j); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := encode.Encode(probe(transport, j)); err != nil {
			return err
		}
	}
}
