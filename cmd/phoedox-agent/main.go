// Command phoedox-agent runs on servers, VMs and LXC containers and reports
// read-only snapshots to a Phoedox instance over outbound HTTPS (ADR-0033,
// stage 1). Actions follow in a later stage and always pass policy.Check.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"os/user"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/phoedox/phoedox-agent/internal/client"
	"github.com/phoedox/phoedox-agent/internal/collect"
	"github.com/phoedox/phoedox-agent/internal/config"
	"github.com/phoedox/phoedox-agent/internal/docker"
	"github.com/phoedox/phoedox-agent/internal/homeassistant"
	"github.com/phoedox/phoedox-agent/internal/jobs"
	"github.com/phoedox/phoedox-agent/internal/policy"
	"github.com/phoedox/phoedox-agent/internal/speedtest"
	"github.com/phoedox/phoedox-agent/internal/state"
	"github.com/phoedox/phoedox-agent/internal/terminal"
)

var version = "dev"

const usage = `phoedox-agent – reports this host to Phoedox

Usage:
  phoedox-agent collect                 Print the current snapshot as JSON
  phoedox-agent enroll <url> <code>     Join a project with an enrollment code
  phoedox-agent run [-interval 60s]     Report every interval until stopped
  phoedox-agent report                  Send one report and exit
  phoedox-agent policy                  Show the allowed actions and trusted keys
  phoedox-agent version

The credentials are stored in $PHOEDOX_AGENT_DIR (default /etc/phoedox-agent).
Actions run only if agent.yaml there allows them and the job carries the
signature of a key listed under adminKeys. "terminal" gives signed jobs an
interactive shell as the agent's user: allow it only deliberately.`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "collect":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(snapshot(ctx))
	case "enroll":
		err = enroll(ctx, os.Args[2:])
	case "run":
		err = run(ctx, os.Args[2:])
	case "report":
		err = reportOnce(ctx)
	case "policy":
		err = showPolicy()
	case "version":
		fmt.Println("phoedox-agent", version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "phoedox-agent:", err)
		os.Exit(1)
	}
}

// ha is the Home Assistant in this network, if agent.yaml names one.
var ha *homeassistant.Client

// snapshot reads the host; Docker and Home Assistant are optional.
func snapshot(ctx context.Context) collect.Snapshot {
	s := collect.Take()
	s.Version = version
	if ha != nil {
		s.HomeAssistant = ha.Read(ctx, time.Now())
	}
	if cs, err := docker.Containers(ctx, docker.Socket()); err == nil {
		s.Containers = cs
		s.DockerVersion, _ = docker.Version(ctx, docker.Socket())
	}
	return s
}

func newClient(base, token string) *client.Client {
	return &client.Client{Base: base, Token: token, UserAgent: "phoedox-agent/" + version}
}

func enroll(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: phoedox-agent enroll <url> <code>")
	}
	base, err := client.Normalize(args[0])
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	e, err := newClient(base, "").Enroll(ctx, args[1], host)
	if err != nil {
		return err
	}
	dir := state.Dir()
	if err := state.Save(dir, state.State{Instance: base, AgentID: e.AgentID, Token: e.Token, Project: e.Project}); err != nil {
		return fmt.Errorf("save credentials in %s: %w", dir, err)
	}
	fmt.Printf("Enrolled %s in project %q. Start reporting with: phoedox-agent run\n", host, e.Project)
	return nil
}

func reportOnce(ctx context.Context) error {
	st, err := state.Load(state.Dir())
	if err != nil {
		return err
	}
	return newClient(st.Instance, st.Token).Report(ctx, snapshot(ctx))
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	interval := fs.Duration("interval", time.Minute, "time between reports (at least 15s)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*interval = max(*interval, 15*time.Second)
	st, err := state.Load(state.Dir())
	if err != nil {
		return err
	}
	c := newClient(st.Instance, st.Token)
	cfg, err := config.Load(state.Dir())
	if err != nil {
		return err
	}
	if cfg.HomeAssistant.Enabled() {
		ha = &homeassistant.Client{URL: cfg.HomeAssistant.URL, TokenFile: cfg.HomeAssistant.TokenFile}
		log.Printf("reading Home Assistant at %s", cfg.HomeAssistant.URL)
	}
	log.Printf("reporting to %s (project %s) every %s", st.Instance, st.Project, *interval)
	if len(cfg.Allow) > 0 && len(cfg.AdminKeys) > 0 {
		log.Printf("accepting signed jobs for: %v", cfg.Allow)
		if slices.Contains(cfg.Allow, policy.ActTerminal) {
			log.Printf("terminal is allowed: signed jobs can open a shell as %s", currentUser())
		}
		go jobLoop(ctx, c, st, cfg)
		defer waitSessions(5 * time.Second)
	}
	failures := 0
	for {
		wait := *interval
		switch err := c.Report(ctx, snapshot(ctx)); {
		case errors.Is(err, client.ErrRevoked):
			return err
		case err != nil && ctx.Err() == nil:
			failures++
			// Back off up to 15 minutes while the instance is unreachable.
			wait = min(*interval*time.Duration(1<<min(failures, 4)), 15*time.Minute)
			log.Printf("report failed (%d): %v; next try in %s", failures, err, wait)
		default:
			failures = 0
		}
		// A little jitter so many hosts do not report in the same second.
		wait += time.Duration(rand.Int64N(int64(wait / 10)))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func showPolicy() error {
	cfg, err := config.Load(state.Dir())
	if err != nil {
		return err
	}
	fmt.Println("allowed actions:", append([]string{policy.ActRead}, cfg.Allow...))
	fmt.Println("exec commands:  ", cfg.ExecCommands)
	fmt.Println("trusted keys:   ", len(cfg.AdminKeys))
	if slices.Contains(cfg.Allow, policy.ActTerminal) {
		shell := cfg.TerminalShell
		if shell == "" {
			shell = "/bin/bash or /bin/sh"
		}
		fmt.Printf("terminal:        ALLOWED – a signed job opens an interactive shell (%s) as %s,\n", shell, currentUser())
		fmt.Println("                 relayed to the instance; sessions end after 60 min or 15 min without input")
	} else {
		fmt.Printf("terminal:        not allowed (add \"terminal\" to allow in %s to permit a web shell as %s)\n", config.File, currentUser())
	}
	return nil
}

// currentUser names the user the agent (and so a terminal) runs as.
func currentUser() string {
	if u, err := user.Current(); err == nil {
		return fmt.Sprintf("user %s (uid %s)", u.Username, u.Uid)
	}
	return "the agent's user"
}

// sessions tracks running terminal sessions so shutdown can wait for them
// to kill their shells.
var sessions sync.WaitGroup

// maxSessions limits concurrent terminal sessions.
const maxSessions = 4

func waitSessions(d time.Duration) {
	done := make(chan struct{})
	go func() { sessions.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// jobLoop waits for signed jobs and runs those that pass policy.Check.
func jobLoop(ctx context.Context, c *client.Client, st state.State, cfg policy.Config) {
	runner := jobs.New()
	runner.Download = c.Download
	runner.SpeedTest = func(ctx context.Context) (string, error) {
		res, err := speedtest.Tester{Base: st.Instance, Token: st.Token, UserAgent: c.UserAgent}.Run(ctx)
		if err != nil {
			return "", err
		}
		return res.JSON(), nil
	}
	runner.Terminal = func(ctx context.Context, id string) (string, error) {
		return terminal.Run(ctx, terminal.Options{Base: st.Instance, Token: st.Token, UserAgent: c.UserAgent,
			SessionID: id, Shell: cfg.TerminalShell})
	}
	if ha != nil {
		runner.HACall = ha.Call
	}
	slots := make(chan struct{}, maxSessions)
	report := func(id string, res jobs.Result) {
		// The result is reported even while the agent stops.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := c.Result(rctx, id, res.OK, res.Output); err != nil {
			log.Printf("job %s: report result: %v", id, err)
		}
	}
	dir := state.Dir()
	for ctx.Err() == nil {
		list, err := c.Jobs(ctx, 25*time.Second)
		if errors.Is(err, client.ErrRevoked) {
			return
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}
		for _, sj := range list {
			if state.Done(dir, sj.Job.ID) {
				continue // a signed job runs once, even if sent again
			}
			_ = state.MarkDone(dir, sj.Job.ID)
			res := jobs.Result{OK: false}
			if err := cfg.Check(st.AgentID, sj, time.Now()); err != nil {
				res.Output = err.Error()
				log.Printf("job %s (%s) refused: %v", sj.Job.ID, sj.Job.Action, err)
			} else if sj.Job.Action == policy.ActTerminal {
				// A session lasts up to an hour: run it beside the other
				// jobs and report when it ends.
				select {
				case slots <- struct{}{}:
					log.Printf("job %s: %s %v", sj.Job.ID, sj.Job.Action, sj.Job.Args)
					sessions.Add(1)
					go func(j policy.Job) {
						defer sessions.Done()
						defer func() { <-slots }()
						report(j.ID, runner.Run(ctx, j))
					}(sj.Job)
					continue
				default:
					res.Output = fmt.Sprintf("too many terminal sessions (at most %d)", maxSessions)
					log.Printf("job %s (%s) refused: %s", sj.Job.ID, sj.Job.Action, res.Output)
				}
			} else {
				log.Printf("job %s: %s %v", sj.Job.ID, sj.Job.Action, sj.Job.Args)
				res = runner.Run(ctx, sj.Job)
			}
			report(sj.Job.ID, res)
			if runner.Restart {
				// The service manager (systemd Restart=always, Docker restart
				// policy) starts the new binary.
				log.Printf("agent updated, exiting to restart")
				os.Exit(0)
			}
		}
	}
}
