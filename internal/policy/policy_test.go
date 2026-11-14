package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
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

func TestAgentUpdatePolicy(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	keys := []string{base64.StdEncoding.EncodeToString(pub)}
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	job := Job{ID: "u1", Agent: "nas-01", Action: ActAgentUpdate, Args: []string{"/api/v1/agent/releases/v0.3.0/phoedox-agent-linux-amd64", sum}, Expires: now.Add(5 * time.Minute)}
	sj, _ := Sign(job, priv)

	allowed := Config{Allow: []string{ActAgentUpdate}, AdminKeys: keys}
	if err := allowed.Check("nas-01", sj, now); err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if err := (Config{Allow: []string{ActContainerRestart}, AdminKeys: keys}).Check("nas-01", sj, now); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("forbidden: %v", err)
	}
	if err := allowed.Check("nas-01", sj, now.Add(time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	tampered := sj
	tampered.Job.Args = []string{job.Args[0], "f" + sum[1:]} // another binary
	if err := allowed.Check("nas-01", tampered, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered checksum: %v", err)
	}
}

func TestNewActionsPolicy(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	keys := []string{base64.StdEncoding.EncodeToString(pub)}
	cases := map[string][]string{
		ActWakeOnLAN: {"aa:bb:cc:dd:ee:ff", "192.168.1.255"},
		ActSpeedTest: nil,
		ActTerminal:  {"Ab3dE6gH9jK2"},
	}
	for action, args := range cases {
		t.Run(action, func(t *testing.T) {
			job := Job{ID: "n1", Agent: "nas-01", Action: action, Args: args, Expires: now.Add(5 * time.Minute)}
			sj, _ := Sign(job, priv)
			allowed := Config{Allow: []string{action}, AdminKeys: keys}
			if err := allowed.Check("nas-01", sj, now); err != nil {
				t.Fatalf("allowed: %v", err)
			}
			// Every other action is allowed, but not this one.
			others := Config{Allow: []string{ActContainerRestart, ActExec, ActAgentUpdate, ActWakeOnLAN, ActSpeedTest, ActTerminal}, AdminKeys: keys}
			others.Allow = slices.DeleteFunc(others.Allow, func(a string) bool { return a == action })
			if err := others.Check("nas-01", sj, now); !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("not in allow list: %v", err)
			}
			if err := allowed.Check("nas-01", sj, now.Add(5*time.Minute)); !errors.Is(err, ErrExpired) {
				t.Fatalf("expired: %v", err)
			}
			if err := allowed.Check("pve-01", sj, now); !errors.Is(err, ErrWrongAgent) {
				t.Fatalf("wrong agent: %v", err)
			}
			tampered := sj
			tampered.Job.Args = append([]string{}, args...)
			tampered.Job.Args = append(tampered.Job.Args, "x")
			if err := allowed.Check("nas-01", tampered, now); !errors.Is(err, ErrSignature) {
				t.Fatalf("tampered args: %v", err)
			}
			retarget := sj
			retarget.Job.Action = ActTerminal
			if action == ActTerminal {
				retarget.Job.Action = ActSpeedTest
			}
			if err := (Config{Allow: []string{ActTerminal, ActSpeedTest}, AdminKeys: keys}).Check("nas-01", retarget, now); !errors.Is(err, ErrSignature) {
				t.Fatalf("tampered action: %v", err)
			}
		})
	}
}
