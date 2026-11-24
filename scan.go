package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type settings struct {
	targets, processes, timeout, parallel int
	schedule                              string
	mode                                  string
	pool                                  providerPool
	dnsOnly                               bool
}

func (c settings) checkLabel() string {
	if c.dnsOnly {
		return "DNS only (A)"
	}
	return "HTTP HEAD"
}

func targetLabel(n int) string {
	if n == 0 {
		return "all"
	}
	return number(n)
}

const skippedLiteral = "IP literal"
const originRateWindow = 5 * time.Second

func (c settings) successLabel() string {
	if c.dnsOnly {
		return "Resolved"
	}
	return "Reachable"
}

type worker struct {
	retired  chan struct{}
	work     chan workBlock
	controls chan probeOptions
	pending  int
	draining bool
}

type completion struct {
	worker *worker
	events []probeEvent
	err    error
}

type bucket struct{ second, reached, failed int }
type completionSample struct {
	at     time.Duration
	before int // cumulative completions before this report
}
type observation struct {
	origin string
	result
}

type scan struct {
	settings
	goal, issued, completed, succeeded, skipped int
	poolIndex, launched, draining               int
	eof, stopped                                bool
	started, ended                              time.Time
	statuses                                    map[int]int
	reasons                                     map[string]int
	history                                     [60]bucket
	completionTimes                             []completionSample
	resolverStats                               map[string]*resolverStats
	recent                                      []observation

	executable string
	ctx        context.Context
	cancel     context.CancelFunc
	workers    map[*worker]struct{}
	reports    chan completion
	children   sync.WaitGroup
	origins    []string
	err        error
}

func (s *scan) failed() int { return s.completed - s.succeeded - s.skipped }

func (s *scan) done() bool { return s.err != nil || (s.eof && len(s.workers) == 0) }

func (s *scan) elapsed() time.Duration {
	if !s.ended.IsZero() {
		return s.ended.Sub(s.started)
	}
	return time.Since(s.started)
}

func (s *scan) outcomePercent(count int) string {
	checked := s.completed - s.skipped
	if checked == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(count)/float64(checked))
}

func (s *scan) outcomeTotals() string {
	failed := s.failed()
	totals := fmt.Sprintf("%d %s (%s) · %d failed (%s)", s.succeeded,
		strings.ToLower(s.successLabel()), s.outcomePercent(s.succeeded), failed, s.outcomePercent(failed))
	if s.dnsOnly {
		totals += fmt.Sprintf(" · %d skipped", s.skipped)
	}
	return totals
}

func startScan(ctx context.Context, input string, config settings, knownCount int) (*scan, error) {
	origins, err := loadOrigins(ctx, input, knownCount)
	if err != nil {
		return nil, err
	}
	return newScan(ctx, origins, config)
}

func newScan(parent context.Context, origins []string, config settings) (*scan, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	goal := len(origins)
	if config.targets > 0 {
		goal = min(goal, config.targets)
	}
	config.parallel = max(1, config.parallel)
	ctx, cancel := context.WithCancel(parent)
	return &scan{
		settings: config, origins: origins[:goal], goal: goal, eof: goal == 0, started: time.Now(),
		statuses: make(map[int]int), reasons: make(map[string]int), resolverStats: make(map[string]*resolverStats),
		executable: executable, ctx: ctx, cancel: cancel,
		workers: make(map[*worker]struct{}), reports: make(chan completion),
	}, nil
}

func (s *scan) options() probeOptions {
	o := probeOptions{Timeout: s.timeout, DNSOnly: s.dnsOnly}
	switch s.mode {
	case "iterative":
		o.Resolvers = []string{"iterative"}
	case "recursive":
		for _, p := range s.pool {
			o.Resolvers = append(o.Resolvers, providers[p])
		}
		if s.schedule != "fixed" {
			o.Offset = (s.poolIndex - s.issued%len(s.pool) + len(s.pool)) % len(s.pool)
		}
	}
	return o
}

func (s *scan) advance(w *worker, count int) {
	w.pending += count
	s.issued += count
	s.eof = s.issued == s.goal
	if s.mode == "recursive" {
		s.poolIndex = (s.poolIndex + count%len(s.pool)) % len(s.pool)
	}
}

// Each fixed partition is contiguous, differs in size by at most one row,
// and belongs to the same process until it finishes.
func shardSize(total, parts, index int) int {
	n := total / parts
	if index < total%parts {
		n++
	}
	return n
}

func (s *scan) fill() {
	for i := 0; i < 8 && s.issued < s.goal && len(s.workers) < s.processes; i++ {
		fixed := s.schedule == "fixed"
		count := min(s.parallel, 1+(s.goal-s.issued-1)/(s.processes-len(s.workers)))
		if fixed {
			count = shardSize(s.goal, min(s.processes, s.goal), s.launched)
		}
		w := &worker{retired: make(chan struct{})}
		init := workerInit{Parallel: min(s.parallel, s.goal), Fixed: fixed, Start: s.issued, Options: s.options()}
		rows := s.origins[s.issued : s.issued+count]
		if fixed {
			w.controls = make(chan probeOptions, 1)
			init.Count = count
			init.Parallel = min(init.Parallel, count)
		} else {
			w.work = make(chan workBlock, 1)
			w.work <- workBlock{First: s.issued, Lanes: count, Origins: rows, Options: init.Options}
			rows = nil
		}
		s.workers[w] = struct{}{}
		s.launched++
		s.advance(w, count)
		s.children.Add(1)
		go func() { defer s.children.Done(); s.runWorker(w, init, rows) }()
	}
}

// Refill freed slots without blocking the event loop on a child's pipe.
// Pending includes the bounded writes that have not reached the child yet.
func (s *scan) refill(w *worker) {
	active := len(s.workers) - s.draining
	if s.schedule != "fixed" {
		switch {
		case !w.draining && active > s.processes:
			w.draining = true
			s.draining++
		case w.draining && active < s.processes:
			w.draining = false
			s.draining--
		}
	}
	if s.schedule != "fixed" && !w.draining && s.issued < s.goal {
		n := min(s.parallel-w.pending, s.goal-s.issued)
		if n > 0 {
			select {
			case w.work <- workBlock{First: s.issued, Lanes: w.pending + n, Origins: s.origins[s.issued : s.issued+n], Options: s.options()}:
				s.advance(w, n)
			default:
			}
		}
	}
	if w.pending == 0 {
		if w.draining {
			s.draining--
		}
		close(w.retired)
		delete(s.workers, w)
	}
}

func (s *scan) updateSettings(c settings) {
	c.parallel, c.schedule = s.parallel, s.schedule
	if s.schedule == "fixed" {
		c.processes = s.processes
		if s.timeout != c.timeout || s.mode != c.mode || !slices.Equal(s.pool, c.pool) {
			s.settings = c
			for w := range s.workers {
				// Only the latest settings matter for unstarted probes.
				select {
				case <-w.controls:
				default:
				}
				w.controls <- s.options()
			}
		}
	} else {
		if !slices.Equal(s.pool, c.pool) {
			s.poolIndex = 0
		}
		s.settings = c
	}
	s.fill()
}

func (s *scan) accept(c completion) {
	if _, active := s.workers[c.worker]; !active {
		return
	}
	if c.err != nil {
		s.err = fmt.Errorf("worker process: %w", c.err)
		return
	}
	elapsed := time.Since(s.started)
	for _, e := range c.events {
		if e.DNS != nil {
			stats := s.resolverStats[e.Resolver]
			if stats == nil {
				stats = &resolverStats{}
				s.resolverStats[e.Resolver] = stats
			}
			stats.record(*e.DNS, int(elapsed/time.Second))
			continue
		}
		if e.Index < 0 || e.Index >= s.goal || c.worker.pending == 0 {
			s.err = errors.New("unexpected origin result from worker")
			return
		}
		s.recordResult(e, elapsed)
		c.worker.pending--
	}
	s.refill(c.worker)
}

func (s *scan) recordResult(e probeEvent, elapsed time.Duration) {
	r := e.Result
	b := s.recordCompletion(elapsed)
	switch {
	case r.Resolved || r.Status > 0:
		s.succeeded++
		if r.Status > 0 {
			s.statuses[r.Status]++
		}
		b.reached++
	case r.Reason == skippedLiteral:
		s.skipped++
	default:
		s.reasons[r.Reason]++
		b.failed++
	}
	if len(s.recent) == 5 {
		s.recent = s.recent[:4]
	}
	s.recent = append(s.recent, observation{})
	copy(s.recent[1:], s.recent)
	s.recent[0] = observation{s.origins[e.Index], r}
}

func (s *scan) trimRateWindow(elapsed time.Duration) {
	cutoff := elapsed - originRateWindow
	for len(s.completionTimes) > 0 && s.completionTimes[0].at <= cutoff {
		s.completionTimes = s.completionTimes[1:]
	}
}

func (s *scan) recordCompletion(elapsed time.Duration) *bucket {
	s.trimRateWindow(elapsed)
	// Results received in one batch have the same timestamp. Keep one
	// cumulative sample for that instant while retaining an exact 5s window.
	if n := len(s.completionTimes); n == 0 || s.completionTimes[n-1].at != elapsed {
		s.completionTimes = append(s.completionTimes, completionSample{elapsed, s.completed})
	}
	s.completed++
	sec := int(elapsed / time.Second)
	b := &s.history[sec%len(s.history)]
	if b.second != sec {
		*b = bucket{second: sec}
	}
	return b
}

func (s *scan) originRates(elapsed time.Duration) (average, recent float64) {
	if elapsed <= 0 {
		return 0, 0
	}
	s.trimRateWindow(elapsed)
	if len(s.completionTimes) > 0 {
		recent = float64(s.completed-s.completionTimes[0].before) / min(elapsed, originRateWindow).Seconds()
	}
	return float64(s.completed) / elapsed.Seconds(), recent
}

func (s *scan) rateLabels(elapsed time.Duration) []string {
	average, recent := s.originRates(elapsed)
	return []string{fmt.Sprintf("%.1f origins/s avg", average), fmt.Sprintf("%.1f origins/s (last 5s)", recent)}
}

func (s *scan) finish() {
	if !s.ended.IsZero() {
		return
	}
	s.stopped = !s.done()
	s.ended = time.Now()
	s.cancel()
	s.children.Wait()
	clear(s.workers)
}
