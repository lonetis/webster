package main

import (
	"context"
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

func TestOriginRates(t *testing.T) {
	var s scan
	check := func(at time.Duration, wantAvg, wantRecent float64) {
		t.Helper()
		avg, recent := s.originRates(at)
		if avg != wantAvg || recent != wantRecent {
			t.Fatalf("at %s got avg %g, last 5s %g; want %g, %g", at, avg, recent, wantAvg, wantRecent)
		}
	}
	check(0, 0, 0)
	s.recordCompletion(500 * time.Millisecond)
	s.recordCompletion(1500 * time.Millisecond)
	check(2*time.Second, 1, 1)
	s.recordCompletion(5500 * time.Millisecond)
	check(5500*time.Millisecond, 3/5.5, 2.0/5) // exactly 5s old expires
	check(6*time.Second, 0.5, 2.0/5)           // the 1.5s completion is still in range
	check(11*time.Second, 3.0/11, 0)           // idle window
	s.recordCompletion(12 * time.Second)
	check(12*time.Second, 4.0/12, 1.0/5)
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
	t.Chdir(dir)
	s, err := startScan(context.Background(), path, settings{targets: 8, processes: 1, timeout: 10}, 0)
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
	if s.reasons["timeout"] != 1 || s.succeeded != 3 {
		t.Fatalf("unexpected outcomes: %d reached, %v", s.succeeded, s.reasons)
	}
	arrival()
	start := time.Now()
	s.finish()
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation did not stop children promptly")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "input.csv" {
		t.Fatalf("unexpected run files: %v, %v", files, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != input.String() || s.completed != 4 {
		t.Fatalf("changed input or incorrect completion count: %v, %d", err, s.completed)
	}
}
