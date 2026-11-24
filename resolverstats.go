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

const resolverFirstRow = 3

func (a *app) drawResolvers(v canvas, elapsed time.Duration) int {
	s := a.scan
	sec := int(elapsed / time.Second)
	seconds := max(0.001, elapsed.Seconds()-float64(max(0, sec-dnsWindow+1)))
	v.heading(0, "DNS RESOLVERS · ↑↓ select")
	mode := "DNS " + s.dnsLabel() + " · last 10s"
	if s.mode == "system" && len(s.resolverStats) == 0 {
		mode = "System DNS · no per-resolver metrics"
	}
	v.text(0, 1, quiet, mode)
	wide := v.w >= 55
	columns := func(name, rate, pct, avg, ref, policy, timeout, last string) string {
		row := fmt.Sprintf("%-9s %5s %6s %5s", name, rate, pct, avg)
		if wide {
			return fmt.Sprintf("%s %4s %4s %4s  %s", row, ref, policy, timeout, last)
		}
		return fmt.Sprintf("%s %4s", row, timeout)
	}
	v.text(2, 2, quiet, columns("Endpoint", "R/s", "Reply%", "Avgms", "Ref", "EDE", "TO", "Last"))
	for i, endpoint := range resolverRows {
		r := s.resolverStats[endpoint]
		rate, pct, avg, ref, ede, to, last := "—", "—", "—", "—", "—", "—", "—"
		style := quiet
		if r != nil {
			b := r.window(sec)
			rate = fmt.Sprintf("%.1f", float64(b.replies)/seconds)
			if b.finished > 0 {
				pct = fmt.Sprintf("%.0f%%", 100*float64(b.replies)/float64(b.finished))
			}
			if b.replies > 0 {
				avg = fmt.Sprintf("%.0f", float64(b.rtt)/float64(time.Millisecond)/float64(b.replies))
			}
			ref, ede, to = number(r.refused), number(r.policy), number(r.timeouts)
			if r.last != "" {
				last = r.last
			}
			style = plain
			if r.lastIssue != "" && sec-r.issueSecond < dnsWindow {
				style = bad
			}
		}
		if i == a.resolverRow {
			v.text(0, resolverFirstRow+i, accent, "›")
			style = style.Bold(true).Dim(false)
		}
		v.text(2, resolverFirstRow+i, style, columns(endpointLabel(endpoint), rate, pct, avg, ref, ede, to, last))
	}

	endpoint := resolverRows[a.resolverRow]
	r := s.resolverStats[endpoint]
	y := resolverFirstRow + len(resolverRows)
	label := "Iterative · authoritative servers"
	for _, p := range providerNames {
		if providers[p] == endpoint {
			label = p + " · " + endpoint
			break
		}
	}
	v.text(0, y, strong, label)
	if r == nil {
		v.text(0, y+1, quiet, "No lookups recorded")
		return y + 2
	}
	// Keep all lifetime counters readable even in the narrow side-by-side layout.
	for _, line := range wrapItems(r.counters(), "  ", v.w) {
		y++
		v.text(0, y, plain, line)
	}
	if r.lastIssue != "" {
		y++
		v.text(0, y, bad, fmt.Sprintf("Last issue %s · %ds ago", r.lastIssue, sec-r.issueSecond))
	}
	return y + 1
}

func (r *resolverStats) counters() []string {
	items := []string{
		fmt.Sprintf("Lookups %d", r.attempts), fmt.Sprintf("Pending %d", r.attempts-r.finished),
		fmt.Sprintf("Replies %d", r.replies), fmt.Sprintf("Cached %d", r.cached),
		fmt.Sprintf("Refused %d", r.refused), fmt.Sprintf("Policy EDE %d", r.policy),
		fmt.Sprintf("Timeouts %d", r.timeouts), fmt.Sprintf("SERVFAIL %d", r.servfail),
		fmt.Sprintf("NXDOMAIN %d", r.nxdomain), fmt.Sprintf("Net errors %d", r.networkErrors),
	}
	if r.last != "" {
		items = append(items, "Last "+r.last)
	}
	return items
}
