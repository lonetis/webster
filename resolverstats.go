package main

import (
	"fmt"
	"net"
	"time"
)

const dnsWindow = 10

type dnsBucket struct {
	second, finished, replies int
	rtt                       time.Duration
}
type resolverStats struct {
	attempts, finished, replies, cached                          int
	refused, policy, servfail, nxdomain, timeouts, networkErrors int
	last, lastIssue                                              string
	issueSecond                                                  int
	recent                                                       [dnsWindow]dnsBucket
}

func (s *resolverStats) record(e dnsEvent, second int) {
	if e.Status == "start" {
		s.attempts++
		return
	}
	b := &s.recent[second%dnsWindow]
	if b.second != second {
		*b = dnsBucket{second: second}
	}
	s.finished++
	s.last = e.Status
	if e.Status == "cached" {
		s.cached++
		return
	}
	b.finished++
	if e.Reply {
		s.replies++
		b.replies++
		b.rtt += e.RTT
	}
	if e.Policy {
		s.policy++
		s.last += " EDE"
	}
	switch e.Status {
	case "REFUSED":
		s.refused++
	case "SERVFAIL":
		s.servfail++
	case "NXDOMAIN":
		s.nxdomain++
	case "timeout":
		s.timeouts++
	case "net error":
		s.networkErrors++
	}
	if e.Policy || e.Status == "REFUSED" || e.Status == "SERVFAIL" || e.Status == "net error" || e.Status == "timeout" {
		s.lastIssue = s.last
		s.issueSecond = second
	}
}

func (s *resolverStats) window(second int) dnsBucket {
	var total dnsBucket
	for _, b := range s.recent {
		if b.second <= second && b.second > second-dnsWindow {
			total.finished += b.finished
			total.replies += b.replies
			total.rtt += b.rtt
		}
	}
	return total
}

func endpointLabel(endpoint string) string {
	host, port, err := net.SplitHostPort(endpoint)
	if err == nil && port == "53" {
		return host
	}
	return endpoint
}

func (a *app) drawResolvers(v canvas) {
	s := a.scan
	now := time.Now()
	if !s.ended.IsZero() {
		now = s.ended
	}
	elapsed := now.Sub(s.started)
	sec := int(elapsed / time.Second)
	seconds := max(0.001, elapsed.Seconds()-float64(max(0, sec-dnsWindow+1)))
	v.text(2, 2, strong, "DNS resolvers · "+s.dnsLabel()+" mode")
	hint := "Last 10s lookup rates and duration · counts since run start"
	if s.mode == "system" && len(s.resolverStats) == 0 {
		hint = "Choose recursive or iterative DNS for lookup metrics"
	}
	v.text(2, 3, quiet, hint)
	wide := v.w >= 72
	addrWidth := max(15, min(45, v.w-39))
	headers := "   R/s  Reply%   Ref   TO  Last"
	if wide {
		addrWidth = min(45, v.w-52)
		headers = "    R/s  Reply%   Avgms   Ref  EDE    TO  Last"
	}
	v.text(2, 5, quiet, fmt.Sprintf("%-*s%s", addrWidth, "Resolver", headers))
	rows := v.h - 13
	first := max(0, a.resolverRow-rows+1)
	for i := first; i < min(len(resolverRows), first+rows); i++ {
		endpoint := resolverRows[i]
		r := s.resolverStats[endpoint]
		if r == nil {
			r = &resolverStats{}
		}
		b := r.window(sec)
		pct, rtt := "—", "—"
		if b.finished > 0 {
			pct = fmt.Sprintf("%.0f%%", 100*float64(b.replies)/float64(b.finished))
		}
		if b.replies > 0 {
			rtt = fmt.Sprintf("%.0f", float64(b.rtt)/float64(time.Millisecond)/float64(b.replies))
		}
		label := endpointLabel(endpoint)
		if len(label) > addrWidth {
			label = label[:addrWidth-1] + "…"
		}
		last := r.last
		if last == "" {
			last = "—"
		}
		rate := float64(b.replies) / seconds
		line := fmt.Sprintf("%-*s %5.1f %7s %5d %4d  %s", addrWidth, label, rate, pct, r.refused, r.timeouts, last)
		if wide {
			line = fmt.Sprintf("%-*s %6.1f %7s %7s %5d %4d %5d  %s", addrWidth, label, rate, pct, rtt, r.refused, r.policy, r.timeouts, last)
		}
		style := plain
		if r.lastIssue != "" && sec-r.issueSecond < dnsWindow {
			style = bad
		}
		if i == a.resolverRow {
			style = style.Reverse(true)
		}
		v.text(2, 6+i-first, style, line)
	}
	endpoint := resolverRows[a.resolverRow]
	stats := s.resolverStats[endpoint]
	if stats == nil {
		stats = &resolverStats{}
	}
	label := endpoint
	for _, p := range providerNames {
		if providers[p] == endpoint {
			label = p + " · " + endpoint
			break
		}
	}
	v.text(2, v.h-7, strong, fmt.Sprintf("%d/%d  %s", a.resolverRow+1, len(resolverRows), label))
	v.text(2, v.h-6, plain, fmt.Sprintf("Lookups %d   Pending %d   Replies %d   Cached %d", stats.attempts, stats.attempts-stats.finished, stats.replies, stats.cached))
	v.text(2, v.h-5, plain, fmt.Sprintf("REFUSED %d   Policy EDE %d   Timeouts %d", stats.refused, stats.policy, stats.timeouts))
	v.text(2, v.h-4, plain, fmt.Sprintf("SERVFAIL %d   NXDOMAIN %d   Net errors %d", stats.servfail, stats.nxdomain, stats.networkErrors))
	if stats.lastIssue != "" {
		v.text(2, v.h-3, bad, fmt.Sprintf("Last issue  %s · %ds ago", stats.lastIssue, sec-stats.issueSecond))
	}
}
