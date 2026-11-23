package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "_worker" {
		if err := serveWorker(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

// Gate responses so the test can resize real worker processes while requests are in flight.
func TestResizeTimeoutAndQuit(t *testing.T) {
	entered := make(chan struct{}, 10)
	release := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "input.csv")
	var input strings.Builder
	input.WriteString("origin,rank\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&input, "%s,%d\n", server.URL, i)
	}
	if err := os.WriteFile(path, []byte(input.String()), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := startScan(context.Background(), path, dir, settings{targets: 8, processes: 1, timeout: 10}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.finish()
	arrival := func() {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not start its request")
		}
	}
	accept := func() {
		t.Helper()
		select {
		case c := <-s.reports:
			s.accept(c)
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not report")
		}
		if s.err != nil {
			t.Fatal(s.err)
		}
	}
	s.fill()
	arrival()
	s.processes = 3
	s.fill()
	arrival()
	arrival()
	s.processes = 1
	s.timeout = 1
	for _, active := range []int{2, 1, 1} {
		release <- struct{}{}
		accept()
		if len(s.workers) != active {
			t.Fatalf("expected %d workers, got %d", active, len(s.workers))
		}
	}
	arrival()
	// The in-flight check still has its one-second deadline. Its successor
	// gets sixty seconds, so returning quickly from finish requires cancellation.
	s.timeout = 60
	accept()
	if s.reasons["timeout"] != 1 || s.reachable != 3 {
		t.Fatalf("unexpected outcomes: %d reached, %v", s.reachable, s.reasons)
	}
	arrival()
	start := time.Now()
	s.finish()
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation did not stop children promptly")
	}
	file, err := os.Open(s.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[4][4] != "timeout" {
		t.Fatalf("unexpected partial CSV: %v", rows)
	}
	ranks := map[string]bool{}
	for _, row := range rows[1:] {
		ranks[row[1]] = true
	}
	if len(ranks) != 4 {
		t.Fatalf("duplicate input assignment: %v", rows)
	}
}
