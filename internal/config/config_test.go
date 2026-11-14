package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if c, err := Load(dir); err != nil || len(c.Allow) != 0 {
		t.Fatalf("missing file is read-only: %v %+v", err, c)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key := base64.StdEncoding.EncodeToString(pub)
	write := func(s string) { os.WriteFile(filepath.Join(dir, File), []byte(s), 0o600) }
	write("allow:\n  - container.restart\nexecCommands: []\nadminKeys:\n  - " + key + "\nmode: host\n")
	c, err := Load(dir)
	if err != nil || len(c.Allow) != 1 || c.AdminKeys[0] != key {
		t.Fatalf("%v %+v", err, c)
	}
	write("allow: [rm-everything]\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unknown action: %v", err)
	}
	write("allow: [agent.update, wol, speedtest, terminal]\nterminalShell: /bin/zsh\n")
	if c, err := Load(dir); err != nil || len(c.Allow) != 4 || c.TerminalShell != "/bin/zsh" {
		t.Fatalf("new actions: %v %+v", err, c)
	}
	write("allow: [terminal]\nterminalShell: zsh\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "terminalShell") {
		t.Fatalf("relative shell: %v", err)
	}
	write("adminKeys: [nope]\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("bad key must fail")
	}
}
