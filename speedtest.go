package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const speedTestURL = "https://speed.cloudflare.com"

// Each direction stops after the first transfer lasting at least one second.
var speedSampleSizes = [...]int64{100_000, 1_000_000, 10_000_000, 50_000_000}

type speedProgress struct {
	phase        string // empty on completion or failure
	sampleBytes  int64
	latency      time.Duration
	downloadMbps float64
	uploadMbps   float64
	err          error
}

func (s speedProgress) rows() [3][2]string {
	rows := [3][2]string{{"HTTPS latency", "—"}, {"Download", "—"}, {"Upload", "—"}}
	if s.latency > 0 {
		rows[0][1] = fmt.Sprintf("%.1f ms", float64(s.latency)/float64(time.Millisecond))
	}
	if s.downloadMbps > 0 {
		rows[1][1] = fmt.Sprintf("%.1f Mbps", s.downloadMbps)
	}
	if s.uploadMbps > 0 {
		rows[2][1] = fmt.Sprintf("%.1f Mbps", s.uploadMbps)
	}
	return rows
}

// Upload synthetic payloads without allocating a buffer the size of the sample.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func transferSample(ctx context.Context, client *http.Client, base, method string, size int64) (time.Duration, error) {
	path := "/__down"
	var payload io.Reader
	if method == http.MethodPost {
		path = "/__up"
		payload = io.LimitReader(zeroReader{}, size)
	}
	url := fmt.Sprintf("%s%s?bytes=%d&t=%d", base, path, size, time.Now().UnixNano())
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("User-Agent", "Webster (internet speed test)")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = size
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("speed-test server returned %s", resp.Status)
	}
	if method == http.MethodPost {
		received := resp.Header.Get("cf-meta-upload-bytes")
		if n, err := strconv.ParseInt(received, 10, 64); err != nil || n != size {
			return 0, fmt.Errorf("server did not confirm receipt of %d upload bytes (reported %q)", size, received)
		}
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	} else {
		if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
			return 0, fmt.Errorf("unexpected download encoding %q", encoding)
		}
		var n int64
		n, err = io.Copy(io.Discard, io.LimitReader(resp.Body, size+1))
		if err == nil && n != size {
			return 0, fmt.Errorf("received %d download bytes, expected %d", n, size)
		}
	}
	return time.Since(start), err
}

// The callback receives snapshots before each stage/sample and once at the end.
// Only successfully completed measurements appear in the final snapshot.
func measureSpeed(ctx context.Context, base string, publish func(speedProgress)) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	s := speedProgress{phase: "HTTPS latency"}
	defer func() {
		if s.err != nil {
			s.err = fmt.Errorf("%s: %w", s.phase, s.err)
		}
		s.phase, s.sampleBytes = "", 0
		publish(s)
	}()
	publish(s)
	// Discard the first round trip, which includes DNS, TCP and TLS setup.
	var latency [4]time.Duration
	for i := range latency {
		latency[i], s.err = transferSample(ctx, client, base, http.MethodGet, 0)
		if s.err != nil {
			return
		}
	}
	slices.Sort(latency[1:])
	s.latency = latency[2]
	for _, phase := range []struct {
		name, method string
		mbps         *float64
	}{
		{"Download", http.MethodGet, &s.downloadMbps},
		{"Upload", http.MethodPost, &s.uploadMbps},
	} {
		s.phase = phase.name
		for i, size := range speedSampleSizes {
			s.sampleBytes = size
			publish(s)
			var elapsed time.Duration
			elapsed, s.err = transferSample(ctx, client, base, phase.method, size)
			if s.err != nil {
				return
			}
			if elapsed >= time.Second || i == len(speedSampleSizes)-1 {
				*phase.mbps = float64(size) * 8 / elapsed.Seconds() / 1e6
				break
			}
		}
	}
}

func printSpeedTest(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Testing internet speed to speed.cloudflare.com")
	var previous speedProgress
	measureSpeed(ctx, speedTestURL, func(s speedProgress) {
		before := previous.rows()
		for i, row := range s.rows() {
			if row[1] != before[i][1] {
				fmt.Fprintf(out, "%-13s %s\n", row[0], row[1])
			}
		}
		previous = s
	})
	return previous.err
}
