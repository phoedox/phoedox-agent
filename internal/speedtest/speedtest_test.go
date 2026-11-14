package speedtest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMaths(t *testing.T) {
	if got := Mbps(39_050_000, time.Second); got != 312.4 {
		t.Errorf("Mbps = %v", got)
	}
	if got := Mbps(100, 0); got != 0 {
		t.Errorf("zero duration = %v", got)
	}
	if got := Millis(2345 * time.Microsecond); got != 2.3 {
		t.Errorf("Millis = %v", got)
	}
	ms := time.Millisecond
	if got := Median([]time.Duration{9 * ms, 1 * ms, 3 * ms, 2 * ms, 50 * ms}); got != 3*ms {
		t.Errorf("odd median = %v", got)
	}
	if got := Median([]time.Duration{4 * ms, 1 * ms, 2 * ms, 3 * ms}); got != 2500*time.Microsecond {
		t.Errorf("even median = %v", got)
	}
	if Median(nil) != 0 {
		t.Error("empty median")
	}
	// 2 MiB in 100 ms → 80 MiB in 4 s.
	if got := SizeFor(2*mib, 100*ms, 4*time.Second, 4*mib, 100*mib); got != 80*mib {
		t.Errorf("SizeFor = %d MiB", got/mib)
	}
	if got := SizeFor(2*mib, 10*ms, 4*time.Second, 4*mib, 100*mib); got != 100*mib {
		t.Errorf("fast link capped: %d", got)
	}
	if got := SizeFor(2*mib, 10*time.Second, 4*time.Second, 4*mib, 100*mib); got != 4*mib {
		t.Errorf("slow link floor: %d", got)
	}
	if got := SizeFor(0, 0, time.Second, 4*mib, 100*mib); got != 4*mib {
		t.Errorf("no probe: %d", got)
	}
	if got := UploadDuration(time.Second, 2); got != 2*time.Second {
		t.Errorf("server slower: %v", got)
	}
	if got := UploadDuration(time.Second, 0.01); got != time.Second {
		t.Errorf("buffering proxy: %v", got)
	}
	if got := (Result{Down: 312.44, Up: 95, Ping: 2.3}).JSON(); got != `{"down":312.4,"up":95.0,"ping":2.3}` {
		t.Errorf("JSON = %s", got)
	}
	var v map[string]float64
	if err := json.Unmarshal([]byte(Result{}.JSON()), &v); err != nil || len(v) != 3 {
		t.Errorf("JSON is not valid: %v", err)
	}
}

func TestRun(t *testing.T) {
	var mu sync.Mutex
	var pings, downs []int64
	var upBytes int64
	auth := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Accept-Encoding") != "identity" {
			auth = false
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/agent/speedtest/ping":
			pings = append(pings, 1)
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/v1/agent/speedtest/down":
			n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
			downs = append(downs, n)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.CopyN(w, rand.Reader, n)
		case "POST /api/v1/agent/speedtest/up":
			start := time.Now()
			n, _ := io.Copy(io.Discard, r.Body)
			upBytes = n
			_ = json.NewEncoder(w).Encode(map[string]any{"bytes": n, "seconds": time.Since(start).Seconds()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tester := Tester{Base: srv.URL, Token: "tok", Probe: 64 << 10, Min: 128 << 10, Max: 1 << 20, MaxUpload: 256 << 10, Target: 50 * time.Millisecond}
	res, err := tester.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !auth {
		t.Fatal("missing bearer token or identity encoding")
	}
	if len(pings) != 6 {
		t.Errorf("pings: %d, want warm-up + 5", len(pings))
	}
	if len(downs) != 2 || downs[0] != 64<<10 || downs[1] < 128<<10 || downs[1] > 1<<20 {
		t.Errorf("downloads: %v", downs)
	}
	if upBytes != min(downs[1], 256<<10) {
		t.Errorf("upload %d bytes, download %d", upBytes, downs[1])
	}
	if res.Down <= 0 || res.Up <= 0 || res.Ping < 0 {
		t.Errorf("result: %+v", res)
	}
	if !strings.HasPrefix(res.JSON(), `{"down":`) {
		t.Errorf("JSON: %s", res.JSON())
	}
}

func TestRunErrors(t *testing.T) {
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/speedtest/ping":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/speedtest/down":
			_, _ = w.Write([]byte("too short"))
		}
	}))
	defer short.Close()
	if _, err := (Tester{Base: short.URL, Token: "t"}).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "download") {
		t.Fatalf("short download: %v", err)
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer denied.Close()
	if _, err := (Tester{Base: denied.URL, Token: "t"}).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("forbidden: %v", err)
	}
	lying := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/speedtest/down":
			n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
			_, _ = io.CopyN(w, rand.Reader, n)
		case "/api/v1/agent/speedtest/up":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"bytes":1,"seconds":1}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer lying.Close()
	if _, err := (Tester{Base: lying.URL, Token: "t", Probe: 1024, Min: 2048, Max: 4096}).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "upload") {
		t.Fatalf("incomplete upload: %v", err)
	}
}
