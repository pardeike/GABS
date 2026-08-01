package version

import (
	"strings"
	"testing"
)

// TestVersionIsNotAPreProfileRelease guards the release identity. The
// launch-profile work shipped for a while under the same "1.0.8" string as the
// pre-profile release, so `gabs version` could not distinguish a binary that
// understands `profiles` from one that silently ignores them — the two behave
// very differently on the same config file, and only the commit hash told them
// apart.
func TestVersionIsNotAPreProfileRelease(t *testing.T) {
	if strings.HasPrefix(Version, "1.0.") || Version == "1.0" {
		t.Fatalf("Version = %q: a profile-aware build must not claim a pre-profile 1.0.x version", Version)
	}
}
