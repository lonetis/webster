package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
)

var presets = []int{1000, 10000, 100000, 1000000, 0}

type app struct {
	settings
	input                string
	meta                 snapshot
	scan                 *scan
	edit, entry, message string
	showResolvers        bool
	resolverRow          int
	customPool           providerPool
}

func dashboard(ctx context.Context, input string, meta snapshot, config settings) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	a := &app{settings: config, input: input, meta: meta}
	if len(config.pool) > 1 && len(config.pool) < len(providerNames) {
		a.customPool = config.pool
	}
	func() { defer screen.Fini(); a.loop(ctx, screen) }()
	if s := a.scan; s != nil {
		fmt.Printf("%d completed · %s\n", s.completed, s.outcomeTotals())
		return s.err
	}
	return nil
}

func (a *app) loop(ctx context.Context, screen tcell.Screen) {
	events := make(chan tcell.Event, 8)
	quit := make(chan struct{})
	defer close(quit)
	go screen.ChannelEvents(events, quit)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		if a.scan != nil {
			a.scan.finish()
		}
	}()
	a.draw(screen)
	for {
		var reports <-chan completion
		if a.running() {
			reports = a.scan.reports
		}
		redraw := true
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			switch e := event.(type) {
			case *tcell.EventKey:
				if a.key(ctx, e) {
					return
				}
			case *tcell.EventResize:
				screen.Sync()
			}
		case report := <-reports:
			a.scan.accept(report)
			redraw = false
		case <-ticker.C:
			if a.running() {
				a.scan.fill()
			} else {
				redraw = false
			}
		}
		if a.running() && (a.scan.err != nil || (a.scan.eof && len(a.scan.workers) == 0)) {
			a.scan.finish()
			a.edit = ""
			redraw = true
		}
		if redraw {
			a.draw(screen)
		}
	}
}

func (a *app) running() bool { return a.scan != nil && a.scan.ended.IsZero() }

func (a *app) key(ctx context.Context, e *tcell.EventKey) bool {
	ch := e.Rune()
	if e.Key() == tcell.KeyCtrlC || ch == 'q' || ch == 'Q' {
		return true
	}
	if a.edit != "" {
		switch e.Key() {
		case tcell.KeyEscape:
			a.edit, a.message = "", ""
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(a.entry) > 0 {
				a.entry = a.entry[:len(a.entry)-1]
			}
		case tcell.KeyEnter:
			n, err := strconv.Atoi(a.entry)
			if err != nil || n < 1 {
				a.message = "Enter a positive whole number"
			} else {
				switch a.edit {
				case "Targets":
					a.targets = n
				case "Processes":
					a.processes = n
				case "Timeout (s)":
					a.timeout = n
				}
				a.edit, a.message = "", ""
				a.applySettings()
			}
		case tcell.KeyRune:
			if ch >= '0' && ch <= '9' && len(a.entry) < 9 {
				a.entry += string(ch)
				a.message = ""
			}
		}
		return false
	}
	if e.Key() == tcell.KeyEscape {
		return true
	}
	if a.scan != nil {
		switch {
		case ch == 'r':
			a.showResolvers = !a.showResolvers
			return false
		case a.showResolvers && e.Key() == tcell.KeyDown:
			a.resolverRow = min(a.resolverRow+1, len(resolverRows)-1)
			return false
		case a.showResolvers && e.Key() == tcell.KeyUp:
			a.resolverRow = max(0, a.resolverRow-1)
			return false
		}
	}
	if a.scan != nil && !a.running() {
		return false
	}
	a.message = ""
	switch {
	case a.scan == nil && ch == 'm':
		a.dnsOnly = !a.dnsOnly
	case ch == 'd':
		i := slices.Index(dnsModes, a.mode)
		a.mode = dnsModes[(i+1)%len(dnsModes)]
	case ch == 'v' && a.mode == "recursive":
		switch {
		case len(a.pool) == 1:
			i := slices.Index(providerNames, a.pool[0]) + 1
			if i == len(providerNames) {
				a.pool = providerNames
			} else {
				a.pool = providerPool{providerNames[i]}
			}
		case len(a.pool) == len(providerNames) && len(a.customPool) > 0:
			a.pool = a.customPool
		default:
			a.pool = providerPool{providerNames[0]}
		}
	case ch == 'p':
		a.edit, a.entry = "Processes", ""
	case ch == 't':
		a.edit, a.entry = "Timeout (s)", ""
	case ch == '+' || ch == '=':
		a.processes++
	case ch == '-':
		a.processes = max(1, a.processes-1)
	case ch == ']':
		a.timeout++
	case ch == '[':
		a.timeout = max(1, a.timeout-1)
	case a.scan == nil && ch == 'n':
		a.edit, a.entry = "Targets", ""
	case a.scan == nil && (e.Key() == tcell.KeyLeft || e.Key() == tcell.KeyRight):
		i := slices.Index(presets, a.targets)
		if e.Key() == tcell.KeyLeft {
			i += len(presets) - 1
		} else {
			i++
		}
		a.targets = presets[i%len(presets)]
	case a.scan == nil && e.Key() == tcell.KeyEnter:
		s, err := startScan(ctx, a.input, a.settings, a.meta.Origins)
		if err != nil {
			a.message = err.Error()
			return false
		}
		a.scan = s
	}
	a.applySettings()
	return false
}

func (a *app) applySettings() {
	if a.running() {
		if !slices.Equal(a.scan.pool, a.pool) {
			a.scan.poolIndex = 0
		}
		a.scan.settings = a.settings
		a.scan.fill()
	}
}
