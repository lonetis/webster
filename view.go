package main

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
)

var (
	plain  = tcell.StyleDefault
	strong = plain.Bold(true)
	quiet  = plain.Dim(true)
	accent = plain.Foreground(tcell.ColorAqua).Bold(true)
	good   = plain.Foreground(tcell.ColorMediumSpringGreen)
	bad    = plain.Foreground(tcell.ColorLightSalmon)
)

func number(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

type canvas struct {
	tcell.Screen
	w, h int
}

func (v canvas) text(x, y int, style tcell.Style, s string) {
	if x >= v.w || y < 0 || y >= v.h {
		return
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	v.PutStrStyled(x, y, s, style)
}
func (v canvas) rule(y int) { v.text(1, y, quiet, strings.Repeat("─", max(0, v.w-2))) }

func (a *app) draw(screen tcell.Screen) {
	w, h := screen.Size()
	v := canvas{screen, w, h}
	v.Clear()
	defer v.Show()
	state := "SETUP"
	if s := a.scan; s != nil {
		state = "RUNNING"
		if !s.ended.IsZero() {
			state = "COMPLETE"
		}
		if s.err != nil {
			state = "STOPPED"
		}
	}
	v.text(2, 0, accent, "WEBSTER")
	title := "Origin connection tester"
	if a.dnsOnly {
		title = "DNS-only check (A)"
	}
	if a.speed != nil {
		title, state = "Internet speed test", "RUNNING"
		if a.speed.phase == "" {
			state = "COMPLETE"
		}
		if a.speed.err != nil {
			state = "FAILED"
		}
	}
	v.text(12, 0, quiet, title)
	v.text(max(2, w-2-len(state)), 0, strong, state)
	if a.speed != nil {
		a.drawSpeed(v)
		return
	}
	if w < 52 || h < 19 {
		v.text(1, 3, plain, "Resize to 52 × 19 for the dashboard")
		if s := a.scan; s != nil {
			v.text(1, 5, plain, s.outcomeTotals())
		}
		v.text(1, h-1, quiet, "q quit")
		return
	}
	v.rule(1)
	var keys string
	if a.scan == nil {
		source := filepath.Base(a.input)
		if a.meta.Month != "" {
			date, _ := time.Parse("200601", a.meta.Month)
			source = "CrUX global · " + date.Format("January 2006")
		}
		v.text(2, 3, quiet, "SOURCE")
		v.text(2, 4, strong, source)
		if a.meta.Origins > 0 {
			v.text(2, 5, plain, number(a.meta.Origins)+" origins in rank order")
		}
		v.text(2, 7, quiet, "RUN SETTINGS")
		targets := number(a.targets)
		if a.targets == 0 {
			targets = "All origins"
		}
		check := "HTTP HEAD"
		if a.dnsOnly {
			check = "DNS only (A)"
		}
		for i, row := range [][3]string{
			{"Targets", targets, "← → presets   n exact"},
			{"Processes", number(a.processes), "+ / − adjust   p set"},
			{"Timeout", fmt.Sprintf("%d s", a.timeout), "[ / ] adjust   t set"},
			{"DNS", a.mode, "d cycle"},
			{"Check", check, "m switch"},
		} {
			y := 8 + i*2
			v.text(2, y, plain, row[0])
			v.text(15, y, accent, row[1])
			v.text(29, y, quiet, row[2])
		}
		if a.mode == "recursive" {
			v.text(2, 15, plain, "v Providers  "+a.pool.label())
		}
		keys = "Enter start run    s speed test    q quit"
	} else if a.showResolvers {
		a.drawResolvers(v)
		keys = "↑/↓ endpoint   r overview   s speed test   q quit"
		if a.running() {
			keys = "↑/↓ row  d/v DNS  r overview  s speed test  q quit"
		}
	} else {
		a.drawScan(v)
		keys = "+/- procs  [ ] time  d/v DNS  s speed test  q quit"
		if !a.running() {
			keys = "r resolvers  s speed test  q quit"
		}
	}
	message := a.message
	if a.scan != nil && a.scan.err != nil {
		message = a.scan.err.Error()
	}
	if message != "" {
		v.text(2, h-2, bad, message)
	} else {
		v.rule(h - 2)
	}
	if a.edit != "" {
		keys = a.edit + " › " + a.entry + "▏   Enter apply   Esc cancel   q quit"
	}
	v.text(2, h-1, accent, keys)
}

func (a *app) drawSpeed(v canvas) {
	s := a.speed
	v.rule(1)
	v.text(2, 3, strong, fmt.Sprintf("Apple · %d connections · %s per direction", a.speedOptions.streams, a.speedOptions.duration))
	for i, row := range s.rows() {
		v.text(2, 5+i, plain, row[0])
		v.text(17, 5+i, accent, row[1])
	}
	keys := "s run again   Esc back   q quit"
	if s.phase != "" {
		status := "Measuring " + strings.ToLower(s.phase)
		if s.phase == "endpoint discovery" {
			status = "Connecting to Apple"
		}
		if s.warmup {
			status = "Warming up " + strings.ToLower(s.phase)
		}
		if s.window > 0 {
			status += fmt.Sprintf(" · %.0fs / %s", s.current.elapsed.Seconds(), s.window)
			if s.current.bytes > 0 && !s.warmup {
				v.text(2, 10, plain, s.current.String())
			}
		}
		v.text(2, 9, quiet, status)
		keys = "Esc cancel and return   q quit"
	} else if s.err != nil {
		v.text(2, 9, bad, s.err.Error())
	}
	if a.running() {
		v.text(2, 11, quiet, "Origin scan running in the background")
	}
	v.text(2, v.h-1, accent, keys)
}

func (a *app) drawScan(v canvas) {
	s := a.scan
	now := time.Now()
	if !s.ended.IsZero() {
		now = s.ended
	}
	elapsed := now.Sub(s.started)
	goal := "all"
	fraction := 0.0
	if s.goal > 0 {
		goal = number(s.goal)
		fraction = float64(s.completed) / float64(s.goal)
	}
	progress := fmt.Sprintf("%s / %s", number(s.completed), goal)
	if s.goal > 0 {
		progress += fmt.Sprintf("   %.1f%%", 100*fraction)
	}
	v.text(2, 2, strong, progress)
	clock := elapsed.Round(time.Second).String()
	v.text(v.w-2-len(clock), 2, quiet, clock)
	width := v.w - 4
	filled := min(width, int(float64(width)*fraction))
	v.text(2, 3, accent, strings.Repeat("━", filled))
	v.text(2+filled, 3, quiet, strings.Repeat("─", width-filled))
	rate := float64(s.completed) / max(0.001, elapsed.Seconds())
	v.text(2, 4, plain, fmt.Sprintf("Processes %d/%d   Timeout %ds   %.1f/s avg", len(s.workers), s.processes, s.timeout, rate))
	dnsLine := "DNS " + s.dnsLabel() + "   r resolver status"
	if len(s.resolverStats) > 0 {
		var refused, policy, timeouts int
		for _, r := range s.resolverStats {
			refused += r.refused
			policy += r.policy
			timeouts += r.timeouts
		}
		dnsLine = fmt.Sprintf("DNS %s  Ref %d  EDE %d  TO %d  r details", s.dnsLabel(), refused, policy, timeouts)
	}
	v.text(2, 5, plain, dnsLine)

	n := min(len(s.history), v.w-28)
	sec := int(elapsed / time.Second)
	series := make([]bucket, n)
	peak := 0
	for i := range series {
		t := sec - n + 1 + i
		if t < 0 {
			continue
		}
		b := s.history[t%len(s.history)]
		if b.second == t {
			series[i] = b
			peak = max(peak, b.reached, b.failed)
		}
	}
	v.text(2, 6, quiet, fmt.Sprintf("CHECKS / SECOND   %ds window · peak %d/s", n, peak))
	v.text(2, 7, good, s.successLabel())
	v.text(2, 8, bad, "Failed")
	levels := []rune("·▁▂▃▄▅▆▇█")
	spark := func(value int) string {
		if value == 0 {
			return string(levels[0])
		}
		return string(levels[1+(value*7/max(1, peak))])
	}
	for i, b := range series {
		v.text(13+i, 7, good, spark(b.reached))
		v.text(13+i, 8, bad, spark(b.failed))
	}
	v.text(15+n, 7, strong, number(s.succeeded))
	v.text(15+n, 8, strong, number(s.failed()))

	right := v.w / 2
	v.text(right, 10, quiet, "FAILURE REASONS")
	bar := func(x, y, space int, label string, count, total int, style tcell.Style) {
		num := number(count)
		v.text(x, y, plain, label)
		v.text(x+space-len(num), y, style, num)
		available := space - 13 - len(num)
		if count > 0 && available > 0 {
			lenBar := max(1, available*count/max(total, 1))
			v.text(x+11, y, style, strings.Repeat("▰", lenBar))
		}
	}
	reasons := []string{"dns", "connection", "tls", "timeout", "protocol", "input"}
	if s.dnsOnly {
		v.text(2, 10, quiet, "DNS A LOOKUPS")
		bar(2, 11, right-5, "Resolved", s.succeeded, s.completed, good)
		bar(2, 12, right-5, "Failed", s.failed(), s.completed, bad)
		bar(2, 13, right-5, "Skipped", s.skipped, s.completed, quiet)
		reasons = topCounts(s.reasons)
	} else {
		v.text(2, 10, quiet, "HTTP STATUS")
		if len(s.statuses) == 0 {
			v.text(2, 11, quiet, "Awaiting response")
		}
		for i, code := range topCounts(s.statuses) {
			bar(2, 11+i, right-5, strconv.Itoa(code), s.statuses[code], s.succeeded, good)
		}
	}
	for i, reason := range reasons {
		bar(right, 11+i, v.w-right-3, reason, s.reasons[reason], s.failed(), bad)
	}
	if v.h > 21 {
		v.text(2, 18, quiet, "RECENT ORIGINS")
		for i, r := range s.recent {
			y := 19 + i
			if y >= v.h-2 {
				break
			}
			label, style := r.Reason, bad
			switch {
			case r.Resolved:
				label, style = "resolved", good
			case r.Reason == skippedLiteral:
				label, style = "skipped", quiet
			case r.Status > 0:
				label, style = strconv.Itoa(r.Status), good
			}
			ms := fmt.Sprintf("%d ms", r.Millis)
			v.text(2, y, style, label)
			origin := []rune(r.origin)
			room := v.w - 17 - len(ms)
			if len(origin) > room {
				origin = append(origin[:room-1], '…')
			}
			v.text(14, y, plain, string(origin))
			v.text(v.w-2-len(ms), y, quiet, ms)
		}
	}
}

func topCounts[K cmp.Ordered](counts map[K]int) []K {
	keys := slices.Collect(maps.Keys(counts))
	slices.SortFunc(keys, func(a, b K) int {
		if counts[a] == counts[b] {
			return cmp.Compare(a, b)
		}
		return cmp.Compare(counts[b], counts[a])
	})
	return keys[:min(len(keys), 6)]
}
