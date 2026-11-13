// Package collect gathers read-only system information for the snapshot the
// agent sends to the Phoedox instance.
package collect

import (
	"os"
	"runtime"
	"time"
)

// Snapshot is the read-only state of one host.
type Snapshot struct {
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	CPUs      int       `json:"cpus"`
	Kernel    string    `json:"kernel,omitempty"`
	Distro    string    `json:"distro,omitempty"`
	UptimeSec int64     `json:"uptimeSec,omitempty"`
	MemTotal  uint64    `json:"memTotalBytes,omitempty"`
	MemAvail  uint64    `json:"memAvailableBytes,omitempty"`
	Disks     []Disk    `json:"disks,omitempty"`
	TakenAt   time.Time `json:"takenAt"`
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
	platform(&s)
	return s
}
