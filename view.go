package main

import (
	"fmt"
	"path/filepath"
	"sort"
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
	v.text(12, 0, quiet, "Origin connection tester")
	v.text(max(2, w-2-len(state)), 0, strong, state)
	if w < 52 || h < 19 {
		v.text(1, 3, plain, "Resize to 52 × 19 for the dashboard")
		if s := a.scan; s != nil {
			v.text(1, 5, plain, fmt.Sprintf("%d reachable · %d failed", s.reachable, s.completed-s.reachable))
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
		for i, row := range [][3]string{
			{"Targets", targets, "← → presets   n exact"},
			{"Processes", number(a.processes), "+ / − adjust   p set"},
			{"Timeout", fmt.Sprintf("%d s", a.timeout), "[ / ] adjust   t set"},
			{"DNS", a.mode, "d cycle"},
		} {
			y := 9 + i*2
			v.text(2, y, plain, row[0])
			v.text(15, y, accent, row[1])
			v.text(29, y, quiet, row[2])
		}
		if a.mode == "recursive" {
			v.text(2, 16, plain, "v Providers  "+a.pool.label())
		} else if h > 19 {
			v.text(2, 17, quiet, "One HEAD request per origin · first response")
		}
		keys = "Enter start run    q quit"
	} else if a.showResolvers {
		a.drawResolvers(v)
		keys = "↑/↓ endpoint   r overview   q quit"
		if a.running() {
			keys = "↑/↓ row   d mode   v provider   r overview   q quit"
		}
	} else {
		a.drawScan(v)
		keys = "+/- processes  [ ] timeout  d DNS  v provider  q quit"
		if !a.running() {
			keys = "r resolvers  q quit"
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
	v.text(2, 7, good, "Reachable")
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
	v.text(15+n, 7, strong, number(s.reachable))
	v.text(15+n, 8, strong, number(s.completed-s.reachable))

	right := v.w / 2
	v.text(2, 10, quiet, "HTTP STATUS")
	v.text(right, 10, quiet, "FAILURE REASONS")
	codes := make([]int, 0, len(s.statuses))
	for code := range s.statuses {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool {
		a, b := s.statuses[codes[i]], s.statuses[codes[j]]
		if a == b {
			return codes[i] < codes[j]
		}
		return a > b
	})
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
	if len(codes) == 0 {
		v.text(2, 11, quiet, "Awaiting response")
	}
	for i, code := range codes[:min(len(codes), 6)] {
		bar(2, 11+i, right-5, strconv.Itoa(code), s.statuses[code], s.reachable, good)
	}
	for i, reason := range []string{"dns", "connection", "tls", "timeout", "protocol", "input"} {
		bar(right, 11+i, v.w-right-3, reason, s.reasons[reason], s.completed-s.reachable, bad)
	}
	if v.h > 21 {
		v.text(2, 18, quiet, "RECENT ORIGINS")
		for i, r := range s.recent {
			y := 19 + i
			if y >= v.h-2 {
				break
			}
			label, style := r.Reason, bad
			if r.Status > 0 {
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
