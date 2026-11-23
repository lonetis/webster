package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const speedTestURL = "https://mensura.cdn-apple.com/api/v1/gm/config"

var speedChunkBytes = [...]int64{100_000, 1_000_000, 10_000_000, 50_000_000, 128_000_000}

type speedEndpoints struct {
	Small  string `json:"small_https_download_url"`
	Large  string `json:"large_https_download_url"`
	Upload string `json:"https_upload_url"`
}

func discoverSpeedEndpoints(ctx context.Context, client *http.Client, configURL string) (speedEndpoints, error) {
	var config struct{ URLs speedEndpoints }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		return speedEndpoints{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return speedEndpoints{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return speedEndpoints{}, fmt.Errorf("configuration server returned %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&config); err != nil {
		return speedEndpoints{}, fmt.Errorf("invalid speed-test configuration: %w", err)
	}
	for _, raw := range []string{config.URLs.Small, config.URLs.Large, config.URLs.Upload} {
		endpoint, err := url.Parse(raw)
		if err != nil || endpoint.Host == "" || endpoint.Scheme != req.URL.Scheme || endpoint.User != nil {
			return speedEndpoints{}, fmt.Errorf("invalid speed-test endpoint in configuration")
		}
	}
	return config.URLs, nil
}

type speedOptions struct {
	duration time.Duration
	streams  int
}

func speedFlags(flags *flag.FlagSet, prefix string) *speedOptions {
	var o speedOptions
	flags.DurationVar(&o.duration, prefix+"duration", 15*time.Second, "speed-test measurement time per direction (1s to 2m), after warm-up")
	flags.IntVar(&o.streams, prefix+"streams", 4, "parallel speed-test connections (1 to 8)")
	return &o
}

func (o speedOptions) validate() error {
	if o.duration < time.Second || o.duration > 2*time.Minute {
		return fmt.Errorf("speed-test duration must be between 1s and 2m")
	}
	if o.streams < 1 || o.streams > 8 {
		return fmt.Errorf("speed-test streams must be between 1 and 8")
	}
	return nil
}

type speedMeasurement struct {
	bytes   int64
	elapsed time.Duration
}

func (m speedMeasurement) String() string {
	if m.elapsed == 0 {
		return "—"
	}
	mbps := float64(m.bytes) * 8 / m.elapsed.Seconds() / 1e6
	return fmt.Sprintf("%.1f Mbps · %.1f MB in %.1fs", mbps, float64(m.bytes)/1e6, m.elapsed.Seconds())
}

type speedProgress struct {
	phase    string // empty on completion or failure
	warmup   bool
	window   time.Duration
	current  speedMeasurement
	latency  time.Duration
	download speedMeasurement
	upload   speedMeasurement
	err      error
}

func (s speedProgress) rows() [3][2]string {
	rows := [3][2]string{{"HTTPS latency", "—"}, {"Download", s.download.String()}, {"Upload", s.upload.String()}}
	if s.latency > 0 {
		rows[0][1] = fmt.Sprintf("%.1f ms", float64(s.latency)/float64(time.Millisecond))
	}
	return rows
}

// Upload synthetic payloads without allocating a buffer the size of the sample.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func transferSample(ctx context.Context, client *http.Client, endpoint, method string, size int64) (time.Duration, error) {
	var payload io.Reader
	if method == http.MethodPost {
		payload = io.LimitReader(zeroReader{}, size)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Webster (internet speed test)")
	status := http.StatusOK
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = size
	} else if size > 1 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", size-1))
		status = http.StatusPartialContent
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		return 0, fmt.Errorf("speed-test server returned %s for %s of %d bytes", resp.Status, method, size)
	}
	if method == http.MethodPost {
		var reply struct{ Bytes *int64 }
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&reply); err != nil || reply.Bytes == nil || *reply.Bytes != size {
			return 0, fmt.Errorf("server did not confirm receipt of %d upload bytes", size)
		}
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

// Reuse each connection's last chunk size across warm-up and measurement.
// Stop starting requests at the deadline and include the final transfers in
// both the verified byte count and the common wall-clock interval.
func speedWindow(ctx context.Context, client *http.Client, endpoint, method string, levels []int, duration time.Duration, progress func(speedMeasurement)) (speedMeasurement, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var completed atomic.Int64
	var workers sync.WaitGroup
	start := time.Now()
	end := start.Add(duration)
	for i := range levels {
		workers.Go(func() {
			for ctx.Err() == nil && time.Now().Before(end) {
				size := speedChunkBytes[levels[i]]
				elapsed, err := transferSample(ctx, client, endpoint, method, size)
				if err != nil {
					cancel(err)
					return
				}
				completed.Add(size)
				if elapsed < 250*time.Millisecond && levels[i] < len(speedChunkBytes)-1 {
					levels[i]++
				} else if elapsed > 3*time.Second && levels[i] > 0 {
					levels[i]--
				}
			}
		})
	}
	finished := make(chan time.Time, 1)
	go func() {
		workers.Wait()
		finished <- time.Now()
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			progress(speedMeasurement{completed.Load(), time.Since(start)})
		case finish := <-finished:
			return speedMeasurement{completed.Load(), finish.Sub(start)}, context.Cause(ctx)
		}
	}
}

func speedClient(base *http.Transport, streams int) *http.Client {
	transport := base.Clone()
	// One HTTP/1.1 transfer per TCP connection, including on HTTP/2 endpoints.
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	// Clone can inherit h2 in the default transport's TLS advertisement.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.MaxConnsPerHost = streams
	transport.MaxIdleConnsPerHost = streams
	return &http.Client{
		Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func measureSpeed(ctx context.Context, configURL string, options speedOptions, publish func(speedProgress)) {
	client := speedClient(http.DefaultTransport.(*http.Transport), options.streams)
	defer client.CloseIdleConnections()
	s := speedProgress{phase: "endpoint discovery"}
	defer func() {
		if s.err != nil {
			s.err = fmt.Errorf("%s: %w", s.phase, s.err)
		}
		s.phase = ""
		publish(s)
	}()
	publish(s)
	endpoints, err := discoverSpeedEndpoints(ctx, client, configURL)
	if err != nil {
		s.err = err
		return
	}
	s.phase = "HTTPS latency"
	publish(s)
	// Discard the first round trip, which includes DNS, TCP and TLS setup.
	var latency [4]time.Duration
	for i := range latency {
		latency[i], s.err = transferSample(ctx, client, endpoints.Small, http.MethodGet, 1)
		if s.err != nil {
			return
		}
	}
	slices.Sort(latency[1:])
	s.latency = latency[2]
	for _, phase := range []struct {
		name, method, endpoint string
		result                 *speedMeasurement
	}{
		{"Download", http.MethodGet, endpoints.Large, &s.download},
		{"Upload", http.MethodPost, endpoints.Upload, &s.upload},
	} {
		s.phase = phase.name
		levels := make([]int, options.streams)
		for _, warmup := range []bool{true, false} {
			s.warmup, s.window, s.current = warmup, options.duration, speedMeasurement{}
			if warmup {
				s.window = min(2*time.Second, options.duration)
			}
			publish(s)
			var measurement speedMeasurement
			measurement, s.err = speedWindow(ctx, client, phase.endpoint, phase.method, levels, s.window, func(m speedMeasurement) {
				s.current = m
				publish(s)
			})
			if s.err != nil {
				return
			}
			if !warmup {
				*phase.result = measurement
			}
		}
	}
}

func printSpeedTest(ctx context.Context, out io.Writer, options speedOptions) error {
	fmt.Fprintf(out, "Testing internet speed to Apple · %d parallel connections · %s per direction after warm-up\n", options.streams, options.duration)
	var previous speedProgress
	measureSpeed(ctx, speedTestURL, options, func(s speedProgress) {
		before := previous.rows()
		for i, row := range s.rows() {
			if row[1] != before[i][1] {
				fmt.Fprintf(out, "%-13s %s\n", row[0], row[1])
			}
		}
		if s.phase != "" && s.window > 0 && !s.warmup && s.current.elapsed == 0 {
			fmt.Fprintf(out, "Measuring %s for %s…\n", strings.ToLower(s.phase), s.window)
		}
		previous = s
	})
	return previous.err
}
