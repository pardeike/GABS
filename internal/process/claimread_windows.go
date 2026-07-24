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
		errorAccessDenied     = syscall.Errno(5)  // a delete-pending file: opens are denied until the delete completes
		errorSharingViolation = syscall.Errno(32) // a concurrent writer's rename briefly holds it without read-sharing
		errorLockViolation    = syscall.Errno(33)
	)
	return errors.Is(err, errorAccessDenied) ||
		errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation)
}

// tightenLegacyClaimPermissions is a no-op on Windows: os.Stat reports every
// file as 0666 (Windows has no unix permission bits), so the legacy 0644→0600
// tighten would run on every read, is meaningless, and — under the concurrent
// writer that holds the file — fails with "Access is denied". The per-launch
// token is protected by the NTFS ACLs inherited from the private %USERPROFILE%\
// .gabs directory, not by unix permission bits.
func tightenLegacyClaimPermissions(string) error { return nil }
