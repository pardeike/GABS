//go:build !windows

package process

import (
	"errors"
	"os"
	"syscall"
)

// isProcessAlive checks whether the process with the given PID is still running.
// Signal(0) returns nil if we can signal the process, EPERM if it exists but we
// lack permission, and ESRCH if it doesn't exist.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	sigErr := process.Signal(syscall.Signal(0))
	if sigErr == nil {
		return true
	}
	if errors.Is(sigErr, syscall.EPERM) {
		// Process exists but we don't have permission to signal it.
		return true
	}
	if errors.Is(sigErr, syscall.ESRCH) {
		// No such process.
		return false
	}
	// For any other error, conservatively report that the process is not alive.
	return false
}
