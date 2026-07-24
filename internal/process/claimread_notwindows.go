//go:build !windows

package process

// isTransientClaimReadError is always false off Windows: rename is atomic and a
// reader never observes a sharing violation.
func isTransientClaimReadError(error) bool { return false }
