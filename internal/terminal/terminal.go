// Package terminal relays an interactive shell to the instance (the
// "terminal" action). The agent opens an outbound WebSocket to
// <instance>/api/v1/agent/terminal/<session>, starts a shell in a PTY as its
// own user and copies bytes both ways:
//
//   - binary frames from the instance are keyboard input for the PTY,
//   - PTY output goes to the instance as binary frames,
//   - text frames from the instance are JSON control messages:
//     {"type":"resize","cols":120,"rows":30} and {"type":"close"}.
//
// A session ends after MaxDuration, after IdleTimeout without input, when
// the shell exits or the WebSocket closes. The shell's process group is then
// killed and the WebSocket closed with status 1000 and the reason. Terminal
// content is never logged.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Limits of a session.
const (
	DefaultMaxDuration = 60 * time.Minute
	DefaultIdleTimeout = 15 * time.Minute
	minSize, maxSize   = 10, 500
	readLimit          = 1 << 20 // largest frame from the instance (pastes)
)

// Reasons a session ends; sent as the WebSocket close reason.
const (
	ReasonExited       = "exited"
	ReasonIdle         = "idle"
	ReasonTimeLimit    = "time limit"
	ReasonClosed       = "closed"       // {"type":"close"} from the instance
	ReasonDisconnected = "disconnected" // the WebSocket went away
	ReasonStopped      = "agent stopped"
)

var sessionRe = regexp.MustCompile(`^[A-Za-z0-9]{10,64}$`)

// ValidSessionID reports whether id is a well-formed session ID.
func ValidSessionID(id string) bool { return sessionRe.MatchString(id) }

// URL derives the WebSocket URL of a session from the instance URL:
// https → wss, http → ws.
func URL(base, sessionID string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid instance URL %q", base)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("invalid instance URL %q", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/v1/agent/terminal/" + sessionID
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	return u.String(), nil
}

// PTY is the terminal side of a session.
type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// Session connects one WebSocket with one PTY.
type Session struct {
	Conn *websocket.Conn
	PTY  PTY
	// Exited is closed when the shell has exited.
	Exited <-chan struct{}
	// Kill ends the shell and its process group; may be nil.
	Kill        func()
	MaxDuration time.Duration
	IdleTimeout time.Duration
}

type control struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func clamp(v int) int { return min(max(v, minSize), maxSize) }

// Relay copies between the WebSocket and the PTY until the session ends,
// then kills the shell, closes the PTY and the WebSocket and returns the
// reason. ctx ends the session early (agent shutdown).
func (s Session) Relay(ctx context.Context) string {
	maxDur := s.MaxDuration
	if maxDur <= 0 {
		maxDur = DefaultMaxDuration
	}
	idleDur := s.IdleTimeout
	if idleDur <= 0 {
		idleDur = DefaultIdleTimeout
	}
	exited := s.Exited
	if exited == nil {
		exited = make(chan struct{})
	}
	// Reads and writes must not use ctx: cancelling the context of a
	// pending Read makes the library drop the connection without the close
	// frame that carries the reason. Close below ends them instead.
	ioCtx := context.Background()
	s.Conn.SetReadLimit(readLimit)

	var lastInput atomic.Int64
	lastInput.Store(time.Now().UnixNano())

	inDone := make(chan string, 1)
	go func() { inDone <- s.input(ioCtx, &lastInput) }()

	outDone := make(chan struct{})
	var outReason string
	go func() {
		defer close(outDone)
		outReason = s.output(ioCtx)
	}()

	maxT := time.NewTimer(maxDur)
	defer maxT.Stop()
	idleT := time.NewTimer(idleDur)
	defer idleT.Stop()

	var reason string
	for reason == "" {
		select {
		case <-ctx.Done():
			reason = ReasonStopped
		case reason = <-inDone:
		case <-exited:
			reason = ReasonExited
			// Let the last output reach the instance.
			select {
			case <-outDone:
			case <-time.After(time.Second):
			}
		case <-outDone:
			reason = outReason
			if reason == ReasonExited {
				select {
				case <-exited:
				case <-time.After(time.Second):
				}
			}
		case <-maxT.C:
			reason = ReasonTimeLimit
		case <-idleT.C:
			since := time.Since(time.Unix(0, lastInput.Load()))
			if since >= idleDur {
				reason = ReasonIdle
			} else {
				idleT.Reset(idleDur - since)
			}
		}
	}

	if s.Kill != nil {
		s.Kill()
	}
	_ = s.PTY.Close()
	select {
	case <-outDone:
	case <-time.After(2 * time.Second):
	}
	_ = s.Conn.Close(websocket.StatusNormalClosure, reason)
	return reason
}

// input handles frames from the instance until the connection ends or the
// instance asks to close.
func (s Session) input(ctx context.Context, lastInput *atomic.Int64) string {
	for {
		typ, data, err := s.Conn.Read(ctx)
		if err != nil {
			return ReasonDisconnected
		}
		switch typ {
		case websocket.MessageBinary:
			lastInput.Store(time.Now().UnixNano())
			if _, err := s.PTY.Write(data); err != nil {
				return ReasonExited
			}
		case websocket.MessageText:
			var c control
			if json.Unmarshal(data, &c) != nil {
				continue // unknown messages are ignored
			}
			switch c.Type {
			case "resize":
				_ = s.PTY.Resize(clamp(c.Cols), clamp(c.Rows))
			case "close":
				return ReasonClosed
			}
		}
	}
}

// output copies PTY output to the instance; it returns why it stopped.
func (s Session) output(ctx context.Context) string {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.PTY.Read(buf)
		if n > 0 {
			if werr := s.Conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return ReasonDisconnected
			}
		}
		if err != nil {
			return ReasonExited
		}
	}
}

// Options configure Run.
type Options struct {
	Base      string // instance URL
	Token     string // agent token
	UserAgent string
	SessionID string
	// Shell is the configured shell (absolute path) or empty for the default.
	Shell       string
	MaxDuration time.Duration
	IdleTimeout time.Duration
	// HTTP is used for the WebSocket handshake; nil uses the default client.
	HTTP *http.Client
	// Logf logs start and end of a session; nil uses log.Printf.
	Logf func(format string, args ...any)
}

// Run connects to the instance, starts the shell and relays until the
// session ends. The output says how long it took and why it ended.
func Run(ctx context.Context, o Options) (string, error) {
	if !ValidSessionID(o.SessionID) {
		return "", errors.New("invalid terminal session ID")
	}
	if err := supported(); err != nil {
		return "", err
	}
	shell, err := pickShell(o.Shell)
	if err != nil {
		return "", err
	}
	u, err := URL(o.Base, o.SessionID)
	if err != nil {
		return "", err
	}
	logf := o.Logf
	if logf == nil {
		logf = log.Printf
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+o.Token)
	if o.UserAgent != "" {
		h.Set("User-Agent", o.UserAgent)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, _, err := websocket.Dial(dialCtx, u, &websocket.DialOptions{
		HTTPClient:      o.HTTP,
		HTTPHeader:      h,
		CompressionMode: websocket.CompressionDisabled,
	})
	cancel()
	if err != nil {
		return "", fmt.Errorf("connect to the instance: %w", err)
	}
	p, err := startShell(shell)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "shell failed")
		return "", fmt.Errorf("start %s: %w", shell, err)
	}
	logf("terminal session %s started (%s)", o.SessionID, shell)
	start := time.Now()
	reason := Session{Conn: conn, PTY: p, Exited: p.exited, Kill: p.kill,
		MaxDuration: o.MaxDuration, IdleTimeout: o.IdleTimeout}.Relay(ctx)
	d := time.Since(start).Round(time.Second)
	logf("terminal session %s ended after %s (%s)", o.SessionID, d, reason)
	return fmt.Sprintf("session ended after %s (%s)", d, reason), nil
}

// shellPTY is a running shell in a PTY (see startShell per platform).
type shellPTY struct {
	PTY
	exited chan struct{}
	kill   func()
}

// pickShell returns the configured shell or /bin/bash, else /bin/sh.
func pickShell(configured string) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("terminalShell must be an absolute path: %q", configured)
		}
		if !isExecutable(configured) {
			return "", fmt.Errorf("terminalShell %s is not an executable file", configured)
		}
		return configured, nil
	}
	for _, s := range []string{"/bin/bash", "/bin/sh"} {
		if isExecutable(s) {
			return s, nil
		}
	}
	return "", errors.New("no shell found (/bin/bash, /bin/sh); set terminalShell")
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
