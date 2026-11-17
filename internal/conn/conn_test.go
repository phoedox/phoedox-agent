package conn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/phoedox/phoedox-agent/internal/client"
	"github.com/phoedox/phoedox-agent/internal/policy"
)

type fake struct {
	mu        sync.Mutex
	noWS      bool // answer the upgrade with 404 (a proxy without WebSockets)
	dropFirst bool // close the first WebSocket after the handshake
	conns     atomic.Int32
	polls     atomic.Int32
	results   []string // "ws:id" or "http:id"
}

func (f *fake) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	job := []policy.SignedJob{{Job: policy.Job{ID: "j1", Action: "container.restart"}, Signature: "sig"}}
	mux.HandleFunc("/api/v1/agent/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.noWS {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		n := f.conns.Add(1)
		if f.dropFirst && n == 1 {
			c.Close(websocket.StatusGoingAway, "restart")
			return
		}
		ctx := r.Context()
		_ = wsjson.Write(ctx, c, Message{Type: "ping"})
		_ = wsjson.Write(ctx, c, Message{Type: "jobs", Jobs: job})
		for {
			var m Message
			if err := wsjson.Read(ctx, c, &m); err != nil {
				return
			}
			if m.Type == "result" {
				f.mu.Lock()
				f.results = append(f.results, "ws:"+m.ID)
				f.mu.Unlock()
			}
		}
	})
	mux.HandleFunc("/api/v1/agent/jobs", func(w http.ResponseWriter, r *http.Request) {
		f.polls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": job})
	})
	mux.HandleFunc("/api/v1/agent/jobs/j1/result", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.results = append(f.results, "http:j1")
		f.mu.Unlock()
		_, _ = w.Write([]byte("{}"))
	})
	return mux
}

func source(srv *httptest.Server, token string, mode Mode) *Source {
	return &Source{Client: &client.Client{Base: srv.URL, Token: token}, Mode: mode, MaxBackoff: 50 * time.Millisecond, Logf: func(string, ...any) {}}
}

func (f *fake) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.results...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestJobOverWebSocket(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	s := source(srv, "good", Auto)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobs, err := s.Jobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Job.ID != "j1" || !s.Connected() {
		t.Fatalf("jobs: %v %+v", err, jobs)
	}
	if err := s.Result(ctx, "j1", true, "ok"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { r := f.got(); return len(r) == 1 && r[0] == "ws:j1" })
	if f.polls.Load() != 0 {
		t.Fatal("must not long-poll while the WebSocket works")
	}
}

func TestFallbackToLongPoll(t *testing.T) {
	f := &fake{noWS: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	s := source(srv, "good", Auto)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobs, err := s.Jobs(ctx)
	if err != nil || len(jobs) != 1 || f.polls.Load() != 1 || s.Connected() {
		t.Fatalf("fallback: %v %+v polls=%d", err, jobs, f.polls.Load())
	}
	if err := s.Result(ctx, "j1", true, "ok"); err != nil {
		t.Fatal(err)
	}
	if r := f.got(); len(r) != 1 || r[0] != "http:j1" {
		t.Fatalf("result over https: %v", r)
	}
	// The next call stays on long-poll without trying the upgrade again.
	if _, err := s.Jobs(ctx); err != nil || f.polls.Load() != 2 {
		t.Fatalf("still polling: %v %d", err, f.polls.Load())
	}
}

func TestReconnect(t *testing.T) {
	f := &fake{dropFirst: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	s := source(srv, "good", WS)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobs, err := s.Jobs(ctx)
	if err != nil || len(jobs) != 1 || f.conns.Load() != 2 {
		t.Fatalf("reconnect: %v %+v conns=%d", err, jobs, f.conns.Load())
	}
}

func TestRejectedToken(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := source(srv, "bad", Auto).Jobs(ctx); !errors.Is(err, client.ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}
