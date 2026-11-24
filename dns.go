package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/zmap/dns"
	"github.com/zmap/zdns/v2/src/zdns"
	"golang.org/x/net/idna"
)

var dnsModes = []string{"system", "recursive", "iterative"}
var providerNames = []string{"cloudflare", "google", "cloudflare-alt", "google-alt"}
var providers = map[string]string{
	"cloudflare": "1.1.1.1:53", "cloudflare-alt": "1.0.0.1:53",
	"google": "8.8.8.8:53", "google-alt": "8.8.4.4:53",
}
var resolverRows = []string{providers["cloudflare"], providers["google"], providers["cloudflare-alt"], providers["google-alt"], "iterative"}

type providerPool []string

func parseProviderPool(value string) (providerPool, error) {
	if strings.TrimSpace(value) == "all" {
		return providerNames, nil
	}
	var names providerPool
	for part := range strings.SplitSeq(value, ",") {
		name := strings.TrimSpace(part)
		if _, ok := providers[name]; !ok {
			return nil, fmt.Errorf("unknown recursive provider %q: choose all or a list from %s", name, strings.Join(providerNames, ", "))
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func (p providerPool) label() string {
	if slices.Equal(p, providerNames) {
		return "all"
	}
	return strings.Join(p, ",")
}

func (c settings) dnsLabel() string {
	if c.mode == "recursive" {
		return "recursive " + c.pool.label()
	}
	return c.mode
}

type dnsEvent struct {
	Status string
	Reply  bool          `json:",omitempty"`
	Policy bool          `json:",omitempty"`
	RTT    time.Duration `json:",omitempty"`
}

// Each probe goroutine performs lookups serially. ZDNS keys recursive cache entries by
// nameserver and keeps the iterative delegation cache in a separate namespace.
type workerDNS struct {
	sharedConfig  func() *zdns.ResolverConfig
	config        *zdns.ResolverConfig
	resolver      *zdns.Resolver
	iterative     bool
	singleAttempt bool
	timeout       time.Duration
}

func (w *workerDNS) close() {
	if w.resolver != nil {
		w.resolver.Close()
		w.resolver = nil
	}
}

func (w *workerDNS) prepare(iterative, singleAttempt bool, timeout time.Duration) error {
	if w.resolver != nil && w.iterative == iterative && w.timeout == timeout && w.singleAttempt == singleAttempt {
		return nil
	}
	w.close()
	if w.config == nil {
		if w.sharedConfig != nil {
			w.config = w.sharedConfig()
		} else {
			w.config = zdns.NewResolverConfig()
		}
	}
	config := *w.config
	config.ShouldRecycleSockets = false
	config.Timeout = timeout
	config.NetworkTimeout = timeout
	if iterative {
		config.NetworkTimeout = min(timeout, 2*time.Second)
	}
	if singleAttempt {
		config.Retries = 0
	}
	w.singleAttempt = singleAttempt
	w.iterative, w.timeout = iterative, timeout
	var err error
	w.resolver, err = zdns.InitResolver(&config)
	return err
}

func (w *workerDNS) lookup(ctx context.Context, endpoint, host string, timeout time.Duration, onlyA bool, emit func(dnsEvent)) ([]netip.Addr, error) {
	name, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil {
		return nil, err
	}
	name = strings.ToLower(name)
	// HTTP checks using system DNS delegate resolution to net/http. Only
	// DNS-only checks use the system resolver here.
	if endpoint == "" {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", name+".")
		if err == nil && len(ips) == 0 {
			err = &net.DNSError{Name: name, Err: "NODATA"}
		}
		return ips, err
	}
	var ns *zdns.NameServer
	if endpoint != "iterative" {
		addr, err := netip.ParseAddrPort(endpoint)
		if err != nil {
			return nil, err
		}
		ns = &zdns.NameServer{IP: addr.Addr().AsSlice(), Port: addr.Port()}
	}
	if err := w.prepare(ns == nil, onlyA && ns != nil, timeout); err != nil {
		return nil, err
	}

	var addresses []netip.Addr
	var lookupErr error
	types := []uint16{dns.TypeA, dns.TypeAAAA}
	if onlyA {
		types = types[:1]
	}
	for _, typ := range types {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// A single-family check can spend the full origin deadline on DNS.
		queryCtx, cancel := context.WithTimeout(ctx, timeout/time.Duration(len(types)))
		q := zdns.Question{Name: name, Type: typ, Class: dns.ClassINET}
		if emit != nil {
			emit(dnsEvent{Status: "start"})
		}
		start := time.Now()
		var res *zdns.SingleQueryResult
		var trace zdns.Trace
		var status zdns.Status
		if ns == nil {
			res, trace, status, err = w.resolver.IterativeLookup(queryCtx, &q)
		} else {
			res, trace, status, err = w.resolver.ExternalLookup(queryCtx, &q, ns)
		}
		event := lookupEvent(res, trace, status, queryCtx, err)
		event.RTT = time.Since(start)
		cancel()
		if emit != nil {
			emit(event)
		}
		if status == zdns.StatusNoError && err == nil && res != nil {
			addresses = append(addresses, addressAnswers(name, typ, res.Answers)...)
		} else if event.Status == "timeout" {
			lookupErr = context.DeadlineExceeded
		} else if lookupErr == nil {
			lookupErr = &net.DNSError{Name: name, Server: endpoint, Err: event.Status}
		}
		if status == zdns.StatusNXDomain || status == zdns.StatusRefused {
			break
		}
	}
	if len(addresses) != 0 {
		return addresses, nil
	}
	if lookupErr == nil {
		lookupErr = &net.DNSError{Name: name, Server: endpoint, Err: "NODATA"}
	}
	return nil, lookupErr
}

func lookupFailure(err error, dnsOnly bool) result {
	r := result{Reason: "dns", Detail: err.Error()}
	var netErr net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		r.Reason = "timeout"
	case dnsOnly && errors.As(err, &dnsErr):
		if _, ok := dns.StringToRcode[dnsErr.Err]; ok || dnsErr.Err == "NODATA" {
			r.Reason = dnsErr.Err
		} else if dnsErr.IsNotFound {
			r.Reason = "not found"
		}
	}
	return r
}

func lookupEvent(res *zdns.SingleQueryResult, trace zdns.Trace, status zdns.Status, ctx context.Context, err error) dnsEvent {
	e := dnsEvent{Status: string(status)}
	_, rcode := dns.StringToRcode[e.Status]
	deadline, _ := ctx.Deadline()
	var netErr net.Error
	switch {
	case status == zdns.StatusNoError && err == nil && len(trace) > 0 && !slices.ContainsFunc(trace, func(step zdns.TraceStep) bool { return !bool(step.Cached) }):
		e.Status = "cached"
	case status == zdns.StatusTimeout || status == zdns.StatusIterTimeout || errors.Is(err, context.DeadlineExceeded) || (err != nil && !time.Now().Before(deadline)) || (errors.As(err, &netErr) && netErr.Timeout()):
		e.Status = "timeout"
	case rcode && (status != zdns.StatusNoError || err == nil):
		e.Reply = true
	case status == zdns.StatusError || status == zdns.StatusNoError || status == "":
		e.Status = "net error"
	}
	if res != nil {
		for _, rr := range res.Additionals {
			if opt, ok := rr.(zdns.EDNSAnswer); ok {
				for _, ede := range opt.EDE {
					if ede.InfoCode >= 15 && ede.InfoCode <= 18 {
						e.Policy = true
					}
				}
			}
		}
	}
	return e
}

// Only accept addresses belonging to the origin or its canonical name.
// Referrals, glue and unrelated answer records must never select an HTTP peer.
func addressAnswers(name string, typ uint16, answers []any) []netip.Addr {
	aliases := make(map[string]string)
	addresses := make(map[string][]netip.Addr)
	for _, rr := range answers {
		a, ok := rr.(zdns.Answer)
		if !ok || a.RrClass != dns.ClassINET {
			continue
		}
		owner := strings.ToLower(strings.TrimSuffix(a.Name, "."))
		switch a.RrType {
		case dns.TypeCNAME:
			aliases[owner] = strings.ToLower(strings.TrimSuffix(a.Answer, "."))
		case typ:
			if ip, err := netip.ParseAddr(a.Answer); err == nil && !slices.Contains(addresses[owner], ip) {
				addresses[owner] = append(addresses[owner], ip)
			}
		}
	}
	for hops := 0; hops <= len(aliases); hops++ {
		if ips := addresses[name]; len(ips) > 0 {
			return ips
		}
		next, ok := aliases[name]
		if !ok {
			break
		}
		name = next
	}
	return nil
}

// Keep the original URL for HTTP Host, TLS SNI and certificate verification.
func dialResolved(ctx context.Context, network, address string, ips []netip.Addr) (net.Conn, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	for i, ip := range ips {
		dialCtx, cancel := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(ips)-i))
		var d net.Dialer
		conn, dialErr := d.DialContext(dialCtx, network, net.JoinHostPort(ip.String(), port))
		cancel()
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
	}
	return nil, err
}
