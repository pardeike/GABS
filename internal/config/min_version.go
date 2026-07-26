package config

import (
	"fmt"

	"github.com/pardeike/gabs/internal/version"
)

// MinGabsVersionForProfiles is the first GABS release that understands
// `profiles`, `launchInputs`, and `lifecycle`. Older binaries ignore those
// fields entirely — they do not warn, and there is no config construct that
// makes them fail, so a config author's only protection against an old binary
// already installed somewhere is to know about it.
const MinGabsVersionForProfiles = "1.1.0"

// checkMinGabsVersion enforces a config's declared minimum binary version.
//
// It returns an error issue when the running binary is demonstrably too old or
// the declaration is malformed, and a warning issue when the requirement could
// not be evaluated because the running binary carries no parseable version (a
// development build stamped "dev"/"unknown"). Refusing to load a config on a
// dev build would be worse than the skew this check exists to catch.
func checkMinGabsVersion(declared string) (warn, errIssue *ConfigIssue) {
	if declared == "" {
		return nil, nil
	}

	want, err := version.Parse(declared)
	if err != nil {
		return nil, &ConfigIssue{
			Path:    "minGabsVersion",
			Message: fmt.Sprintf("%q is not a MAJOR.MINOR.PATCH version: %v", declared, err),
		}
	}

	ok, comparable := version.AtLeast(want)
	if !comparable {
		return &ConfigIssue{
			Path: "minGabsVersion",
			Message: fmt.Sprintf(
				"config requires GABS >= %s but this build reports version %q, which cannot be compared; "+
					"the requirement was NOT enforced", declared, version.Version),
		}, nil
	}
	if !ok {
		return nil, &ConfigIssue{
			Path: "minGabsVersion",
			Message: fmt.Sprintf(
				"config requires GABS >= %s but this binary is %s; upgrade GABS, or remove minGabsVersion "+
					"if you accept that an older binary ignores config fields it does not understand",
				declared, version.Version),
		}
	}
	return nil, nil
}
