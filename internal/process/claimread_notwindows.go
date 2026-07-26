//go:build !windows

package process

import (
	"fmt"
	"os"
)

// isTransientClaimReadError is always false off Windows: rename is atomic and a
// reader never observes a sharing violation.
func isTransientClaimReadError(error) bool { return false }

// tightenLegacyClaimHandle tightens a legacy world/group-readable claim to
// 0600 so its per-launch token is not left readable (design/07). It operates
// on the already-validated open handle, never the pathname, so a concurrent
// swap to a symlink can never redirect the chmod; a genuine untightenable
// file still surfaces.
func tightenLegacyClaimHandle(f *os.File, fi os.FileInfo) error {
	if fi.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if chmodErr := f.Chmod(0o600); chmodErr != nil {
		return fmt.Errorf("runtime state %s has loose permissions (%v) that cannot be tightened: %w", f.Name(), fi.Mode().Perm(), chmodErr)
	}
	return nil
}
