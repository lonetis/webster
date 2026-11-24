package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
)

var presets = []int{1000, 10000, 100000, 1000000, 0}

type app struct {
	settings
	input                       string
	meta                        snapshot
	scan, lastScan              *scan
	edit, entry, message        string
	field                       int
	scroll, maxScroll, pageRows int
	followSelection             bool
	resolverRow                 int
	customPool                  providerPool
	speedOptions                speedOptions
	speed                       *speedProgress
	speedEvents                 <-chan speedProgress
	cancelSpeed                 context.CancelFunc
}

func dashboard(ctx context.Context, out io.Writer, input string, meta snapshot, config settings, speed speedOptions) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	a := &app{settings: config, input: input, meta: meta, speedOptions: speed}
	if len(config.pool) > 1 && !slices.Equal(config.pool, providerNames) {
		a.customPool = config.pool
	}
	func() { defer screen.Fini(); a.loop(ctx, screen) }()
	s := a.scan
	if s == nil {
		s = a.lastScan
	}
	if s != nil {
		fmt.Fprintf(out, "%d completed · %s\n", s.completed, s.outcomeTotals())
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
	defer a.closeSpeed()
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
		case update := <-a.speedEvents:
			a.speed = &update
			if update.phase == "" {
				a.speedEvents = nil
				a.cancelSpeed()
				a.cancelSpeed = nil
			}
		case <-ticker.C:
			if a.running() {
				a.scan.fill()
			} else {
				redraw = false
			}
		}
		if a.running() && a.scan.done() {
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

func (a *app) startSpeed(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	events := make(chan speedProgress, 1)
	a.speed = &speedProgress{phase: "endpoint discovery"}
	a.speedEvents, a.cancelSpeed = events, cancel
	go func() {
		measureSpeed(runCtx, speedTestURL, a.speedOptions, func(s speedProgress) {
			select {
			case events <- s:
			case <-runCtx.Done():
			}
		})
	}()
}

func (a *app) closeSpeed() {
	if a.cancelSpeed != nil {
		a.cancelSpeed()
	}
	a.speed, a.speedEvents, a.cancelSpeed = nil, nil, nil
}

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
	switch e.Key() {
	case tcell.KeyEscape:
		if a.speed != nil {
			a.closeSpeed()
			a.scroll = 0
			return false
		}
		return true
	case tcell.KeyPgDn:
		a.scroll = min(a.maxScroll, a.scroll+max(1, a.pageRows-1))
		return false
	case tcell.KeyPgUp:
		a.scroll = max(0, a.scroll-max(1, a.pageRows-1))
		return false
	case tcell.KeyHome:
		a.scroll = 0
		return false
	case tcell.KeyEnd:
		a.scroll = a.maxScroll
		return false
	}
	if ch == 's' {
		if a.speedEvents == nil {
			a.startSpeed(ctx)
			a.scroll = 0
		}
		return false
	}
	if e.Key() == tcell.KeyUp || e.Key() == tcell.KeyDown {
		step := 1
		if e.Key() == tcell.KeyUp {
			step = -1
		}
		if a.scan == nil {
			a.field = max(0, min(a.lastField(), a.field+step))
		} else {
			a.resolverRow = max(0, min(len(resolverRows)-1, a.resolverRow+step))
		}
		a.followSelection = true
		return false
	}
	if a.scan != nil && !a.running() {
		if e.Key() == tcell.KeyEnter {
			a.lastScan = a.scan
			a.scan, a.message, a.scroll = nil, "", 0
			a.field = 0
		}
		return false
	}
	a.message = ""
	switch {
	case a.running() && ch == 'x':
		a.scan.finish()
		return false
	case a.scan == nil && (e.Key() == tcell.KeyLeft || e.Key() == tcell.KeyRight):
		step := 1
		if e.Key() == tcell.KeyLeft {
			step = -1
		}
		a.adjust(a.field, step)
	case a.scan == nil && ch == 'm':
		a.adjust(fieldCheck, 1)
	case ch == 'd':
		a.adjust(fieldDNS, 1)
	case ch == 'v' && a.mode == "recursive":
		a.adjust(fieldProvider, 1)
	case ch == 'p':
		a.edit, a.entry = "Processes", ""
	case ch == 't':
		a.edit, a.entry = "Timeout (s)", ""
	case ch == '+' || ch == '=':
		a.adjust(fieldProcesses, 1)
	case ch == '-':
		a.adjust(fieldProcesses, -1)
	case ch == ']':
		a.adjust(fieldTimeout, 1)
	case ch == '[':
		a.adjust(fieldTimeout, -1)
	case a.scan == nil && ch == 'n':
		a.edit, a.entry = "Targets", ""
	case a.scan == nil && e.Key() == tcell.KeyEnter:
		s, err := startScan(ctx, a.input, a.settings, a.meta.Origins)
		if err != nil {
			a.message = err.Error()
			return false
		}
		a.scan, a.lastScan, a.scroll = s, nil, 0
		a.resolverRow = 0
		if a.mode == "iterative" {
			a.resolverRow = len(resolverRows) - 1
		} else if a.mode == "recursive" {
			a.resolverRow = max(0, slices.Index(resolverRows, providers[a.pool[0]]))
		}
	}
	a.applySettings()
	return false
}

const (
	fieldTargets = iota
	fieldProcesses
	fieldTimeout
	fieldCheck
	fieldDNS
	fieldProvider
)

func (a *app) lastField() int {
	if a.mode == "recursive" {
		return fieldProvider
	}
	return fieldDNS
}

func (a *app) adjust(field, step int) {
	switch field {
	case fieldTargets:
		i := slices.Index(presets, a.targets)
		if i < 0 {
			// A custom count falls between adjacent presets; all is last.
			i, _ = slices.BinarySearch(presets[:len(presets)-1], a.targets)
			if step > 0 {
				i--
			}
		}
		a.targets = presets[(i+step+len(presets))%len(presets)]
	case fieldProcesses:
		if step < 0 || a.processes < math.MaxInt {
			a.processes = max(1, a.processes+step)
		}
	case fieldTimeout:
		if step < 0 || int64(a.timeout) < maxTimeoutSeconds && a.timeout < math.MaxInt {
			a.timeout = max(1, a.timeout+step)
		}
	case fieldCheck:
		a.dnsOnly = !a.dnsOnly
	case fieldDNS:
		i := slices.Index(dnsModes, a.mode)
		a.mode = dnsModes[(i+step+len(dnsModes))%len(dnsModes)]
		a.field = min(a.field, a.lastField())
	case fieldProvider:
		var pools []providerPool
		for _, name := range providerNames {
			pools = append(pools, providerPool{name})
		}
		pools = append(pools, providerNames)
		if len(a.customPool) > 0 {
			pools = append(pools, a.customPool)
		}
		i := slices.IndexFunc(pools, func(p providerPool) bool { return slices.Equal(a.pool, p) })
		a.pool = pools[(i+step+len(pools))%len(pools)]
	}
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
