// Package jobs runs actions the instance sent and policy.Check allowed.
// Nothing here decides whether an action is allowed.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/phoedox/phoedox-agent/internal/docker"
	"github.com/phoedox/phoedox-agent/internal/policy"
	"github.com/phoedox/phoedox-agent/internal/terminal"
)

// Result is what the agent reports back.
type Result struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// Runner executes actions. Command is replaceable in tests.
type Runner struct {
	DockerSocket string
	Command      func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookPath     func(string) (string, error)
	// Download fetches a path from the instance (agent updates come only
	// from there, never from elsewhere).
	Download func(ctx context.Context, path string) (io.ReadCloser, error)
	// Executable is the path of the running agent binary.
	Executable func() (string, error)
	// Restart is set after the binary was replaced; the caller exits so the
	// service manager starts the new version.
	Restart bool
	// WOLSend sends a magic packet to addr (host:9); nil sends it three
	// times over UDP, 100 ms apart.
	WOLSend func(ctx context.Context, addr string, pkt []byte) error
	// SpeedTest measures the connection to the instance and returns the
	// result as JSON; nil means the action is not available.
	SpeedTest func(ctx context.Context) (string, error)
	// Terminal runs one terminal session until it ends; nil means the
	// action is not available. It blocks for up to an hour, so the caller
	// runs terminal jobs in their own goroutine.
	Terminal func(ctx context.Context, sessionID string) (string, error)
}

// New returns a runner that uses the real system.
func New() *Runner {
	return &Runner{DockerSocket: docker.Socket(), LookPath: exec.LookPath, Executable: os.Executable,
		Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}}
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

const maxOutput = 8 << 10

// Run executes one job and never panics; errors become a failed result.
func (r *Runner) Run(ctx context.Context, j policy.Job) Result {
	out, err := r.run(ctx, j)
	if len(out) > maxOutput {
		out = "…" + out[len(out)-maxOutput:]
	}
	if err != nil {
		if out != "" {
			out += "\n"
		}
		return Result{OK: false, Output: out + err.Error()}
	}
	return Result{OK: true, Output: out}
}

func (r *Runner) run(ctx context.Context, j policy.Job) (string, error) {
	arg := func(i int) string {
		if i < len(j.Args) {
			return j.Args[i]
		}
		return ""
	}
	switch j.Action {
	case policy.ActContainerRestart:
		if !nameRe.MatchString(arg(0)) {
			return "", errors.New("invalid container name")
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := docker.Restart(ctx, r.DockerSocket, arg(0)); err != nil {
			return "", err
		}
		return "restarted " + arg(0), nil
	case policy.ActStackRedeploy, policy.ActContainerUpdate:
		// args: compose project, project folder[, service]
		project, dir, service := arg(0), arg(1), arg(2)
		if !nameRe.MatchString(project) || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || (service != "" && !nameRe.MatchString(service)) {
			return "", errors.New("invalid compose project, folder or service")
		}
		if j.Action == policy.ActContainerUpdate && service == "" {
			return "", errors.New("container.update needs the compose service")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		base := []string{"compose", "--project-name", project, "--project-directory", dir}
		pull := append(append([]string{}, base...), "pull")
		up := append(append([]string{}, base...), "up", "-d")
		if service != "" {
			pull, up = append(pull, service), append(up, service)
		}
		var log strings.Builder
		for _, a := range [][]string{pull, up} {
			out, err := r.Command(ctx, "docker", a...)
			log.Write(out)
			if err != nil {
				return log.String(), fmt.Errorf("docker %s: %w", strings.Join(a[len(base):], " "), err)
			}
		}
		return log.String(), nil
	case policy.ActSystemUpdate:
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		steps := r.updateSteps()
		if steps == nil {
			return "", errors.New("no supported package manager (apt-get, dnf, apk)")
		}
		var log strings.Builder
		for _, s := range steps {
			out, err := r.Command(ctx, s[0], s[1:]...)
			log.Write(out)
			if err != nil {
				return log.String(), fmt.Errorf("%s: %w", strings.Join(s, " "), err)
			}
		}
		return log.String(), nil
	case policy.ActAgentUpdate:
		return r.update(ctx, arg(0), arg(1))
	case policy.ActWakeOnLAN:
		if len(j.Args) > 2 {
			return "", errors.New("wol takes a MAC and an optional broadcast address")
		}
		return r.wakeOnLAN(ctx, arg(0), arg(1))
	case policy.ActSpeedTest:
		if len(j.Args) != 0 {
			return "", errors.New("speedtest takes no arguments")
		}
		if r.SpeedTest == nil {
			return "", errors.New("speed test is not available")
		}
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		return r.SpeedTest(ctx)
	case policy.ActTerminal:
		if len(j.Args) != 1 || !terminal.ValidSessionID(j.Args[0]) {
			return "", errors.New("terminal needs a session ID")
		}
		if r.Terminal == nil {
			return "", errors.New("terminal is not available")
		}
		return r.Terminal(ctx, j.Args[0])
	case policy.ActExec:
		if len(j.Args) == 0 || !filepath.IsAbs(j.Args[0]) {
			return "", errors.New("exec needs an absolute command path")
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		out, err := r.Command(ctx, j.Args[0], j.Args[1:]...)
		return string(out), err
	}
	return "", fmt.Errorf("unknown action %q", j.Action)
}

const releasePrefix = "/api/v1/agent/releases/"

var sumRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// update replaces the agent binary with the file at path on the instance
// if its SHA-256 matches the signed job; the old binary stays as .old.
func (r *Runner) update(ctx context.Context, path, sum string) (string, error) {
	if !strings.HasPrefix(path, releasePrefix) || strings.Contains(path, "..") || !sumRe.MatchString(sum) {
		return "", errors.New("agent.update needs a release path on the instance and a SHA-256")
	}
	if r.Download == nil || r.Executable == nil {
		return "", errors.New("updates are not available")
	}
	exe, err := r.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	body, err := r.Download(ctx, path)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".phoedox-agent-update-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, 200<<20)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return "", fmt.Errorf("checksum mismatch: got %s, the signed job says %s", got, sum)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	// Windows cannot overwrite a running binary, but it can rename it.
	_ = os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		_ = os.Rename(exe+".old", exe)
		return "", err
	}
	r.Restart = true
	return "updated " + exe + " (sha256 " + sum[:12] + "…); restarting", nil
}

func (r *Runner) updateSteps() [][]string {
	has := func(name string) bool { _, err := r.LookPath(name); return err == nil }
	switch {
	case has("apt-get"):
		return [][]string{{"apt-get", "update"}, {"apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", "upgrade"}}
	case has("dnf"):
		return [][]string{{"dnf", "-y", "upgrade"}}
	case has("apk"):
		return [][]string{{"apk", "upgrade", "--no-cache"}}
	}
	return nil
}
