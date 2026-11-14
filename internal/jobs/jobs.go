// Package jobs runs actions the instance sent and policy.Check allowed.
// Nothing here decides whether an action is allowed.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/phoedox/phoedox-agent/internal/docker"
	"github.com/phoedox/phoedox-agent/internal/policy"
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
}

// New returns a runner that uses the real system.
func New() *Runner {
	return &Runner{DockerSocket: docker.Socket(), LookPath: exec.LookPath,
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
