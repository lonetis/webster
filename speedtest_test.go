package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveSpeedConfig(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	if r.TLS != nil {
		base = "https://" + r.Host
	}
	json.NewEncoder(w).Encode(map[string]speedEndpoints{"urls": {base + "/small", base + "/large", base + "/upload"}})
}

func TestSpeedMeasurements(t *testing.T) {
	options := speedOptions{duration: 120 * time.Millisecond, streams: 3}
	var latencyRequests, active, peak, downloaded, uploaded atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			serveSpeedConfig(w, r)
			return
		}
		if r.URL.Path == "/small" && r.Method == "GET" {
			latencyRequests.Add(1)
			fmt.Fprint(w, "x")
			return
		}
		size := r.ContentLength
		if r.Method == "GET" {
			var end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=0-%d", &end); err != nil {
				t.Error(err)
				return
			}
			size = end + 1
		}
		if !slices.Contains(speedChunkBytes[:], size) || r.Header.Get("Accept-Encoding") != "identity" || r.ProtoMajor != 1 {
			t.Errorf("invalid sample request %v", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		// Keep requests in flight long enough to exercise concurrency and drain.
		time.Sleep(10*time.Millisecond + time.Duration(size)*time.Second/5_000_000)
		switch {
		case r.Method == "GET" && r.URL.Path == "/large":
			downloaded.Add(size)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.CopyN(w, zeroReader{}, size)
		case r.Method == "POST" && r.URL.Path == "/upload":
			received, err := io.Copy(io.Discard, r.Body)
			if err != nil || received != size || r.ContentLength != size {
				t.Errorf("upload of %d bytes: length %d, received %d, err %v", size, r.ContentLength, received, err)
			}
			uploaded.Add(received)
			fmt.Fprintf(w, `{"Bytes":%d}`, received)
		default:
			t.Errorf("unexpected request %s %v", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	var result speedProgress
	measureSpeed(t.Context(), server.URL, options, func(s speedProgress) {
		if !s.warmup && s.current.elapsed == 0 {
			switch s.phase {
			case "Download":
				if downloaded.Swap(0) == 0 {
					t.Error("no download warm-up bytes")
				}
			case "Upload":
				if uploaded.Swap(0) == 0 {
					t.Error("no upload warm-up bytes")
				}
			}
		}
		result = s
	})
	if result.err != nil || result.phase != "" || result.latency <= 0 || latencyRequests.Load() != 4 {
		t.Fatalf("unexpected result %+v, latency requests %d", result, latencyRequests.Load())
	}
	if peak.Load() != int64(options.streams) {
		t.Fatalf("peak concurrent transfers %d, want %d", peak.Load(), options.streams)
	}
	for _, check := range []struct {
		m     speedMeasurement
		bytes int64
	}{
		{result.download, downloaded.Load()}, {result.upload, uploaded.Load()},
	} {
		if check.m.elapsed < options.duration || check.bytes <= 0 || check.m.bytes != check.bytes {
			t.Errorf("measured %v; server transferred %d bytes in measured window", check.m, check.bytes)
		}
	}
}

func TestSpeedWindowStopsAllConnectionsOnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ready := make(chan struct{})
	stopped := make(chan struct{})
	var started atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if started.Add(1) == 2 {
			close(ready)
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		select {
		case <-ready:
		case <-ctx.Done():
		}
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	m, err := speedWindow(ctx, server.Client(), server.URL, http.MethodGet, []int{0, 0}, time.Second, func(speedMeasurement) {})
	if err == nil || !strings.Contains(err.Error(), "429") || m.bytes != 0 {
		t.Fatalf("unexpected result %v, err %v", m, err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("other connection remained active")
	}
}

func TestSpeedClientNegotiatesHTTP1(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("negotiated %s", r.Proto)
		}
		fmt.Fprint(w, "x")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	base := server.Client().Transport.(*http.Transport).Clone()
	base.TLSClientConfig.NextProtos = []string{"h2", "http/1.1"}
	client := speedClient(base, 4)
	defer client.CloseIdleConnections()
	if _, err := transferSample(t.Context(), client, server.URL, http.MethodGet, 1); err != nil {
		t.Fatal(err)
	}
}

func TestSpeedSampleRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, method, body, encoding string
		status                       int
	}{
		{"short download", "GET", "a", "", 206},
		{"long download", "GET", "abc", "", 206},
		{"encoded download", "GET", "ab", "gzip", 206},
		{"missing acknowledgement", "POST", "{}", "", 200},
		{"short upload", "POST", `{"Bytes":1}`, "", 200},
		{"unavailable", "GET", "ab", "", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

				w.Header().Set("Content-Encoding", tc.encoding)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			if _, err := transferSample(t.Context(), server.Client(), server.URL, tc.method, 2); err == nil {
				t.Fatal("accepted invalid speed-test response")
			}
		})
	}
}

func TestSpeedTestKeepsLatencyOnRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			serveSpeedConfig(w, r)
		case "/small":
			fmt.Fprint(w, "x")
		case "/large":
			http.Redirect(w, r, "/login", http.StatusFound)
		default:
			t.Errorf("followed redirect to %s", r.URL.Path)
		}
	}))
	defer server.Close()
	var result speedProgress
	measureSpeed(t.Context(), server.URL, speedOptions{duration: time.Second, streams: 2}, func(s speedProgress) { result = s })
	if result.err == nil || !strings.Contains(result.err.Error(), "302") ||
		result.phase != "" || result.latency <= 0 || result.download.bytes != 0 || result.upload.bytes != 0 {
		t.Fatalf("unexpected partial result %+v", result)
	}
}

func TestSpeedTestCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			t.Error("request still active after cancellation")
		}
	}))
	defer server.Close()
	var result speedProgress
	measureSpeed(ctx, server.URL, speedOptions{duration: time.Second, streams: 2}, func(s speedProgress) { result = s })
	if result.phase != "" || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancellation result %+v", result)
	}
}
