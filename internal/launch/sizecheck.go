package launch

import (
	"fmt"
	"runtime"
	"unicode/utf16"
)

// Platform spec-size hard limits. Exceeding these is a structured pre-spawn
// error at Stage 2 naming the oversized part — never allowed through to an
// opaque CreateProcess/E2BIG failure. Values are deliberately conservative
// hard floors of the real platform limits.
const (
	// Windows: the environment block is limited to 32767 UTF-16 code units
	// (including terminators); the command line to 32766 characters.
	windowsEnvBlockLimit    = 32760
	windowsCommandLineLimit = 32760
	// Unix: combined argv+env floor (macOS ARG_MAX is 1 MiB; Linux is
	// typically larger). A spec near this size is pathological.
	unixCombinedLimit = 1 << 20
)

// SpecSizeIssue reports a resolved spec exceeding a platform hard limit.
type SpecSizeIssue struct {
	Part    string // "env" or "args"
	Message string
}

func (i *SpecSizeIssue) Error() string { return i.Message }

// CheckSpecSize validates the resolved spec against the current platform's
// hard limits. Returns nil when the spec fits.
func CheckSpecSize(r *Resolved) *SpecSizeIssue {
	return checkSpecSizeFor(r, runtime.GOOS)
}

func checkSpecSizeFor(r *Resolved, goos string) *SpecSizeIssue {
	if goos == "windows" {
		envUnits := 0
		for k, v := range r.Env {
			envUnits += len(utf16.Encode([]rune(k))) + len(utf16.Encode([]rune(v))) + 2 // '=' and NUL
		}
		if envUnits > windowsEnvBlockLimit {
			return &SpecSizeIssue{Part: "env", Message: fmt.Sprintf(
				"resolved environment block is %d UTF-16 units, exceeding the Windows limit of %d", envUnits, windowsEnvBlockLimit)}
		}
		argUnits := 0
		for _, a := range r.Args {
			argUnits += len(utf16.Encode([]rune(a))) + 3 // worst-case quoting + separator
		}
		if argUnits > windowsCommandLineLimit {
			return &SpecSizeIssue{Part: "args", Message: fmt.Sprintf(
				"resolved command line is ~%d UTF-16 units, exceeding the Windows limit of %d", argUnits, windowsCommandLineLimit)}
		}
		return nil
	}
	total := 0
	for k, v := range r.Env {
		total += len(k) + len(v) + 2
	}
	envTotal := total
	for _, a := range r.Args {
		total += len(a) + 1
	}
	if total > unixCombinedLimit {
		part := "args"
		if envTotal > total/2 {
			part = "env"
		}
		return &SpecSizeIssue{Part: part, Message: fmt.Sprintf(
			"resolved argv+environment is %d bytes, exceeding the platform limit of %d", total, unixCombinedLimit)}
	}
	return nil
}
