package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestUnifiedDashboard(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	content := func() string {
		cells, w, _ := screen.GetContents()
		var lines strings.Builder
		for i, c := range cells {
			if i > 0 && i%w == 0 {
				lines.WriteByte('\n')
			}
			lines.WriteString(string(c.Runes))
		}
		return lines.String()
	}
	press := func(a *app, key tcell.Key, ch rune) { a.key(t.Context(), tcell.NewEventKey(key, ch, tcell.ModNone)) }
	cfg := settings{targets: 3, processes: 2, timeout: 5, mode: "recursive", pool: providerPool{"cloudflare"}}
	start := time.Now().Add(-time.Minute)
	s := &scan{settings: cfg, started: start, ended: start.Add(8 * time.Second), goal: 3,
		succeeded: 2, statuses: map[int]int{200: 2}, reasons: map[string]int{"dns": 1},
		recent:        []observation{{"https://origin.example", result{Status: 200}}},
		resolverStats: map[string]*resolverStats{providers["google"]: {attempts: 7, replies: 3, finished: 7}}}
	s.recordCompletion(time.Second).reached++
	s.recordCompletion(2*time.Second).failed++
	s.recordCompletion(7*time.Second).reached++
	a := &app{scan: s, settings: cfg}
	press(a, tcell.KeyDown, 0)
	for _, width := range []int{140, 80} {
		screen.SetSize(width, 28)
		a.draw(screen)
		got := content()
		for _, want := range []string{"0.4 origins/s avg", "0.2 origins/s (last 5s)", "2 (66.7%)", "1 (33.3%)", "HTTP STATUS", "DNS RESOLVERS", "google · 8.8.8.8:53", "Lookups 7", "RECENT ORIGINS", "https://origin.example", "Enter new run"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%d cols: missing %q\n%s", width, want, got)
			}
		}
	}
	screen.SetSize(52, 19)
	a.draw(screen)
	if !strings.Contains(content(), "0.2 origins/s (last 5s)") || !strings.Contains(content(), "1 (33.3%)") {
		t.Fatalf("clipped scan metrics on narrow terminal\n%s", content())
	}
	if !strings.Contains(content(), "PgUp/PgDn") || !strings.Contains(content(), "DNS RESOLVERS") {
		t.Fatalf("missing small-terminal navigation\n%s", content())
	}
	press(a, tcell.KeyEnd, 0)
	a.draw(screen)
	if !strings.Contains(content(), "RECENT ORIGINS") || !strings.Contains(content(), "HTTP STATUS") {
		t.Fatalf("cannot scroll to origins\n%s", content())
	}

	press(a, tcell.KeyEnter, 0)
	press(a, tcell.KeyDown, 0)
	press(a, tcell.KeyRight, 0)
	if a.scan != nil || a.processes != 3 || a.scroll != 0 {
		t.Fatalf("new run or field navigation failed")
	}
	a.speed = &speedProgress{latency: 2 * time.Millisecond}
	screen.SetSize(120, 32)
	a.draw(screen)
	got := content()
	if !strings.Contains(got, "INTERNET SPEED") || !strings.Contains(got, "MEASUREMENT SETTINGS") {
		t.Fatalf("inline speed results hid setup\n%s", got)
	}
}
