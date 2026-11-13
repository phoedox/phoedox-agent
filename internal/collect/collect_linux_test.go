//go:build linux

package collect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeminfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meminfo")
	os.WriteFile(p, []byte("MemTotal:       16314740 kB\nMemFree:  1 kB\nMemAvailable:    8000000 kB\n"), 0o644)
	total, avail := meminfo(p)
	if total != 16314740*1024 || avail != 8000000*1024 {
		t.Fatalf("got %d %d", total, avail)
	}
}

func TestOSRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "os-release")
	os.WriteFile(p, []byte("NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n"), 0o644)
	if got := osRelease(p); got != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("got %q", got)
	}
}

func TestTake(t *testing.T) {
	s := Take()
	if s.CPUs < 1 || s.OS != "linux" || s.Hostname == "" {
		t.Fatalf("unexpected snapshot %+v", s)
	}
}
