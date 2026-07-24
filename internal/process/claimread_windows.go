//go:build windows

package process

import (
	"errors"
	"syscall"
)

// isTransientClaimReadError reports whether err is a transient Windows file
// sharing/lock violation: a concurrent writer's rename/replace briefly holds
// runtime.json without read-sharing (atomic and invisible on unix, but not on
// Windows), so a read/tighten should retry rather than fail.
func isTransientClaimReadError(err error) bool {
	const (
		errorSharingViolation = syscall.Errno(32)
		errorLockViolation    = syscall.Errno(33)
	)
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}
