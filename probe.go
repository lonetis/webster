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
	Origin   string
	Rank     string
	Timeout  int    // seconds
	Resolver string // empty uses system DNS
}

type result struct {
	Status int
	Reason string
	Detail string
	Millis int64
}

func newProbeTransport(resolver string) *http.Transport {
	transport := &http.Transport{
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 1 << 20,
	}
	if resolver != "" {
		transport.DialContext = resolverDialContext(resolver)
	}
	return transport
}

func validOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return u.User == nil && (u.EscapedPath() == "" || u.EscapedPath() == "/") &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func probe(transport http.RoundTripper, j job, emit func(dnsEvent)) (r result) {
	start := time.Now()
	defer func() { r.Millis = time.Since(start).Milliseconds() }()
	if !validOrigin(j.Origin) {
		return result{Reason: "input", Detail: "expected an HTTP(S) origin"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(j.Timeout)*time.Second)
	defer cancel()
	if j.Resolver != "" && emit != nil {
		o := &dnsObserver{ctx: ctx, emit: emit, open: make(map[*dnsAttempt]bool)}
		defer o.close()
		ctx = context.WithValue(ctx, dnsObserverKey{}, o)
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
	if isDNS && j.Resolver != "" {
		// net.Resolver reports the OS nameserver even when its Dial hook overrides it.
		detail := *dns
		detail.Server = j.Resolver
		r.Detail = detail.Error()
	}
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

// Each child handles one assignment at a time. Keep transports scoped to their resolver.
func serveWorker(in io.Reader, out io.Writer) error {
	decode, encode := json.NewDecoder(in), json.NewEncoder(out)
	transports := make(map[string]*http.Transport)
	for {
		var j job
		if err := decode.Decode(&j); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		transport := transports[j.Resolver]
		if transport == nil {
			transport = newProbeTransport(j.Resolver)
			transports[j.Resolver] = transport
		}
		r := probe(transport, j, func(e dnsEvent) {
			// A final Encode below also reports a broken pipe.
			_ = encode.Encode(workerMessage{DNS: &e})
		})
		transport.CloseIdleConnections()
		if err := encode.Encode(workerMessage{Result: &r}); err != nil {
			return err
		}
	}
}
