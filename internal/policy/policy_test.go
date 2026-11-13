package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	cfg := Config{
		Allow:        []string{ActContainerRestart, ActExec},
		ExecCommands: []string{"/usr/local/bin/backup-*"},
		AdminKeys:    []string{base64.StdEncoding.EncodeToString(pub)},
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	job := func(action string, args ...string) Job {
		return Job{ID: "j1", Agent: "pve-01", Action: action, Args: args, Expires: now.Add(5 * time.Minute)}
	}
	sign := func(j Job, k ed25519.PrivateKey) SignedJob { s, _ := Sign(j, k); return s }

	cases := []struct {
		name string
		sj   SignedJob
		at   time.Time
		want error
	}{
		{"read always allowed", sign(job(ActRead), priv), now, nil},
		{"allowed restart", sign(job(ActContainerRestart, "nextcloud"), priv), now, nil},
		{"system update not in allowlist", sign(job(ActSystemUpdate), priv), now, ErrNotAllowed},
		{"exec allowed pattern", sign(job(ActExec, "/usr/local/bin/backup-nas"), priv), now, nil},
		{"exec other command", sign(job(ActExec, "/bin/rm"), priv), now, ErrNotAllowed},
		{"untrusted key", sign(job(ActContainerRestart), other), now, ErrSignature},
		{"expired", sign(job(ActContainerRestart), priv), now.Add(time.Hour), ErrExpired},
		{"other agent", sign(Job{Agent: "nas-01", Action: ActRead, Expires: now.Add(time.Minute)}, priv), now, ErrWrongAgent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cfg.Check("pve-01", tc.sj, tc.at)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("tampered job", func(t *testing.T) {
		sj := sign(job(ActContainerRestart, "nextcloud"), priv)
		sj.Job.Action = ActExec
		if !errors.Is(cfg.Check("pve-01", sj, now), ErrSignature) {
			t.Fatal("tampered job must fail signature check")
		}
	})
}
