//go:build !(linux || darwin || freebsd)

package terminal

import (
	"errors"
	"runtime"
)

func supported() error {
	if runtime.GOOS == "windows" {
		return errors.New("terminal is not supported on Windows")
	}
	return errors.New("terminal is not supported on " + runtime.GOOS)
}

func startShell(string) (*shellPTY, error) { return nil, supported() }
