// Package state keeps what enrollment returns: instance, agent ID and token.
// The file is readable by its owner only.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// File is the name of the credentials file inside the agent directory.
const File = "credentials.json"

// State is the result of `phoedox-agent enroll`.
type State struct {
	Instance string `json:"instance"`
	AgentID  string `json:"agentId"`
	Token    string `json:"token"`
	Project  string `json:"project"`
}

// Dir is the agent directory: $PHOEDOX_AGENT_DIR, else /etc/phoedox-agent
// (%ProgramData%\phoedox-agent on Windows).
func Dir() string {
	if d := os.Getenv("PHOEDOX_AGENT_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "phoedox-agent")
	}
	return "/etc/phoedox-agent"
}

// ErrNotEnrolled means there is no credentials file yet.
var ErrNotEnrolled = errors.New("not enrolled: run `phoedox-agent enroll <url> <code>` first")

// Load reads the credentials from dir.
func Load(dir string) (State, error) {
	var s State
	b, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, os.ErrNotExist) {
		return s, ErrNotEnrolled
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.Instance == "" || s.Token == "" {
		return s, ErrNotEnrolled
	}
	return s, nil
}

// Save writes the credentials atomically with mode 0600.
func Save(dir string, s State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, File))
}
