// Package config reads agent.yaml: which actions this host allows and whose
// signatures it trusts. Without the file the agent only reads.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phoedox/phoedox-agent/internal/policy"
	"go.yaml.in/yaml/v3"
)

// File is the name of the configuration inside the agent directory.
const File = "agent.yaml"

var known = map[string]bool{
	policy.ActContainerRestart: true, policy.ActContainerUpdate: true, policy.ActStackRedeploy: true,
	policy.ActSystemUpdate: true, policy.ActExec: true, policy.ActAgentUpdate: true,
	policy.ActWakeOnLAN: true, policy.ActSpeedTest: true, policy.ActTerminal: true,
}

// Load reads dir/agent.yaml; a missing file means "read only".
func Load(dir string) (policy.Config, error) {
	var c policy.Config
	b, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", File, err)
	}
	for _, a := range c.Allow {
		if !known[a] {
			return c, fmt.Errorf("%s: unknown action %q in allow", File, a)
		}
	}
	for _, k := range c.AdminKeys {
		pub, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return c, fmt.Errorf("%s: adminKeys entry %q is not a base64 ed25519 public key", File, k)
		}
	}
	if c.TerminalShell != "" && (!filepath.IsAbs(c.TerminalShell) || filepath.Clean(c.TerminalShell) != c.TerminalShell) {
		return c, fmt.Errorf("%s: terminalShell must be an absolute path", File)
	}
	return c, nil
}
