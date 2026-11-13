// Package policy decides whether the agent may run an action. Three checks
// must all pass: the action is allowed in the local config, the job was
// approved in the UI (the instance only sends approved jobs) and the job is
// signed with an admin key the agent trusts.
package policy

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"
)

// Action names.
const (
	ActRead             = "read"
	ActContainerRestart = "container.restart"
	ActContainerUpdate  = "container.update"
	ActStackRedeploy    = "stack.redeploy"
	ActSystemUpdate     = "system.update"
	ActExec             = "exec"
)

// Config is the local allowlist from agent.yaml. Read is always allowed.
type Config struct {
	Allow []string `json:"allow" yaml:"allow"`
	// ExecCommands are glob patterns for exec, e.g. "/usr/local/bin/backup-*".
	ExecCommands []string `json:"execCommands" yaml:"execCommands"`
	// AdminKeys are base64 ed25519 public keys whose signatures are accepted.
	AdminKeys []string `json:"adminKeys" yaml:"adminKeys"`
}

// Job is an approved action sent by the instance.
type Job struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Action  string    `json:"action"`
	Args    []string  `json:"args,omitempty"`
	Expires time.Time `json:"expires"`
}

// SignedJob carries the job and its signature over the canonical JSON.
type SignedJob struct {
	Job       Job    `json:"job"`
	Signature string `json:"signature"`
}

var (
	ErrNotAllowed = errors.New("policy: action not allowed by local config")
	ErrExpired    = errors.New("policy: job expired")
	ErrSignature  = errors.New("policy: no trusted signature")
	ErrWrongAgent = errors.New("policy: job is for another agent")
)

// Sign signs a job (used by the instance and in tests).
func Sign(j Job, key ed25519.PrivateKey) (SignedJob, error) {
	b, err := json.Marshal(j)
	if err != nil {
		return SignedJob{}, err
	}
	return SignedJob{Job: j, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))}, nil
}

// Check verifies a signed job for the agent with the given id.
func (c Config) Check(agentID string, sj SignedJob, now time.Time) error {
	j := sj.Job
	if j.Agent != agentID {
		return ErrWrongAgent
	}
	if !now.Before(j.Expires) {
		return ErrExpired
	}
	if err := c.verify(sj); err != nil {
		return err
	}
	if j.Action == ActRead {
		return nil
	}
	if !contains(c.Allow, j.Action) {
		return fmt.Errorf("%w: %s", ErrNotAllowed, j.Action)
	}
	if j.Action == ActExec {
		if len(j.Args) == 0 || !c.execAllowed(j.Args[0]) {
			return fmt.Errorf("%w: command not in execCommands", ErrNotAllowed)
		}
	}
	return nil
}

func (c Config) verify(sj SignedJob) error {
	sig, err := base64.StdEncoding.DecodeString(sj.Signature)
	if err != nil {
		return ErrSignature
	}
	msg, err := json.Marshal(sj.Job)
	if err != nil {
		return ErrSignature
	}
	for _, k := range c.AdminKeys {
		pub, err := base64.StdEncoding.DecodeString(k)
		if err == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, msg, sig) {
			return nil
		}
	}
	return ErrSignature
}

func (c Config) execAllowed(cmd string) bool {
	for _, p := range c.ExecCommands {
		if ok, _ := path.Match(p, cmd); ok {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
