//go:build !windows

package config

import (
	"errors"
	"os"
	"syscall"
)

func serverLockProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	sigErr := process.Signal(syscall.Signal(0))
	if sigErr == nil {
		return true
	}
	if errors.Is(sigErr, syscall.EPERM) {
		return true
	}
	if errors.Is(sigErr, syscall.ESRCH) {
		return false
	}
	return false
}
