// Package client talks to the Phoedox instance over HTTPS: enrollment and
// read-only reports (ADR-0033 stage 1).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/phoedox/phoedox-agent/internal/policy"
)

// ErrRevoked means the instance no longer accepts the token.
var ErrRevoked = errors.New("the instance rejected the agent token (revoked?)")

// Client is a small HTTPS client for one instance.
type Client struct {
	Base      string
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// Normalize checks an instance URL: https, or http only for loopback hosts.
func Normalize(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid instance URL %q", raw)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", fmt.Errorf("instance URL must use https: %q", raw)
	}
	return u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/"), nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	return c.do(ctx, http.MethodPost, path, in, out)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusUnauthorized:
		if c.Token != "" {
			return ErrRevoked
		}
		return errors.New("the enrollment code is invalid, expired or used up")
	case res.StatusCode >= 300:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return fmt.Errorf("instance: %s", e.Error)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Download streams a file from the instance (agent releases). path is
// absolute on the instance, e.g. /api/v1/agent/releases/….
func (c *Client) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("instance: %s", res.Status)
	}
	return res.Body, nil
}

// Enrolled is the instance's answer to an enrollment.
type Enrolled struct {
	AgentID string `json:"agentId"`
	Token   string `json:"token"`
	Project string `json:"project"`
}

// Enroll exchanges an enrollment code for an agent token.
func (c *Client) Enroll(ctx context.Context, code, hostname string) (Enrolled, error) {
	var e Enrolled
	err := c.post(ctx, "/agent/enroll", map[string]string{"code": code, "hostname": hostname}, &e)
	return e, err
}

// Report sends a snapshot.
func (c *Client) Report(ctx context.Context, snapshot any) error {
	return c.post(ctx, "/agent/report", snapshot, nil)
}

// Jobs waits up to wait for approved, signed jobs of this agent.
func (c *Client) Jobs(ctx context.Context, wait time.Duration) ([]policy.SignedJob, error) {
	var out struct {
		Jobs []policy.SignedJob `json:"jobs"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/agent/jobs?wait=%d", int(wait.Seconds())), nil, &out)
	return out.Jobs, err
}

// Result reports how a job went.
func (c *Client) Result(ctx context.Context, id string, ok bool, output string) error {
	return c.post(ctx, "/agent/jobs/"+url.PathEscape(id)+"/result", map[string]any{"ok": ok, "output": output}, nil)
}
