package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Spread successive assignments across providers before cycling through their alternates.
var publicResolvers = []struct{ name, primary, alternate string }{
	{"Cloudflare", "1.1.1.1", "1.0.0.1"},
	{"Google", "8.8.8.8", "8.8.4.4"},
	{"Quad9", "9.9.9.10", "149.112.112.10"},
	{"AdGuard", "94.140.14.140", "94.140.14.141"},
	{"Control D", "76.76.2.0", "76.76.10.0"},
}

func defaultResolverList() string {
	var primary, alternate []string
	for _, vendor := range publicResolvers {
		primary = append(primary, vendor.primary)
		alternate = append(alternate, vendor.alternate)
	}
	return strings.Join(append(primary, alternate...), ",")
}

func parseResolvers(list string) ([]string, error) {
	var servers []string
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if ip, err := netip.ParseAddr(item); err == nil {
			servers = append(servers, netip.AddrPortFrom(ip, 53).String())
			continue
		}
		endpoint, err := netip.ParseAddrPort(item)
		if err != nil || endpoint.Port() == 0 {
			return nil, fmt.Errorf("invalid DNS resolver %q: use an IP address with an optional port", item)
		}
		servers = append(servers, endpoint.String())
	}
	return servers, nil
}

// Only the socket destination changes. HTTP and TLS still see the origin hostname.
func resolverDialContext(server string) func(context.Context, string, string) (net.Conn, error) {
	dialer := net.Dialer{Resolver: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// net.Resolver chooses UDP or TCP, including TCP retries after truncation.
			return dialObservedDNS(ctx, network, server)
		},
	}}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if _, err := netip.ParseAddr(host); err != nil && !strings.HasSuffix(host, ".") {
			// Query absolute names so local search suffixes never go to a public resolver.
			address = net.JoinHostPort(host+".", port)
		}
		return dialer.DialContext(ctx, network, address)
	}
}
