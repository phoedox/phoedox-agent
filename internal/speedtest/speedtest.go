// Package speedtest measures the agent's connection to its instance: round
// trip time, download and upload throughput. It only talks to the instance,
// over the agent-token-authenticated endpoints under /api/v1/agent/speedtest.
package speedtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"
)

const (
	mib = 1 << 20

	// Defaults; a zero field in Tester uses these.
	defaultProbe     = 2 * mib
	defaultMin       = 4 * mib
	defaultMax       = 100 * mib
	defaultMaxUpload = 25 * mib
	defaultTarget    = 4 * time.Second
	pings            = 5
)

// Result holds the measurement: Mbit/s and milliseconds.
type Result struct {
	Down float64
	Up   float64
	Ping float64
}

// JSON renders the result exactly as {"down":312.4,"up":95.1,"ping":2.3},
// always with one decimal.
func (r Result) JSON() string {
	f := func(v float64) string {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			v = 0
		}
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return `{"down":` + f(r.Down) + `,"up":` + f(r.Up) + `,"ping":` + f(r.Ping) + `}`
}

// Mbps converts n bytes transferred in d to Mbit/s, rounded to one decimal.
func Mbps(n int64, d time.Duration) float64 {
	if n <= 0 || d <= 0 {
		return 0
	}
	return Round1(float64(n) * 8 / d.Seconds() / 1e6)
}

// Millis converts d to milliseconds, rounded to one decimal.
func Millis(d time.Duration) float64 {
	return Round1(float64(d) / float64(time.Millisecond))
}

// Round1 rounds to one decimal.
func Round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// Median of the durations (mean of the middle two for an even count).
func Median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// SizeFor picks the size of the main transfer from a probe: the bytes that
// would take target at the probe's rate, clamped to [lo, hi].
func SizeFor(probe int64, took, target time.Duration, lo, hi int64) int64 {
	if probe <= 0 || took <= 0 {
		return lo
	}
	n := float64(probe) / took.Seconds() * target.Seconds()
	if n >= float64(hi) {
		return hi
	}
	return max(lo, int64(n))
}

// Tester runs the measurement against one instance.
type Tester struct {
	Base      string // instance URL, e.g. https://app.phoedox.com
	Token     string // agent token
	UserAgent string
	HTTP      *http.Client

	// Sizes and target duration; zero uses the defaults (tests shrink them).
	Probe, Min, Max, MaxUpload int64
	Target                     time.Duration
}

func or[T int64 | time.Duration](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}

// Run measures ping (median of 5 after a warm-up), download (2 MiB probe,
// then a size aiming at ~4 s between 4 and 100 MiB) and upload (same size,
// at most 25 MiB).
func (t Tester) Run(ctx context.Context) (Result, error) {
	var res Result
	// Warm-up: opens the (keep-alive) connection, so TLS is not measured.
	if _, err := t.ping(ctx); err != nil {
		return res, fmt.Errorf("ping: %w", err)
	}
	rtts := make([]time.Duration, 0, pings)
	for range pings {
		d, err := t.ping(ctx)
		if err != nil {
			return res, fmt.Errorf("ping: %w", err)
		}
		rtts = append(rtts, d)
	}
	res.Ping = Millis(Median(rtts))

	probe := or(t.Probe, defaultProbe)
	took, err := t.download(ctx, probe)
	if err != nil {
		return res, fmt.Errorf("download: %w", err)
	}
	size := SizeFor(probe, took, or(t.Target, defaultTarget), or(t.Min, defaultMin), or(t.Max, defaultMax))
	took, err = t.download(ctx, size)
	if err != nil {
		return res, fmt.Errorf("download: %w", err)
	}
	res.Down = Mbps(size, took)

	up := min(size, or(t.MaxUpload, defaultMaxUpload))
	took, err = t.upload(ctx, up)
	if err != nil {
		return res, fmt.Errorf("upload: %w", err)
	}
	res.Up = Mbps(up, took)
	return res, nil
}

func (t Tester) client() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return http.DefaultClient
}

func (t Tester) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.Base+"/api/v1/agent/speedtest"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	if t.UserAgent != "" {
		req.Header.Set("User-Agent", t.UserAgent)
	}
	// No transparent gzip (it would distort the byte count), no caches.
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	return req, nil
}

func (t Tester) do(req *http.Request) (*http.Response, error) {
	res, err := t.client().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		return nil, fmt.Errorf("instance: %s", res.Status)
	}
	return res, nil
}

func (t Tester) ping(ctx context.Context) (time.Duration, error) {
	req, err := t.request(ctx, http.MethodGet, "/ping", nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	res, err := t.do(req)
	if err != nil {
		return 0, err
	}
	d := time.Since(start)
	// Drain so the connection is reused for the next round trip.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	return d, nil
}

// download fetches n bytes and returns the time from sending the request
// to the last byte.
func (t Tester) download(ctx context.Context, n int64) (time.Duration, error) {
	req, err := t.request(ctx, http.MethodGet, "/down?bytes="+strconv.FormatInt(n, 10), nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	res, err := t.do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	got, err := io.Copy(io.Discard, io.LimitReader(res.Body, n+1))
	d := time.Since(start)
	if err != nil {
		return 0, err
	}
	if got != n {
		return 0, fmt.Errorf("got %d bytes, asked for %d", got, n)
	}
	return d, nil
}

// upload sends n random bytes. The duration is the client's (request start
// to answer) or the server's, whichever is longer: a proxy that buffers the
// body would make the server's time too short, so it never inflates the rate.
func (t Tester) upload(ctx context.Context, n int64) (time.Duration, error) {
	body := make([]byte, n)
	if _, err := rand.Read(body); err != nil {
		return 0, err
	}
	req, err := t.request(ctx, http.MethodPost, "/up", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	res, err := t.do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var out struct {
		Bytes   int64   `json:"bytes"`
		Seconds float64 `json:"seconds"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
	d := time.Since(start)
	if err != nil {
		return 0, fmt.Errorf("answer: %w", err)
	}
	if out.Bytes != n {
		return 0, fmt.Errorf("instance received %d bytes, sent %d", out.Bytes, n)
	}
	return UploadDuration(d, out.Seconds), nil
}

// UploadDuration is the longer of the client's and the server's time.
func UploadDuration(client time.Duration, serverSeconds float64) time.Duration {
	if serverSeconds > 0 && !math.IsInf(serverSeconds, 0) {
		if s := time.Duration(serverSeconds * float64(time.Second)); s > client {
			return s
		}
	}
	return client
}
