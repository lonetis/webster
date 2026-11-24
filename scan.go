package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

type settings struct {
	targets, processes, timeout int
	mode                        string
	pool                        providerPool
	dnsOnly                     bool
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

func (c settings) successLabel() string {
	if c.dnsOnly {
		return "Resolved"
	}
	return "Reachable"
}

func (s *scan) nextResolver() string {
	switch s.mode {
	case "recursive":
		name := s.pool[s.poolIndex]
		s.poolIndex = (s.poolIndex + 1) % len(s.pool)
		return providers[name]
	case "iterative":
		return "iterative"
	default:
		return ""
	}
}

type worker chan job
type completion struct {
	worker worker
	job    job
	result result
	err    error
	dns    *dnsEvent
}

type bucket struct{ second, reached, failed int }
type observation struct {
	origin string
	result
}

type scan struct {
	settings
	goal, issued, completed, succeeded, skipped int
	poolIndex                                   int
	eof, stopped                                bool
	started, ended                              time.Time
	statuses                                    map[int]int
	reasons                                     map[string]int
	history                                     [60]bucket
	resolverStats                               map[string]*resolverStats
	recent                                      []observation

	executable string
	ctx        context.Context
	cancel     context.CancelFunc
	workers    map[worker]struct{}
	reports    chan completion
	children   sync.WaitGroup
	source     *os.File
	reader     *csv.Reader
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

func (s *scan) outcomeTotals() string {
	if s.dnsOnly {
		return fmt.Sprintf("%d resolved · %d failed · %d skipped", s.succeeded, s.failed(), s.skipped)
	}
	return fmt.Sprintf("%d reachable · %d failed", s.succeeded, s.failed())
}

func startScan(parent context.Context, input string, config settings, knownCount int) (*scan, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	source, err := os.Open(input)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(source)
	header, err := reader.Read()
	if err != nil || len(header) != 2 || header[0] != "origin" || header[1] != "rank" {
		source.Close()
		return nil, errors.New("input must be a CrUX CSV with header origin,rank")
	}
	ctx, cancel := context.WithCancel(parent)
	goal := config.targets
	if knownCount > 0 && (goal == 0 || goal > knownCount) {
		goal = knownCount
	}
	return &scan{
		settings: config, goal: goal, started: time.Now(),
		statuses: make(map[int]int), reasons: make(map[string]int), resolverStats: make(map[string]*resolverStats),
		executable: executable, ctx: ctx, cancel: cancel,
		workers: make(map[worker]struct{}), reports: make(chan completion),
		source: source, reader: reader,
	}, nil
}

// At most one input row per running process is outstanding.
func (s *scan) next() (job, bool) {
	if s.eof || (s.goal > 0 && s.issued >= s.goal) {
		s.eof = true
		return job{}, false
	}
	row, err := s.reader.Read()
	if err != nil {
		if err != io.EOF {
			s.err = fmt.Errorf("read input: %w", err)
		}
		s.eof = true
		s.goal = s.issued
		return job{}, false
	}
	s.issued++
	return job{Origin: row[0], Timeout: s.timeout, Resolver: s.nextResolver(), DNSOnly: s.dnsOnly}, true
}

func (s *scan) fill() {
	// Grow in small batches so keyboard input stays responsive during a large resize.
	for i := 0; i < 8 && len(s.workers) < s.processes; i++ {
		j, ok := s.next()
		if !ok {
			break
		}
		w := make(worker, 1)
		s.workers[w] = struct{}{}
		w <- j
		s.children.Add(1)
		go func() { defer s.children.Done(); s.runWorker(w) }()
	}
}

func (s *scan) runWorker(w worker) {
	report := func(c completion) {
		select {
		case s.reports <- c:
		case <-s.ctx.Done():
		}
	}
	cmd := exec.CommandContext(s.ctx, s.executable, "_worker")
	in, err := cmd.StdinPipe()
	if err != nil {
		report(completion{worker: w, err: err})
		return
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		report(completion{worker: w, err: err})
		return
	}
	defer out.Close()
	if err = cmd.Start(); err != nil {
		report(completion{worker: w, err: err})
		return
	}
	defer func() { in.Close(); cmd.Wait() }()
	write, read := json.NewEncoder(in), json.NewDecoder(out)
	for {
		select {
		case <-s.ctx.Done():
			return
		case j, ok := <-w:
			if !ok {
				return
			}
			err = write.Encode(j)
			for err == nil {
				var msg workerMessage
				if err = read.Decode(&msg); err != nil {
					break
				}
				if msg.DNS != nil {
					report(completion{worker: w, job: j, dns: msg.DNS})
					continue
				}
				if msg.Result == nil {
					err = errors.New("empty worker message")
					break
				}
				report(completion{worker: w, job: j, result: *msg.Result})
				break
			}
			if err != nil {
				report(completion{worker: w, job: j, err: err})
				return
			}
		}
	}
}

func (s *scan) accept(c completion) {
	if c.dns != nil {
		stats := s.resolverStats[c.job.Resolver]
		if stats == nil {
			stats = &resolverStats{}
			s.resolverStats[c.job.Resolver] = stats
		}
		stats.record(*c.dns, int(time.Since(s.started)/time.Second))
		return
	}
	if c.err != nil {
		s.err = fmt.Errorf("worker process: %w", c.err)
		return
	}
	r := c.result
	b := s.bucket(time.Now())
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
	s.completed++
	if len(s.recent) == 5 {
		s.recent = s.recent[:4]
	}
	s.recent = append(s.recent, observation{})
	copy(s.recent[1:], s.recent)
	s.recent[0] = observation{c.job.Origin, r}
	if s.err == nil && len(s.workers) <= s.processes {
		if j, ok := s.next(); ok {
			c.worker <- j
			return
		}
	}
	close(c.worker)
	delete(s.workers, c.worker)
}

func (s *scan) bucket(t time.Time) *bucket {
	sec := int(t.Sub(s.started) / time.Second)
	b := &s.history[sec%len(s.history)]
	if b.second != sec {
		*b = bucket{second: sec}
	}
	return b
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
	s.source.Close()
}
