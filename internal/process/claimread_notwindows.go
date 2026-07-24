//go:build !windows

package process

import (
	"fmt"
	"os"
)

// isTransientClaimReadError is always false off Windows: rename is atomic and a
// reader never observes a sharing violation.
func isTransientClaimReadError(error) bool { return false }

// tightenLegacyClaimPermissions tightens a legacy world/group-readable claim to
// 0600 so its per-launch token is not left readable (design/07). A file that
// vanished between the read and the tighten is a concurrent removal and needs no
// action; a genuine untightenable file still surfaces.
func tightenLegacyClaimPermissions(path string) error {
	fi, statErr := os.Stat(path)
	if statErr != nil || fi.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		return fmt.Errorf("runtime state %s has loose permissions (%v) that cannot be tightened: %w", path, fi.Mode().Perm(), chmodErr)
	}
	return nil
}
