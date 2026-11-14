//go:build linux

package collect

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	s.Virt = virt("/")
	s.Packages = packages()
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

// virt detects whether the host is a VM, an LXC or another container.
// root is "/" except in tests.
func virt(root string) string {
	read := func(p string) string {
		b, _ := os.ReadFile(root + p)
		return strings.TrimSpace(string(b))
	}
	if c := read("run/systemd/container"); c != "" {
		if c == "lxc" || c == "lxc-libvirt" {
			return "lxc"
		}
		return "container"
	}
	if env := read("proc/1/environ"); strings.Contains(env, "container=lxc") {
		return "lxc"
	}
	if _, err := os.Stat(root + ".dockerenv"); err == nil {
		return "container"
	}
	dmi := strings.ToLower(read("sys/class/dmi/id/sys_vendor") + " " + read("sys/class/dmi/id/product_name"))
	for _, v := range []string{"qemu", "kvm", "vmware", "virtualbox", "microsoft corporation virtual", "xen", "bochs", "parallels", "proxmox"} {
		if strings.Contains(dmi, v) {
			return "vm"
		}
	}
	if strings.Contains(read("proc/cpuinfo"), " hypervisor") {
		return "vm"
	}
	return ""
}

// packages reads the package database: dpkg and apk from their files, rpm
// through the rpm command. Missing tools simply give no list.
func packages() []Package {
	if f, err := os.Open("/var/lib/dpkg/status"); err == nil {
		defer f.Close()
		return parseDpkg(f)
	}
	if f, err := os.Open("/lib/apk/db/installed"); err == nil {
		defer f.Close()
		return parseApk(f)
	}
	if path, err := exec.LookPath("rpm"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, path, "-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}\n").Output()
		if err == nil {
			return parseRpmQuery(bytes.NewReader(out))
		}
	}
	return nil
}
