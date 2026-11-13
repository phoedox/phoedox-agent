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

func TestVirt(t *testing.T) {
	mk := func(files map[string]string) string {
		root := t.TempDir() + "/"
		for p, c := range files {
			os.MkdirAll(filepath.Dir(root+p), 0o755)
			os.WriteFile(root+p, []byte(c), 0o644)
		}
		return root
	}
	cases := []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"run/systemd/container": "lxc\n"}, "lxc"},
		{map[string]string{"proc/1/environ": "PATH=/bin\x00container=lxc\x00"}, "lxc"},
		{map[string]string{".dockerenv": ""}, "container"},
		{map[string]string{"sys/class/dmi/id/sys_vendor": "QEMU", "sys/class/dmi/id/product_name": "Standard PC (Q35 + ICH9, 2009)"}, "vm"},
		{map[string]string{"proc/cpuinfo": "flags : fpu vme hypervisor lahf_lm"}, "vm"},
		{map[string]string{"sys/class/dmi/id/sys_vendor": "Dell Inc."}, ""},
	}
	for _, c := range cases {
		if got := virt(mk(c.files)); got != c.want {
			t.Errorf("%v: got %q, want %q", c.files, got, c.want)
		}
	}
}
