package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.phoedox.com/":   "https://app.phoedox.com",
		"https://x.example:8443/phx": "https://x.example:8443/phx",
		"http://localhost:8080":      "http://localhost:8080",
		"http://127.0.0.1:8080/":     "http://127.0.0.1:8080",
	} {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"http://app.phoedox.com", "ftp://x", "app.phoedox.com", ""} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("%s must fail", bad)
		}
	}
}

func TestEnrollAndReport(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/enroll":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["code"] != "phxe_ok" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(Enrolled{AgentID: "a1", Token: "phxa_t", Project: "lab"})
		case "/api/v1/agent/report":
			if r.Header.Get("Authorization") != "Bearer phxa_t" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			json.NewDecoder(r.Body).Decode(&got)
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c := &Client{Base: srv.URL}
	if _, err := c.Enroll(ctx, "phxe_bad", "h"); err == nil {
		t.Fatal("bad code must fail")
	}
	e, err := c.Enroll(ctx, "phxe_ok", "h")
	if err != nil || e.Token != "phxa_t" {
		t.Fatalf("%v %+v", err, e)
	}
	c.Token = e.Token
	if err := c.Report(ctx, map[string]any{"hostname": "h"}); err != nil || got["hostname"] != "h" {
		t.Fatalf("%v %v", err, got)
	}
	c.Token = "phxa_other"
	if err := c.Report(ctx, map[string]any{}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}
