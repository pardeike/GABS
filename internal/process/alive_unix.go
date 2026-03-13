//go:build !windows

package process

import (
	"os"
	"syscall"
)

// isProcessAlive checks whether the process with the given PID is still running.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
