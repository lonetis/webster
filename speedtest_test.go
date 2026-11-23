package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSpeedMeasurements(t *testing.T) {
	var latencyRequests, uploads, downloads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if err != nil || size < 0 || r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("invalid sample request %v", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/__down" && size == 0:
			latencyRequests++
		case r.Method == "GET" && r.URL.Path == "/__down":
			downloads++
			if downloads > len(speedSampleSizes) || size != speedSampleSizes[downloads-1] {
				t.Errorf("unexpected download sample %d at step %d", size, downloads)
			}
			_, _ = io.CopyN(w, zeroReader{}, size)
		case r.Method == "POST" && r.URL.Path == "/__up":
			uploads++
			if uploads > len(speedSampleSizes) || size != speedSampleSizes[uploads-1] {
				t.Errorf("unexpected upload sample %d at step %d", size, uploads)
			}
			n, err := io.Copy(io.Discard, r.Body)
			if err != nil || n != size || r.ContentLength != size {
				t.Errorf("upload of %d bytes: length %d, received %d, err %v", size, r.ContentLength, n, err)
			}
			w.Header().Set("cf-meta-upload-bytes", strconv.FormatInt(n, 10))
		default:
			t.Errorf("unexpected request %s %v", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	var result speedProgress
	measureSpeed(t.Context(), server.URL, func(s speedProgress) { result = s })
	server.Close()
	if latencyRequests != 4 || uploads == 0 || downloads == 0 ||
		result.err != nil || result.phase != "" || result.latency <= 0 || result.downloadMbps <= 0 || result.uploadMbps <= 0 {
		t.Fatalf("result %+v after %d latency, %d download and %d upload requests", result, latencyRequests, downloads, uploads)
	}
}

func TestSpeedSampleRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, method, body, uploadAck, encoding string
		status                                  int
	}{
		{"short download", "GET", "a", "", "", 200},
		{"long download", "GET", "abc", "", "", 200},
		{"encoded download", "GET", "ab", "", "gzip", 200},
		{"missing acknowledgement", "POST", "", "", "", 200},
		{"short upload", "POST", "", "1", "", 200},
		{"unavailable", "GET", "ab", "", "", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("cf-meta-upload-bytes", tc.uploadAck)
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
		if r.URL.Path != "/__down" {
			t.Errorf("followed redirect to %s", r.URL.Path)
		}
		if r.URL.Query().Get("bytes") != "0" {
			http.Redirect(w, r, "/login", http.StatusFound)
		}
	}))
	defer server.Close()
	var result speedProgress
	measureSpeed(t.Context(), server.URL, func(s speedProgress) { result = s })
	if result.err == nil || !strings.Contains(result.err.Error(), "302") ||
		result.phase != "" || result.latency <= 0 || result.downloadMbps != 0 || result.uploadMbps != 0 {
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
	measureSpeed(ctx, server.URL, func(s speedProgress) { result = s })
	if result.phase != "" || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancellation result %+v", result)
	}
}
