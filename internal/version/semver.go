package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Semver is a parsed MAJOR.MINOR.PATCH triple. Pre-release and build metadata
// are accepted and ignored for ordering: GABS only needs "is this binary new
// enough", and no GABS release has ever ordered two builds by pre-release tag.
type Semver struct {
	Major, Minor, Patch int
}

// Parse reads a MAJOR.MINOR.PATCH version, tolerating a leading "v" and any
// -prerelease or +build suffix.
func Parse(s string) (Semver, error) {
	v := strings.TrimSpace(s)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return Semver{}, fmt.Errorf("empty version")
	}
	// Drop build metadata, then pre-release.
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}

	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return Semver{}, fmt.Errorf("version %q has too many components", s)
	}
	out := Semver{}
	targets := []*int{&out.Major, &out.Minor, &out.Patch}
	for i, p := range parts {
		if p == "" {
			return Semver{}, fmt.Errorf("version %q has an empty component", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return Semver{}, fmt.Errorf("version %q component %q is not a number", s, p)
		}
		if n < 0 {
			return Semver{}, fmt.Errorf("version %q component %q is negative", s, p)
		}
		*targets[i] = n
	}
	return out, nil
}

// Compare returns -1 if a < b, 0 if equal, +1 if a > b.
func Compare(a, b Semver) int {
	for _, pair := range [][2]int{
		{a.Major, b.Major},
		{a.Minor, b.Minor},
		{a.Patch, b.Patch},
	} {
		switch {
		case pair[0] < pair[1]:
			return -1
		case pair[0] > pair[1]:
			return 1
		}
	}
	return 0
}

// AtLeast reports whether the running binary's version is >= want. The second
// result is false when the running version is not parseable — a development
// build stamped "dev" or "unknown" — in which case the caller must not treat
// the requirement as violated: refusing to load a config on a dev build would
// be worse than the skew the check exists to catch.
func AtLeast(want Semver) (ok bool, comparable bool) {
	running, err := Parse(Version)
	if err != nil {
		return false, false
	}
	return Compare(running, want) >= 0, true
}
