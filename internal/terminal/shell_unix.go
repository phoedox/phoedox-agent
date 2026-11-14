//go:build linux || darwin || freebsd

package terminal

import (
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
)

func supported() error { return nil }

type unixPTY struct{ f *os.File }

func (p unixPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p unixPTY) Close() error                { return p.f.Close() }
func (p unixPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// startShell starts shell as a login shell in a new session (its own
// process group) with the PTY as controlling terminal, as the agent's user.
func startShell(shell string) (*shellPTY, error) {
	home := "/"
	env := os.Environ()
	set := map[string]string{"TERM": "xterm-256color", "SHELL": shell}
	if u, err := user.Current(); err == nil {
		if fi, err := os.Stat(u.HomeDir); err == nil && fi.IsDir() {
			home = u.HomeDir
		}
		set["USER"], set["LOGNAME"] = u.Username, u.Username
	} else if h, err := os.UserHomeDir(); err == nil {
		if fi, err := os.Stat(h); err == nil && fi.IsDir() {
			home = h
		}
	}
	if home != "/" {
		set["HOME"] = home
	}
	env = withEnv(env, set)

	cmd := exec.Command(shell, "-l")
	cmd.Env = env
	cmd.Dir = home
	// pty.Start puts the shell into a new session (Setsid) with the PTY as
	// its controlling terminal, so its process group id is its pid.
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	pgid := cmd.Process.Pid
	kill := func() {
		_ = syscall.Kill(-pgid, syscall.SIGHUP)
		select {
		case <-exited:
		case <-time.After(time.Second):
		}
		// Background jobs that ignored SIGHUP.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return &shellPTY{PTY: unixPTY{f}, exited: exited, kill: kill}, nil
}

// withEnv replaces or adds the variables in set.
func withEnv(env []string, set map[string]string) []string {
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}
