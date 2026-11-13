//go:build linux

package collect

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func platform(s *Snapshot) {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s.Kernel = strings.TrimSpace(string(b))
	}
	s.Distro = osRelease("/etc/os-release")
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				s.UptimeSec = int64(v)
			}
		}
	}
	s.MemTotal, s.MemAvail = meminfo("/proc/meminfo")
	var st syscall.Statfs_t
	if syscall.Statfs("/", &st) == nil {
		s.Disks = append(s.Disks, Disk{Mount: "/", Total: st.Blocks * uint64(st.Bsize), Free: st.Bavail * uint64(st.Bsize)})
	}
}

// osRelease returns PRETTY_NAME from an os-release file.
func osRelease(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// meminfo parses MemTotal and MemAvailable (kB) from /proc/meminfo.
func meminfo(path string) (total, avail uint64) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return total, avail
}
