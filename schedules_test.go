package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/gob"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestOriginListInMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	csvout := csv.NewWriter(f)
	csvout.Write([]string{"origin", "rank"})
	var want []string
	for i := 0; i < 9000; i++ {
		value := fmt.Sprintf("https://example.test/%d,\"quoted\"", i)
		csvout.Write([]string{value, fmt.Sprint(i)})
		want = append(want, value)
	}
	csvout.Flush()
	if err := csvout.Error(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := loadOrigins(t.Context(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatal("packed list changed input order or CSV quoting")
	}
	for _, mode := range []string{"dynamic", "fixed"} {
		s, err := newScan(t.Context(), got, settings{schedule: mode, targets: 4, processes: 6, parallel: 8, timeout: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer s.finish()
		s.fill()
		if len(s.workers) != 4 {
			t.Fatalf("%s started %d workers for 4 origins", mode, len(s.workers))
		}
		for !s.done() {
			s.accept(nextReport(t, s))
		}
		if s.err != nil || s.completed != 4 || s.reasons["input"] != 4 {
			t.Fatalf("%s: %s, %v", mode, s.outcomeTotals(), s.err)
		}
	}
}

func nextReport(t *testing.T, s *scan) completion {
	t.Helper()
	select {
	case c := <-s.reports:
		return c
	case <-time.After(7 * time.Second):
		t.Fatal("timed out waiting for process report")
		return completion{}
	}
}

func TestFixedPartitions(t *testing.T) {
	const n = 25009 // crosses the 4096-name wire chunk in every partition
	origins := make([]string, n)
	for i := range origins {
		origins[i] = "http://127.0.0.1"
	}
	cfg := settings{schedule: "fixed", processes: 3, parallel: 5, timeout: 1, dnsOnly: true}
	s, err := newScan(t.Context(), origins, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.finish()
	s.fill()
	a := app{settings: cfg, scan: s}
	for _, key := range []rune{'-', '+', '=', 'p'} {
		a.key(t.Context(), tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone))
	}
	if a.processes != 3 || s.processes != 3 || a.edit != "" {
		t.Fatal("fixed resize allowed")
	}
	cfg.processes = 8
	s.updateSettings(cfg)
	if s.processes != 3 || s.launched != 3 || s.issued != n {
		t.Fatal("fixed partition assignment changed")
	}
	owners := make([]*worker, n)
	for !s.done() {
		report := nextReport(t, s)
		for _, e := range report.events {
			if e.Index < 0 || e.Index >= n || owners[e.Index] != nil {
				t.Fatalf("unexpected or repeated row %d", e.Index)
			}
			owners[e.Index] = report.worker
		}
		s.accept(report)
	}
	if s.err != nil || s.completed != n || s.skipped != n {
		t.Fatalf("%v: %s", s.err, s.outcomeTotals())
	}
	start := 0
	used := map[*worker]bool{}
	for part := range 3 {
		owner := owners[start]
		if owner == nil || used[owner] {
			t.Fatal("missing or repeated partition")
		}
		used[owner] = true
		size := []int{8337, 8336, 8336}[part]
		for _, w := range owners[start : start+size] {
			if w != owner {
				t.Fatal("partition not contiguous")
			}
		}
		start += size
	}
}

func TestParallelProcessesAndCancellation(t *testing.T) {
	for _, mode := range []string{"dynamic", "fixed"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 16)
			gate := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-gate:
					w.WriteHeader(204)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			cfg := settings{schedule: mode, processes: 2, parallel: 4, timeout: 60}
			origins := make([]string, 32)
			for i := range origins {
				origins[i] = server.URL
			}
			s, err := newScan(t.Context(), origins, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.finish()
			s.fill()
			for range 8 {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("eight simultaneous requests never arrived")
				}
			}
			select {
			case <-entered:
				t.Fatal("exceeded concurrency")
			default:
			}
			if len(s.workers) != 2 {
				t.Fatalf("unexpected worker count %d", len(s.workers))
			}
			if mode == "dynamic" {
				cfg.processes = 1
				s.updateSettings(cfg)
			}
			gate <- struct{}{}
			for s.completed == 0 {
				s.accept(nextReport(t, s))
			}
			start := time.Now()
			s.finish()
			if s.completed != 1 || time.Since(start) > 3*time.Second {
				t.Fatal("cancellation failed to preserve partial results and stop children")
			}
		})
	}
}

func TestFixedCLIAndEmptyList(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("fixed.json", []byte(`{"input":"origins.csv","schedule":"fixed","concurrency":20,"processes":3,"dns-only":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, rows := range []string{"", "http://127.0.0.1,1\n"} {
		if err := os.WriteFile("origins.csv", []byte("origin,rank\n"+rows), 0600); err != nil {
			t.Fatal(err)
		}
		var log, out strings.Builder
		if err := command(t.Context(), []string{"scan", "-config", "fixed.json"}, &out, &log); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), "fixed scheduling · 20 checks/process") || !strings.Contains(out.String(), fmt.Sprintf("%d completed", strings.Count(rows, "\n"))) {
			t.Fatalf("%s %s", &log, &out)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadOrigins(ctx, "origins.csv", 0); err != context.Canceled {
		t.Fatalf("canceled preload: %v", err)
	}
}

func TestWorkerExitWithUnfinishedPartition(t *testing.T) {
	for _, mode := range []string{"fixed", "dynamic"} {
		s, err := newScan(t.Context(), []string{"http://localhost"}, settings{schedule: mode, processes: 1, timeout: 1})
		if err != nil {
			t.Fatal(err)
		}
		s.executable = "/usr/bin/true" // successful process exit with no results
		s.fill()
		s.accept(nextReport(t, s))
		s.finish()
		if s.err == nil || s.completed != 0 {
			t.Fatalf("lost %s worker silently", mode)
		}
	}
}

func TestFixedWorkerUpdatesRemainingChecks(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "127.0.0.1:") {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(204)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(4 * time.Second):
			t.Error("updated deadline did not cancel the second HTTP request")
		}
	}))
	defer server.Close()
	defer close(release)
	upstream := testDNS(t, false)
	reader, writer := io.Pipe()
	defer writer.Close()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		err := serveWorker(reader, &output)
		reader.CloseWithError(err)
		done <- err
	}()
	enc := gob.NewEncoder(writer)
	if err := enc.Encode(workerInit{Parallel: 1, Fixed: true, Count: 2, Options: probeOptions{Timeout: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode([]string{server.URL, strings.Replace(server.URL, "127.0.0.1", "fixed.webster.test", 1)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first request never arrived")
	}
	// The unbuffered pipe cannot deliver the second frame until the worker
	// has applied the first. The first probe remains in flight throughout.
	for range 2 {
		if err := enc.Encode(workBlock{Options: probeOptions{Timeout: 1, Resolvers: []string{upstream}}}); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("worker did not complete the partition")
	}
	seen := make(map[int]probeEvent)
	dec := gob.NewDecoder(&output)
	for {
		var batch []probeEvent
		if err := dec.Decode(&batch); err != nil {
			if err != io.EOF {
				t.Fatal(err)
			}
			break
		}
		for _, e := range batch {
			if e.DNS == nil {
				seen[e.Index] = e
			}
		}
	}
	if len(seen) != 2 || seen[0].Resolver != "" || seen[0].Result.Status != 204 ||
		seen[1].Resolver != upstream || seen[1].Result.Reason != "timeout" {
		t.Fatalf("unexpected results after live update: %+v", seen)
	}
}

// Exercise parallel ZDNS resolvers inside the race-instrumented test process.
func TestParallelResolverPool(t *testing.T) {
	endpoints := []string{testDNS(t, false), testDNS(t, true)}
	for _, fixed := range []bool{false, true} {
		var input, output bytes.Buffer
		enc := gob.NewEncoder(&input)
		options := probeOptions{Timeout: 2, DNSOnly: true, Resolvers: endpoints}
		names := make([]string, 40)
		for i := range names {
			names[i] = "https://shared.webster.test"
		}
		init := workerInit{Parallel: 8, Fixed: fixed, Options: options}
		if fixed {
			init.Count = len(names)
		}
		if err := enc.Encode(init); err != nil {
			t.Fatal(err)
		}
		if fixed {
			if err := enc.Encode(names); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := enc.Encode(workBlock{Origins: names, Lanes: 8, Options: options}); err != nil {
				t.Fatal(err)
			}
		}
		if err := serveWorker(&input, &output); err != nil {
			t.Fatal(err)
		}
		dec := gob.NewDecoder(&output)
		seen := map[int]bool{}
		starts, finishes := 0, 0
		for {
			var batch []probeEvent
			if err := dec.Decode(&batch); err != nil {
				if err != io.EOF {
					t.Fatal(err)
				}
				break
			}
			for _, e := range batch {
				if e.Resolver != endpoints[e.Index%2] {
					t.Fatal("wrong resolver assignment")
				}
				if e.DNS != nil {
					if e.DNS.Status == "start" {
						starts++
					} else {
						finishes++
					}
					continue
				}
				if seen[e.Index] || e.Result.Resolved != (e.Index%2 == 0) {
					t.Fatalf("duplicate or incorrect result: %+v", e)
				}
				seen[e.Index] = true
			}
		}
		if len(seen) != len(names) || starts != len(names) || finishes != starts {
			t.Fatalf("missing observations: %d origins, %d/%d DNS events", len(seen), starts, finishes)
		}
	}
}
