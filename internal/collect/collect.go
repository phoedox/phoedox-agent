// Package collect gathers read-only system information for the snapshot the
// agent sends to the Phoedox instance.
package collect

import (
	"net"
	"os"
	"runtime"
	"time"
)

// Snapshot is the read-only state of one host.
type Snapshot struct {
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPUs      int    `json:"cpus"`
	Kernel    string `json:"kernel,omitempty"`
	Distro    string `json:"distro,omitempty"`
	UptimeSec int64  `json:"uptimeSec,omitempty"`
	MemTotal  uint64 `json:"memTotalBytes,omitempty"`
	MemAvail  uint64 `json:"memAvailableBytes,omitempty"`
	Disks     []Disk `json:"disks,omitempty"`
	// Virt is "vm", "lxc", "container" or empty for bare metal / unknown.
	Virt       string      `json:"virt,omitempty"`
	IPs        []string    `json:"ips,omitempty"`
	Containers []Container `json:"containers,omitempty"`
	Packages   []Package   `json:"packages,omitempty"`
	// DockerVersion is the version of the Docker engine, if one answers.
	DockerVersion string    `json:"dockerVersion,omitempty"`
	Version       string    `json:"agentVersion,omitempty"`
	TakenAt       time.Time `json:"takenAt"`
}

// Container is a Docker container on the host.
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	State string `json:"state"`
	Ports string `json:"ports,omitempty"`
	// Compose labels: project (stack), service and its folder.
	Project    string `json:"project,omitempty"`
	Service    string `json:"service,omitempty"`
	ComposeDir string `json:"composeDir,omitempty"`
}

// Disk is the usage of one mount point.
type Disk struct {
	Mount string `json:"mount"`
	Total uint64 `json:"totalBytes"`
	Free  uint64 `json:"freeBytes"`
}

// Take returns a snapshot of the current host. Platform specific details are
// filled in by platform() and missing values are simply left empty.
func Take() Snapshot {
	h, _ := os.Hostname()
	s := Snapshot{Hostname: h, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), TakenAt: time.Now().UTC()}
	s.IPs = addresses()
	platform(&s)
	return s
}

// addresses returns the global unicast addresses of the up interfaces,
// IPv4 first.
func addresses() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var v4, v6 []string
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || !n.IP.IsGlobalUnicast() {
				continue
			}
			if n.IP.To4() != nil {
				v4 = append(v4, n.IP.String())
			} else {
				v6 = append(v6, n.IP.String())
			}
		}
	}
	return append(v4, v6...)
}
