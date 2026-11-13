//go:build !linux

package collect

// platform: Windows, macOS and FreeBSD collectors follow; basic fields only.
func platform(*Snapshot) {}
