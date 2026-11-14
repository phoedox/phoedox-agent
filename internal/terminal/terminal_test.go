package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://app.phoedox.com":        "wss://app.phoedox.com/api/v1/agent/terminal/Ab3dE6gH9jK2",
		"https://example.com/phoedox/":   "wss://example.com/phoedox/api/v1/agent/terminal/Ab3dE6gH9jK2",
		"http://localhost:8080":          "ws://localhost:8080/api/v1/agent/terminal/Ab3dE6gH9jK2",
		"https://example.com:8443?x=1#y": "wss://example.com:8443/api/v1/agent/terminal/Ab3dE6gH9jK2",
	} {
		if got, err := URL(base, "Ab3dE6gH9jK2"); err != nil || got != want {
			t.Errorf("%s: %q %v", base, got, err)
		}
	}
	for _, base := range []string{"ftp://example.com", "example.com", ""} {
		if _, err := URL(base, "Ab3dE6gH9jK2"); err == nil {
			t.Errorf("%q must fail", base)
		}
	}
}

func TestValidSessionID(t *testing.T) {
	for id, ok := range map[string]bool{
		"Ab3dE6gH9j": true, strings.Repeat("a", 64): true, "Ab3dE6gH9": false, strings.Repeat("a", 65): false,
		"Ab3dE6gH9j-": false, "../../etc/x": false, "Ab3dE6gH9j\n": false, "": false,
	} {
		if ValidSessionID(id) != ok {
			t.Errorf("%q: want %v", id, ok)
		}
	}
}

// fakePTY is a shell made of pipes: the test reads what the relay types
// (stdin) and writes what the shell prints (stdout).
type fakePTY struct {
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	resized chan [2]int
	closed  atomic.Bool
}

func newFakePTY() *fakePTY {
	f := &fakePTY{resized: make(chan [2]int, 4)}
	f.stdinR, f.stdinW = io.Pipe()
	f.stdoutR, f.stdoutW = io.Pipe()
	return f
}

func (f *fakePTY) Read(b []byte) (int, error)  { return f.stdoutR.Read(b) }
func (f *fakePTY) Write(b []byte) (int, error) { return f.stdinW.Write(b) }
func (f *fakePTY) Resize(c, r int) error       { f.resized <- [2]int{c, r}; return nil }
func (f *fakePTY) Close() error {
	f.closed.Store(true)
	_ = f.stdoutR.Close()
	return f.stdinW.Close()
}

// instance is the server end of a session in an httptest server.
type instance struct {
	conn   *websocket.Conn
	frames chan []byte // binary frames from the agent
	end    chan error  // the error that ended reading (close frame)
}

func (in *instance) closeReason(t *testing.T) (websocket.StatusCode, string) {
	t.Helper()
	select {
	case err := <-in.end:
		var ce websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code, ce.Reason
		}
		return -1, err.Error()
	case <-time.After(5 * time.Second):
		t.Fatal("no close frame")
		return 0, ""
	}
}

func (in *instance) send(t *testing.T, typ websocket.MessageType, s string) {
	t.Helper()
	if err := in.conn.Write(context.Background(), typ, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

// startInstance runs a WebSocket endpoint; header and path checks go to check.
func startInstance(t *testing.T, check func(r *http.Request)) (*httptest.Server, chan *instance) {
	t.Helper()
	ch := make(chan *instance, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		in := &instance{conn: c, frames: make(chan []byte, 64), end: make(chan error, 1)}
		ch <- in
		for {
			typ, data, err := c.Read(context.Background())
			if err != nil {
				in.end <- err
				return
			}
			if typ == websocket.MessageBinary {
				in.frames <- data
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

// pair dials the test instance and returns both ends.
func pair(t *testing.T) (*websocket.Conn, *instance) {
	t.Helper()
	srv, ch := startInstance(t, nil)
	c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/agent/terminal/Ab3dE6gH9jK2", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, <-ch
}

func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(r, buf); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading PTY input")
	}
	return string(buf)
}

func TestRelay(t *testing.T) {
	conn, in := pair(t)
	p := newFakePTY()
	var killed atomic.Bool
	reason := make(chan string, 1)
	go func() {
		reason <- Session{Conn: conn, PTY: p, Kill: func() { killed.Store(true) }}.Relay(context.Background())
	}()

	// Keyboard input: binary frame → PTY.
	in.send(t, websocket.MessageBinary, "ls -l\n")
	if got := readN(t, p.stdinR, 6); got != "ls -l\n" {
		t.Fatalf("PTY got %q", got)
	}
	// Shell output: PTY → binary frame.
	go p.stdoutW.Write([]byte("total 0\r\n"))
	select {
	case f := <-in.frames:
		if string(f) != "total 0\r\n" {
			t.Fatalf("instance got %q", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no output frame")
	}
	// Resize, clamped to 10..500; garbage is ignored.
	in.send(t, websocket.MessageText, `not json`)
	in.send(t, websocket.MessageText, `{"type":"resize","cols":1000,"rows":3}`)
	in.send(t, websocket.MessageText, `{"type":"resize","cols":120,"rows":30}`)
	for _, want := range [][2]int{{500, 10}, {120, 30}} {
		select {
		case got := <-p.resized:
			if got != want {
				t.Fatalf("resize %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no resize")
		}
	}
	// Close message ends the session.
	in.send(t, websocket.MessageText, `{"type":"close"}`)
	code, msg := in.closeReason(t)
	if code != websocket.StatusNormalClosure || msg != ReasonClosed {
		t.Fatalf("close %d %q", code, msg)
	}
	if r := <-reason; r != ReasonClosed || !killed.Load() || !p.closed.Load() {
		t.Fatalf("reason %q killed %v closed %v", r, killed.Load(), p.closed.Load())
	}
}

func TestRelayIdle(t *testing.T) {
	conn, in := pair(t)
	p := newFakePTY()
	go io.Copy(io.Discard, p.stdinR)
	const idle = 300 * time.Millisecond
	reason := make(chan string, 1)
	start := time.Now()
	go func() { reason <- Session{Conn: conn, PTY: p, IdleTimeout: idle}.Relay(context.Background()) }()

	// Input keeps the session alive; resize messages do not count as input.
	time.Sleep(idle / 2)
	in.send(t, websocket.MessageBinary, "x")
	lastInput := time.Now()
	in.send(t, websocket.MessageText, `{"type":"resize","cols":80,"rows":24}`)

	code, msg := in.closeReason(t)
	ended := time.Now()
	if code != websocket.StatusNormalClosure || msg != ReasonIdle {
		t.Fatalf("close %d %q", code, msg)
	}
	if ended.Sub(lastInput) < idle-20*time.Millisecond || ended.Sub(start) < idle+idle/2-20*time.Millisecond {
		t.Fatalf("ended %s after the last input, %s after start", ended.Sub(lastInput), ended.Sub(start))
	}
	if r := <-reason; r != ReasonIdle {
		t.Fatalf("reason %q", r)
	}
}

func TestRelayEnds(t *testing.T) {
	cases := []struct {
		name string
		want string
		run  func(p *fakePTY, exited chan struct{}, in *instance, cancel context.CancelFunc) Session
	}{
		{"shell exits", ReasonExited, func(p *fakePTY, exited chan struct{}, _ *instance, _ context.CancelFunc) Session {
			go func() {
				p.stdoutW.Write([]byte("logout\r\n"))
				close(exited)
				p.stdoutW.Close()
			}()
			return Session{}
		}},
		{"time limit", ReasonTimeLimit, func(*fakePTY, chan struct{}, *instance, context.CancelFunc) Session {
			return Session{MaxDuration: 100 * time.Millisecond}
		}},
		{"agent stops", ReasonStopped, func(_ *fakePTY, _ chan struct{}, _ *instance, cancel context.CancelFunc) Session {
			time.AfterFunc(50*time.Millisecond, cancel)
			return Session{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, in := pair(t)
			p := newFakePTY()
			exited := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := tc.run(p, exited, in, cancel)
			s.Conn, s.PTY, s.Exited = conn, p, exited
			reason := make(chan string, 1)
			go func() { reason <- s.Relay(ctx) }()
			code, msg := in.closeReason(t)
			if code != websocket.StatusNormalClosure || msg != tc.want {
				t.Fatalf("close %d %q", code, msg)
			}
			if r := <-reason; r != tc.want {
				t.Fatalf("reason %q", r)
			}
			if tc.want == ReasonExited {
				select {
				case f := <-in.frames:
					if string(f) != "logout\r\n" {
						t.Fatalf("last output %q", f)
					}
				default:
					t.Fatal("last output lost")
				}
			}
		})
	}

	t.Run("instance disconnects", func(t *testing.T) {
		conn, in := pair(t)
		p := newFakePTY()
		reason := make(chan string, 1)
		go func() { reason <- Session{Conn: conn, PTY: p}.Relay(context.Background()) }()
		_ = in.conn.Close(websocket.StatusGoingAway, "bye")
		select {
		case r := <-reason:
			if r != ReasonDisconnected {
				t.Fatalf("reason %q", r)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("relay did not end")
		}
	})
}

func TestRunShell(t *testing.T) {
	if supported() != nil || !isExecutable("/bin/sh") {
		t.Skip("no PTY support or /bin/sh")
	}
	var mu sync.Mutex
	var auth, path string
	srv, ch := startInstance(t, func(r *http.Request) {
		mu.Lock()
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		mu.Unlock()
	})
	var logs []string
	result := make(chan string, 1)
	go func() {
		out, err := Run(context.Background(), Options{Base: srv.URL, Token: "tok", SessionID: "Ab3dE6gH9jK2", Shell: "/bin/sh",
			Logf: func(f string, a ...any) { logs = append(logs, f) }})
		if err != nil {
			out = err.Error()
		}
		result <- out
	}()
	in := <-ch
	in.send(t, websocket.MessageText, `{"type":"resize","cols":100,"rows":40}`)
	in.send(t, websocket.MessageBinary, "stty size; echo hi-$((40+2)); exit\n")
	var out bytes.Buffer
	deadline := time.After(10 * time.Second)
	for !strings.Contains(out.String(), "hi-42") || !strings.Contains(out.String(), "40 100") {
		select {
		case f := <-in.frames:
			out.Write(f)
		case <-deadline:
			t.Fatalf("output so far: %q", out.String())
		}
	}
	code, msg := in.closeReason(t)
	if code != websocket.StatusNormalClosure || msg != ReasonExited {
		t.Fatalf("close %d %q", code, msg)
	}
	if got := <-result; !strings.HasPrefix(got, "session ended after ") || !strings.HasSuffix(got, "(exited)") {
		t.Fatalf("result %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer tok" || path != "/api/v1/agent/terminal/Ab3dE6gH9jK2" {
		t.Fatalf("auth %q path %q", auth, path)
	}
	if len(logs) != 2 {
		t.Fatalf("logs: %v", logs)
	}
}

func TestRunRejects(t *testing.T) {
	if _, err := Run(context.Background(), Options{Base: "https://example.com", SessionID: "bad id"}); err == nil {
		t.Fatal("bad session id")
	}
	if supported() != nil {
		return
	}
	if _, err := Run(context.Background(), Options{Base: "https://example.com", SessionID: "Ab3dE6gH9jK2", Shell: "sh"}); err == nil {
		t.Fatal("relative shell")
	}
	if _, err := Run(context.Background(), Options{Base: "https://example.com", SessionID: "Ab3dE6gH9jK2", Shell: "/nonexistent/shell"}); err == nil {
		t.Fatal("missing shell")
	}
}
