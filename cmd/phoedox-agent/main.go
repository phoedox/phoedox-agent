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
	"syscall"
	"time"

	"github.com/phoedox/phoedox-agent/internal/client"
	"github.com/phoedox/phoedox-agent/internal/collect"
	"github.com/phoedox/phoedox-agent/internal/docker"
	"github.com/phoedox/phoedox-agent/internal/state"
)

var version = "dev"

const usage = `phoedox-agent – reports this host to Phoedox

Usage:
  phoedox-agent collect                 Print the current snapshot as JSON
  phoedox-agent enroll <url> <code>     Join a project with an enrollment code
  phoedox-agent run [-interval 60s]     Report every interval until stopped
  phoedox-agent report                  Send one report and exit
  phoedox-agent version

The credentials are stored in $PHOEDOX_AGENT_DIR (default /etc/phoedox-agent).`

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

// snapshot reads the host; Docker is optional.
func snapshot(ctx context.Context) collect.Snapshot {
	s := collect.Take()
	s.Version = version
	if cs, err := docker.Containers(ctx, docker.DefaultSocket); err == nil {
		s.Containers = cs
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
	log.Printf("reporting to %s (project %s) every %s", st.Instance, st.Project, *interval)
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
