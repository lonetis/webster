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
	"github.com/rivo/uniseg"
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

// Each column clips its own text. Scrolling moves the content origin while
// the header and key hints stay outside the vertical clipping bounds.
type canvas struct {
	tcell.Screen
	x, y, w     int
	top, bottom int
}

func (v canvas) at(x, y, width int) canvas {
	v.x, v.y, v.w = v.x+x, v.y+y, min(width, v.w-x)
	return v
}

func (v canvas) text(x, y int, style tcell.Style, s string) {
	if x < 0 || x >= v.w || v.y+y < v.top || v.y+y >= v.bottom {
		return
	}
	room := v.w - x
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > room && uniseg.StringWidth(s) > room {
		g := uniseg.NewGraphemes(s)
		var clipped strings.Builder
		for g.Next() && room > g.Width() {
			clipped.WriteString(g.Str())
			room -= g.Width()
		}
		s = clipped.String() + "…"
	}
	v.PutStrStyled(v.x+x, v.y+y, s, style)
}

func (v canvas) heading(y int, title string) {
	v.text(0, y, accent, title)
	start := uniseg.StringWidth(title) + 1
	if start < v.w {
		v.text(start, y, quiet, strings.Repeat("─", v.w-start))
	}
}

func wrapItems(items []string, gap string, width int) []string {
	var lines []string
	line := ""
	for _, item := range items {
		switch {
		case line == "":
			line = item
		case uniseg.StringWidth(line+gap+item) <= width:
			line += gap + item
		default:
			lines = append(lines, line)
			line = item
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func (a *app) draw(screen tcell.Screen) {
	w, h := screen.Size()
	screen.Clear()
	defer screen.Show()
	v := canvas{Screen: screen, x: 2, w: w - 4, bottom: h}
	if w < 44 || h < 12 {
		v.text(0, 0, accent, "WEBSTER")
		v.text(0, 2, plain, "Resize to at least 44 × 12")
		v.text(0, h-1, quiet, "q quit")
		return
	}
	state, style := "SETUP", quiet
	if a.scan != nil {
		state, style = "RUNNING", good
		if a.scan.err != nil || a.scan.stopped {
			state, style = "STOPPED", bad
		} else if !a.running() {
			state, style = "COMPLETE", good
		}
	}
	v.text(0, 0, accent, "WEBSTER")
	v.text(10, 0, strong, a.checkLabel())
	v.text(v.w-len(state), 0, style, state)
	v.text(0, 1, quiet, strings.Repeat("─", v.w))

	keys := []string{"Enter start", "↑↓ select", "←→ change"}
	if a.running() {
		keys = []string{"x stop", "+/- workers", "[/] timeout", "p/t set", "d DNS"}
		if a.mode == "recursive" {
			keys = append(keys, "v provider")
		}
	} else if a.scan != nil {
		keys = []string{"Enter new run"}
	}
	if a.speed != nil {
		keys = append(keys, "Esc close speed test")
	}
	if a.speedEvents == nil {
		keys = append(keys, "s speed test")
	}
	keys = append(keys, "q quit")
	if a.edit != "" {
		keys = []string{a.edit + " › " + a.entry + "▏", "Enter apply", "Esc cancel"}
	}
	keyLines := wrapItems(keys, "   ", v.w)
	footer := h - len(keyLines) - 1
	body := viewport(v, footer, a.scroll)
	a.pageRows = footer - 2
	height, selected := a.drawBody(body)
	a.maxScroll = max(0, height-a.pageRows)
	offset := min(a.scroll, a.maxScroll)
	if a.followSelection {
		offset = max(0, min(offset, selected))
		offset = max(offset, selected-a.pageRows+1)
		a.followSelection = false
	}
	if offset != a.scroll {
		a.scroll = offset
		// The layout height and selection can change on resize or mode switch.
		// Clear the viewport before painting it at the newly clamped offset.
		for y := body.top; y < body.bottom; y++ {
			v.text(0, y, plain, strings.Repeat(" ", v.w))
		}
		a.drawBody(viewport(v, footer, offset))
	}
	message := a.message
	if a.scan != nil && a.scan.err != nil {
		message = a.scan.err.Error()
	}
	switch {
	case message != "":
		v.text(0, footer, bad, message)
	case a.maxScroll > 0:
		v.text(0, footer, quiet, fmt.Sprintf("PgUp/PgDn scroll  ·  lines %d–%d of %d", a.scroll+1, a.scroll+a.pageRows, height))
	default:
		v.text(0, footer, quiet, strings.Repeat("─", v.w))
	}
	for i, row := range keyLines {
		v.text(0, footer+1+i, strong, row)
	}
}

func viewport(v canvas, footer, offset int) canvas {
	v = v.at(0, 2-offset, v.w)
	v.top, v.bottom = 2, footer
	return v
}

func (a *app) drawBody(v canvas) (height, selected int) {
	y := 0
	if a.speed != nil {
		y = a.drawSpeed(v) + 1
	}
	main := v.at(0, y, v.w)
	if a.scan == nil {
		height, selected = a.drawSetup(main)
	} else {
		height, selected = a.drawScan(main)
	}
	return y + height, y + selected
}

func (a *app) drawSetup(v canvas) (int, int) {
	source := filepath.Base(a.input)
	if date, err := time.Parse("200601", a.meta.Month); err == nil {
		source = fmt.Sprintf("CrUX · %s · %s origins", date.Format("January 2006"), number(a.meta.Origins))
	}
	v.heading(0, "INPUT")
	v.text(0, 1, strong, source)
	v.heading(3, "MEASUREMENT SETTINGS")
	fields := [][3]string{
		{"Targets", targetLabel(a.targets), "n exact count"},
		{"Processes", number(a.processes), "p set   +/- adjust"},
		{"Timeout", fmt.Sprintf("%d s", a.timeout), "t set   [/] adjust"},
		{"Check", a.checkLabel(), "m switch"},
		{"DNS", a.mode, "d cycle"},
		{"Providers", a.pool.label(), "v cycle"},
	}
	spacing := 1
	if a.pageRows >= 17 {
		spacing = 2
	}
	selected := 4 + a.field*spacing
	for i, row := range fields[:a.lastField()+1] {
		y := 4 + i*spacing
		style := plain
		if i == a.field {
			style = strong
			v.text(0, y, accent, "›")
		}
		v.text(2, y, style, row[0])
		hints := max(24, min(v.w-15, 50))
		v.at(13, 0, hints-14).text(0, y, accent, row[1])
		v.text(hints, y, quiet, row[2])
	}
	return 5 + a.lastField()*spacing, selected
}

func (a *app) drawSpeed(v canvas) int {
	s := a.speed
	v.heading(0, "INTERNET SPEED")
	v.text(0, 1, quiet, fmt.Sprintf("Apple · %d streams · %s per direction", a.speedOptions.streams, a.speedOptions.duration))
	for i, row := range s.rows() {
		v.text(0, 2+i, plain, row[0])
		v.text(15, 2+i, strong, row[1])
	}
	if s.err != nil {
		v.text(0, 5, bad, s.err.Error())
	} else if s.phase != "" {
		status := "Measuring " + strings.ToLower(s.phase)
		if s.phase == "endpoint discovery" {
			status = "Connecting to Apple"
		} else if s.warmup {
			status = "Warming up " + strings.ToLower(s.phase)
		}
		if s.window > 0 {
			status += fmt.Sprintf(" · %.0fs / %s", s.current.elapsed.Seconds(), s.window)
		}
		v.text(0, 5, accent, status)
		if s.current.bytes > 0 && !s.warmup {
			v.text(0, 6, plain, s.current.String())
			return 7
		}
	} else {
		v.text(0, 5, good, "Speed test complete")
	}
	return 6
}

func (a *app) drawScan(v canvas) (int, int) {
	s := a.scan
	elapsed := s.elapsed()
	progress := number(s.completed) + " origins checked"
	fraction := 0.0
	if s.goal > 0 {
		fraction = float64(s.completed) / float64(s.goal)
		progress = fmt.Sprintf("%s / %s origins   %.1f%%", number(s.completed), number(s.goal), 100*fraction)
	}
	clock := elapsed.Round(time.Second).String()
	v.at(0, 0, v.w-len(clock)-2).text(0, 0, strong, progress)
	v.text(v.w-len(clock), 0, quiet, clock)
	filled := min(v.w, int(float64(v.w)*fraction))
	v.text(0, 1, good, strings.Repeat("━", filled))
	v.text(filled, 1, quiet, strings.Repeat("─", v.w-filled))
	settings := fmt.Sprintf("Workers %d/%d   Timeout %ds", len(s.workers), s.processes, s.timeout)
	stats := wrapItems(append([]string{settings}, s.rateLabels(elapsed)...), "   ", v.w)
	for i, line := range stats {
		v.text(0, 2+i, plain, line)
	}
	start := 2 + len(stats)
	if v.w >= 72 {
		left := (v.w - 3) * 45 / 100
		l := v.at(0, start, left)
		r := v.at(left+3, start, v.w-left-3)
		s.drawActivity(l, elapsed)
		end := 4 + s.drawOutcomes(l.at(0, 4, l.w))
		height := max(end, a.drawResolvers(r, elapsed))
		for y := range height {
			v.text(left+1, start+y, quiet, "│")
		}
		recent := start + height + 1
		return recent + s.drawRecent(v.at(0, recent, v.w)), start + resolverFirstRow + a.resolverRow
	}
	s.drawActivity(v.at(0, start, v.w), elapsed)
	dnsStart := start + 4
	end := dnsStart + a.drawResolvers(v.at(0, dnsStart, v.w), elapsed)
	end += 1 + s.drawOutcomes(v.at(0, end+1, v.w))
	end += 1 + s.drawRecent(v.at(0, end+1, v.w))
	return end, dnsStart + resolverFirstRow + a.resolverRow
}

func (s *scan) drawActivity(v canvas, elapsed time.Duration) {
	sec := int(elapsed / time.Second)
	value := func(count int) string {
		return fmt.Sprintf("%s (%s)", number(count), s.outcomePercent(count))
	}
	reached, failed := value(s.succeeded), value(s.failed())
	offset := 11 + max(uniseg.StringWidth(reached), uniseg.StringWidth(failed))
	n := max(0, min(len(s.history), v.w-offset))
	series := make([]bucket, n)
	peak := 0
	for i := range series {
		t := sec - n + 1 + i
		if t >= 0 {
			b := s.history[t%len(s.history)]
			if b.second == t {
				series[i] = b
				peak = max(peak, b.reached, b.failed)
			}
		}
	}
	heading := fmt.Sprintf("CHECKS / SECOND · peak %d", peak)
	if v.w >= 40 {
		heading += fmt.Sprintf(" · %ds", n)
	}
	v.heading(0, heading)
	levels := []rune("·▁▂▃▄▅▆▇█")
	for i, row := range []struct {
		label string
		total string
		style tcell.Style
	}{
		{s.successLabel(), reached, good}, {"Failed", failed, bad},
	} {
		v.text(0, i+1, row.style, row.label)
		v.text(10, i+1, strong, row.total)
		var graph strings.Builder
		for _, b := range series {
			value := b.reached
			if i == 1 {
				value = b.failed
			}
			level := 0
			if value > 0 {
				level = 1 + 7*value/peak
			}
			graph.WriteRune(levels[level])
		}
		v.text(offset, i+1, row.style, graph.String())
	}
}

func (s *scan) drawOutcomes(v canvas) int {
	left := v.at(0, 0, (v.w-2)/2)
	right := v.at(left.w+2, 0, v.w-left.w-2)
	line := func(c canvas, y int, label string, count, total int, style tcell.Style) {
		value := number(count)
		c.at(0, 0, c.w-len(value)-1).text(0, y, plain, label)
		start := max(10, len(label)+1)
		if space := c.w - len(value) - 1 - start; space >= 4 && count > 0 {
			c.text(start, y, style, strings.Repeat("━", max(1, space*count/max(1, total))))
		}
		c.text(c.w-len(value), y, style, value)
	}
	statuses, reasons := topCounts(s.statuses), topCounts(s.reasons)
	rows := max(1, len(statuses), len(reasons))
	if s.dnsOnly {
		left.heading(0, "DNS A LOOKUPS")
		line(left, 1, "Resolved", s.succeeded, s.completed, good)
		line(left, 2, "Failed", s.failed(), s.completed, bad)
		line(left, 3, "Skipped", s.skipped, s.completed, quiet)
		rows = max(rows, 3)
	} else {
		left.heading(0, "HTTP STATUS")
		if len(statuses) == 0 {
			left.text(0, 1, quiet, "No responses")
		}
		for i, code := range statuses {
			line(left, i+1, strconv.Itoa(code), s.statuses[code], s.succeeded, good)
		}
	}
	right.heading(0, "FAILURE REASONS")
	if len(reasons) == 0 {
		right.text(0, 1, quiet, "None")
	}
	for i, reason := range reasons {
		line(right, i+1, reason, s.reasons[reason], s.failed(), bad)
	}
	return rows + 1
}

func (s *scan) drawRecent(v canvas) int {
	v.heading(0, "RECENT ORIGINS")
	if len(s.recent) == 0 {
		v.text(0, 1, quiet, "Awaiting first result")
		return 2
	}
	for i, r := range s.recent {
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
		v.at(0, 0, 10).text(0, i+1, style, label)
		v.at(11, 0, v.w-12-len(ms)).text(0, i+1, plain, r.origin)
		v.text(v.w-len(ms), i+1, quiet, ms)
	}
	return len(s.recent) + 1
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
