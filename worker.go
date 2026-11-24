package main

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/zmap/zdns/v2/src/zdns"
)

// The parent sends each origin string once. Results refer back to the in-memory
// list by index, including when parallel probes complete out of order.
type probeOptions struct {
	Timeout   int
	DNSOnly   bool
	Resolvers []string
	Offset    int
}

func (o probeOptions) job(index int, origin string) job {
	j := job{Origin: origin, Timeout: o.Timeout, DNSOnly: o.DNSOnly}
	if n := len(o.Resolvers); n > 0 {
		j.Resolver = o.Resolvers[(index%n+o.Offset)%n]
	}
	return j
}

type workerInit struct {
	Parallel     int
	Fixed        bool
	Start, Count int
	Options      probeOptions
}

type workBlock struct {
	First, Lanes int
	Origins      []string
	Options      probeOptions
}

type probeEvent struct {
	Index    int
	Resolver string
	DNS      *dnsEvent
	Result   result
}

func (s *scan) runWorker(w *worker, init workerInit, shard []string) {
	report := func(events []probeEvent, err error) {
		select {
		case s.reports <- completion{worker: w, events: events, err: err}:
		case <-s.ctx.Done():
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.executable, "_worker")
	in, err := cmd.StdinPipe()
	if err != nil {
		report(nil, err)
		return
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		report(nil, err)
		return
	}
	defer out.Close()
	if err = cmd.Start(); err != nil {
		report(nil, err)
		return
	}

	written := make(chan error, 1)
	go func() {
		err := writeWork(ctx, gob.NewEncoder(in), w, init, shard)
		in.Close()
		if err != nil {
			cancel()
		}
		written <- err
	}()
	read := gob.NewDecoder(out)
	for {
		var events []probeEvent
		if err = read.Decode(&events); err != nil {
			break
		}
		report(events, nil)
	}
	cancel()
	writeErr := <-written
	waitErr := cmd.Wait()
	// A cleanly closed input means the scheduler already retired this worker
	// after accounting for every result. Do not enqueue another report.
	if err == io.EOF && writeErr == nil {
		return
	}
	switch {
	case err != io.EOF:
		report(nil, err)
	case writeErr != nil && !errors.Is(writeErr, context.Canceled):
		report(nil, writeErr)
	case waitErr != nil:
		report(nil, waitErr)
	default:
		report(nil, errors.New("worker exited before completing its assignments"))
	}
}

func writeWork(ctx context.Context, enc *gob.Encoder, w *worker, init workerInit, shard []string) error {
	if err := enc.Encode(init); err != nil {
		return err
	}
	// Bound encoder/decoder scratch space even when an entire monthly snapshot
	// belongs to a single process.
	for len(shard) > 0 {
		n := min(len(shard), 4096)
		if err := enc.Encode(shard[:n]); err != nil {
			return err
		}
		shard = shard[n:]
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.retired:
			return nil
		case options := <-w.controls:
			if err := enc.Encode(workBlock{Options: options}); err != nil {
				return err
			}
		case block := <-w.work:
			if err := enc.Encode(block); err != nil {
				return err
			}
		}
	}
}

type indexedJob struct {
	index int
	job   job
}

func serveWorker(in io.Reader, out io.Writer) error {
	dec := gob.NewDecoder(in)
	var init workerInit
	if err := dec.Decode(&init); err != nil {
		return err
	}
	if init.Parallel < 1 || init.Count < 0 {
		return errors.New("invalid worker configuration")
	}

	// Each lane needs its own mutable resolver. ZDNS supports sharing the
	// config/cache across resolvers, allocated lazily once per process.
	config := sync.OnceValue(zdns.NewResolverConfig)
	transport := newProbeTransport()
	defer transport.CloseIdleConnections()
	events := make(chan probeEvent, min(init.Parallel, 256))
	var lanes sync.WaitGroup
	launch := func(count int, next func() (indexedJob, bool)) {
		for range count {
			lanes.Go(func() {
				dns := workerDNS{sharedConfig: config}
				defer dns.close()
				for {
					item, ok := next()
					if !ok {
						return
					}
					j := item.job
					r := probe(transport, j, &dns, func(e dnsEvent) {
						events <- probeEvent{Index: item.index, Resolver: j.Resolver, DNS: &e}
					})
					events <- probeEvent{Index: item.index, Resolver: j.Resolver, Result: r}
				}
			})
		}
	}

	readDone := make(chan error, 1)
	if init.Fixed {
		shard := make([]string, 0, init.Count)
		for len(shard) < init.Count {
			var block []string
			if err := dec.Decode(&block); err != nil {
				return err
			}
			if len(block) == 0 || len(block) > init.Count-len(shard) {
				return errors.New("invalid fixed partition")
			}
			shard = append(shard, block...)
		}
		var options atomic.Pointer[probeOptions]
		options.Store(&init.Options)
		var cursor atomic.Int64
		launch(init.Parallel, func() (indexedJob, bool) {
			i := int(cursor.Add(1) - 1)
			if i >= len(shard) {
				return indexedJob{}, false
			}
			index := init.Start + i
			return indexedJob{index, options.Load().job(index, shard[i])}, true
		})
		go func() { lanes.Wait(); close(events) }()
		go func() {
			for {
				var update workBlock
				if err := dec.Decode(&update); err != nil {
					readDone <- err
					return
				}
				options.Store(&update.Options)
			}
		}()
	} else {
		jobs := make(chan indexedJob, min(init.Parallel, 256))
		next := func() (indexedJob, bool) { j, ok := <-jobs; return j, ok }
		go func() {
			defer func() { close(jobs); lanes.Wait(); close(events) }()
			started := 0
			for {
				var block workBlock
				if err := dec.Decode(&block); err != nil {
					readDone <- err
					return
				}
				if block.Lanes < 1 || block.Lanes > init.Parallel {
					readDone <- errors.New("invalid worker concurrency")
					return
				}
				if block.Lanes > started {
					launch(block.Lanes-started, next)
					started = block.Lanes
				}
				for i, origin := range block.Origins {
					index := block.First + i
					jobs <- indexedJob{index, block.Options.job(index, origin)}
				}
			}
		}()
	}
	if err := writeEvents(gob.NewEncoder(out), events); err != nil {
		return err
	}
	// The parent closes stdin after accepting the last result. This also joins
	// the fixed worker's configuration reader before a clean exit.
	if err := <-readDone; err != io.EOF {
		return fmt.Errorf("read worker input: %w", err)
	}
	return nil
}

// Flush immediately at low volume. Under load, drain ready results and DNS
// observations together to amortize pipe writes without waiting for a timer.
func writeEvents(enc *gob.Encoder, events <-chan probeEvent) error {
	batch := make([]probeEvent, 0, 256)
	for first := range events {
		batch = append(batch[:0], first)
	drain:
		for len(batch) < cap(batch) {
			select {
			case e, ok := <-events:
				if !ok {
					break drain
				}
				batch = append(batch, e)
			default:
				break drain
			}
		}
		if err := enc.Encode(batch); err != nil {
			return err
		}
	}
	return nil
}
