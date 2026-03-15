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
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but we can't signal it
	return errors.Is(err, syscall.EPERM)
}
