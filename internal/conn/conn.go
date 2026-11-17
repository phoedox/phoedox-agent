// Package conn delivers jobs from the instance: over one WebSocket when the
// network allows it (jobs pushed at once, results sent back on it), else by
// HTTPS long-poll (ADR-0037, both transports). The agent only connects out;
// actions stay signed and are checked by policy.Check before they run.
package conn

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/phoedox/phoedox-agent/internal/client"
	"github.com/phoedox/phoedox-agent/internal/policy"
)

// Mode chooses the transport: auto (WebSocket, long-poll as fallback), ws
// or poll.
type Mode string

const (
	Auto Mode = "auto"
	WS   Mode = "ws"
	Poll Mode = "poll"
)

// Message is one frame on the WebSocket.
type Message struct {
	Type   string             `json:"type"` // jobs | ping | result | report | pong
	ID     string             `json:"id,omitempty"`
	OK     bool               `json:"ok,omitempty"`
	Output string             `json:"output,omitempty"`
	Report any                `json:"report,omitempty"`
	Jobs   []policy.SignedJob `json:"jobs,omitempty"`
}

// Source hands out jobs and takes results.
type Source struct {
	Client *client.Client
	Mode   Mode
	// HTTP is used for the WebSocket handshake (nil = default client).
	HTTP *http.Client
	// PollFor is how long to stay on long-poll after the upgrade failed
	// (e.g. a proxy without WebSocket support) before trying again.
	PollFor time.Duration
	// MaxBackoff caps the wait between reconnects.
	MaxBackoff time.Duration
	Logf       func(format string, args ...any)

	mu        sync.Mutex
	ws        *websocket.Conn
	failures  int
	pollUntil time.Time
}

func (s *Source) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func (s *Source) wsURL() string {
	u := s.Client.Base + "/api/v1/agent/ws"
	if rest, ok := strings.CutPrefix(u, "https://"); ok {
		return "wss://" + rest
	}
	return "ws://" + strings.TrimPrefix(u, "http://")
}

// errUpgrade means the WebSocket handshake failed (not a dropped connection).
var errUpgrade = errors.New("websocket upgrade failed")

func (s *Source) dial(ctx context.Context) (*websocket.Conn, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+s.Client.Token)
	if s.Client.UserAgent != "" {
		h.Set("User-Agent", s.Client.UserAgent)
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, res, err := websocket.Dial(dctx, s.wsURL(), &websocket.DialOptions{HTTPClient: s.HTTP, HTTPHeader: h, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if res != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			return nil, client.ErrRevoked
		}
		if res != nil {
			return nil, errors.Join(errUpgrade, err)
		}
		return nil, err
	}
	c.SetReadLimit(8 << 20)
	return c, nil
}

func (s *Source) backoff() time.Duration {
	maxWait := s.MaxBackoff
	if maxWait == 0 {
		maxWait = time.Minute
	}
	d := time.Second << min(s.failures, 6)
	return min(d, maxWait)
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Jobs waits for the next jobs. It returns client.ErrRevoked when the
// instance no longer accepts the token.
func (s *Source) Jobs(ctx context.Context) ([]policy.SignedJob, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		usePoll := s.Mode == Poll || (s.Mode != WS && time.Now().Before(s.pollUntil))
		c := s.ws
		s.mu.Unlock()
		if usePoll {
			return s.Client.Jobs(ctx, 25*time.Second)
		}
		if c == nil {
			var err error
			c, err = s.dial(ctx)
			switch {
			case errors.Is(err, client.ErrRevoked):
				return nil, err
			case errors.Is(err, errUpgrade) && s.Mode == Auto:
				// The network does not carry WebSockets: long-poll for a while.
				pollFor := s.PollFor
				if pollFor == 0 {
					pollFor = 10 * time.Minute
				}
				s.mu.Lock()
				s.pollUntil = time.Now().Add(pollFor)
				s.mu.Unlock()
				s.logf("websocket not available (%v), using long-poll", err)
				continue
			case err != nil:
				s.failures++
				d := s.backoff()
				s.logf("websocket: %v, retrying in %s", err, d)
				if err := sleep(ctx, d); err != nil {
					return nil, err
				}
				continue
			}
			s.mu.Lock()
			s.ws, s.failures = c, 0
			s.mu.Unlock()
		}
		var m Message
		if err := wsjson.Read(ctx, c, &m); err != nil {
			s.drop(c)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var ce websocket.CloseError
			if errors.As(err, &ce) && ce.Code == websocket.StatusPolicyViolation {
				return nil, client.ErrRevoked
			}
			s.failures++
			d := s.backoff()
			s.logf("websocket closed: %v, reconnecting in %s", err, d)
			if err := sleep(ctx, d); err != nil {
				return nil, err
			}
			continue
		}
		if m.Type == "jobs" && len(m.Jobs) > 0 {
			return m.Jobs, nil
		}
	}
}

func (s *Source) drop(c *websocket.Conn) {
	s.mu.Lock()
	if s.ws == c {
		s.ws = nil
	}
	s.mu.Unlock()
	c.CloseNow()
}

// Result reports how a job went: over the WebSocket when it is open, else
// over HTTPS.
func (s *Source) Result(ctx context.Context, id string, ok bool, output string) error {
	s.mu.Lock()
	c := s.ws
	s.mu.Unlock()
	if c != nil {
		wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := wsjson.Write(wctx, c, Message{Type: "result", ID: id, OK: ok, Output: output})
		cancel()
		if err == nil {
			return nil
		}
		s.drop(c)
	}
	return s.Client.Result(ctx, id, ok, output)
}

// Connected tells whether the WebSocket is open.
func (s *Source) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ws != nil
}

// Close ends the WebSocket.
func (s *Source) Close() {
	s.mu.Lock()
	c := s.ws
	s.ws = nil
	s.mu.Unlock()
	if c != nil {
		c.Close(websocket.StatusNormalClosure, "")
	}
}
