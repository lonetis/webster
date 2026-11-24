package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanCommandFromConfig(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	t.Chdir(t.TempDir())
	rows := "origin,rank\n" + server.URL + ",1\ninvalid,2\nhttp://localhost,3\n"
	if err := os.WriteFile("origins.csv", []byte(rows), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("quick.json", []byte(`{"input":"origins.csv","targets":"all","dns-only":true,"processes":1,"timeout":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flags []string
		want  []string
	}{
		{nil, []string{"3 completed · 1 resolved · 1 failed · 1 skipped", "input=1"}},
		{[]string{"-dns-only=false", "-targets", "2"}, []string{"2 completed · 1 reachable · 1 failed", "404=1", "input=1"}},
	} {
		var out, log bytes.Buffer
		err := command(t.Context(), append([]string{"scan", "-config", "quick.json"}, tc.flags...), &out, &log)
		if err != nil || !strings.Contains(log.String(), "Workers 1 · Timeout 2s") {
			t.Fatalf("scan: %v; stderr: %s", err, &log)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q from stdout: %s", want, &out)
			}
		}
	}
}

func TestScanCommandErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("bad.json", []byte(`{"prcesses":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-config", "bad.json"}, "unknown measurement option"},
		{[]string{"-timeout", "0"}, "invalid -timeout"},
		{[]string{"-timeout", "0.5s"}, "invalid -timeout"},
		{[]string{"-processes", "0"}, "-processes must be"},
		{[]string{"-targets", "-1"}, "invalid -targets"},
		{[]string{"-dns", "other"}, "invalid DNS mode"},
		{[]string{"-provider", "missing"}, "unknown recursive provider"},
	} {
		err := command(t.Context(), append([]string{"scan"}, tc.args...), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: got %v, want %q", tc.args, err, tc.want)
		}
	}
	if err := os.WriteFile("bad.csv", []byte("origin,rank\nhttp://localhost,1\nbad,row,extra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := command(t.Context(), []string{"scan", "-input", "bad.csv", "-dns-only", "-processes", "1"}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "read input") || !strings.Contains(out.String(), "Partial results · 1 completed") {
		t.Fatalf("mid-scan input error: %v, stdout %s", err, &out)
	}
}

func TestScanCommandCancel(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "origins.csv")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("origin,rank\n%s,1\n", server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- command(ctx, []string{"scan", "-input", path, "-timeout", "60s"}, &out, io.Discard)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach the origin")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !strings.Contains(out.String(), "Partial results · 0 completed") {
			t.Fatalf("canceled run: %v, stdout %s", err, &out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker outlived the canceled scan")
	}
}

func TestScanSnapshotWithResolver(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "global-202610")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	input := "origin,rank\nhttps://webster.test,1\n"
	if err := os.WriteFile(filepath.Join(dir, "origins.csv"), []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"Month":"202610","Origins":1,"Bytes":%d}`, len(input))
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := testDNS(t, false)
	providers["fixture"] = endpoint
	t.Cleanup(func() { delete(providers, "fixture") })
	var out, log bytes.Buffer
	err := command(t.Context(), []string{"scan", "-data", root, "-dns-only", "-dns", "recursive", "-provider", "fixture"}, &out, &log)
	if err != nil || !strings.Contains(log.String(), "Targets 1") || !strings.Contains(out.String(), "1 resolved") ||
		!strings.Contains(out.String(), "DNS "+endpoint+"  Lookups 1  Pending 0  Replies 1") {
		t.Fatalf("snapshot scan: %v, stdout %s, stderr %s", err, &out, &log)
	}
}
