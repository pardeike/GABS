package launch

import (
	"fmt"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Platform spec-size hard limits. Exceeding these is a structured pre-spawn
// error at Stage 2 naming the oversized part — never allowed through to an
// opaque CreateProcess/E2BIG failure. The check consumes the FULLY
// MATERIALIZED process spec (argv including the executable, environment
// including the managed layer), after endpoint allocation — a partial spec
// near a limit could otherwise pass and then exceed it once GABP variables,
// forward/absence lists, and platform variables are added.
const (
	// Windows: the environment block is limited to 32767 UTF-16 code units
	// (including terminators); the command line to 32766 characters.
	windowsEnvBlockLimit    = 32760
	windowsCommandLineLimit = 32760
	// Unix: combined argv+env floor (macOS ARG_MAX is 1 MiB; Linux is
	// typically larger). A spec near this size is pathological.
	unixCombinedLimit = 1 << 20
	// Linux additionally limits each individual argv/env string
	// (MAX_ARG_STRLEN, normally 32 pages = 128 KiB) independently of the
	// combined total.
	unixPerStringLimit = 128 * 1024
)

// SpecSizeIssue reports a materialized spec exceeding a platform hard limit.
type SpecSizeIssue struct {
	Part    string // "env" or "args"
	Message string
}

func (i *SpecSizeIssue) Error() string { return i.Message }

// CheckProcessSize validates the fully materialized process spec — the
// complete argv (executable first) and the complete environment (managed
// layer included) — against the current platform's hard limits. Returns nil
// when the spec fits.
func CheckProcessSize(argv []string, env map[string]string) *SpecSizeIssue {
	return checkProcessSizeFor(argv, env, runtime.GOOS)
}

func checkProcessSizeFor(argv []string, env map[string]string, goos string) *SpecSizeIssue {
	if goos == "windows" {
		envUnits := 0
		for k, v := range env {
			envUnits += utf16Len(k) + utf16Len(v) + 2 // '=' and NUL terminator
		}
		if envUnits > windowsEnvBlockLimit {
			return &SpecSizeIssue{Part: "env", Message: fmt.Sprintf(
				"materialized environment block is %d UTF-16 units, exceeding the Windows limit of %d", envUnits, windowsEnvBlockLimit)}
		}
		// Count the actual quoted command line the process launcher builds
		// (CreateProcess quoting doubles backslashes before quotes and
		// escapes quotes) — a length estimate undercounts pathological args.
		line := 0
		for i, a := range argv {
			if i > 0 {
				line++ // separating space
			}
			line += utf16Len(escapeWindowsArg(a))
		}
		if line > windowsCommandLineLimit {
			return &SpecSizeIssue{Part: "args", Message: fmt.Sprintf(
				"materialized command line is %d UTF-16 units after quoting, exceeding the Windows limit of %d", line, windowsCommandLineLimit)}
		}
		return nil
	}

	total := 0
	for k, v := range env {
		kv := len(k) + len(v) + 1
		if kv+1 > unixPerStringLimit {
			return &SpecSizeIssue{Part: "env", Message: fmt.Sprintf(
				"environment entry %s is %d bytes, exceeding the per-string exec limit of %d", k, kv, unixPerStringLimit)}
		}
		total += kv + 1
	}
	envTotal := total
	for i, a := range argv {
		if len(a)+1 > unixPerStringLimit {
			return &SpecSizeIssue{Part: "args", Message: fmt.Sprintf(
				"argument %d is %d bytes, exceeding the per-string exec limit of %d", i, len(a), unixPerStringLimit)}
		}
		total += len(a) + 1
	}
	if total > unixCombinedLimit {
		part := "args"
		if envTotal > total/2 {
			part = "env"
		}
		return &SpecSizeIssue{Part: part, Message: fmt.Sprintf(
			"materialized argv+environment is %d bytes, exceeding the platform limit of %d", total, unixCombinedLimit)}
	}
	return nil
}

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// escapeWindowsArg mirrors the CreateProcess quoting the Go runtime applies
// when building the command line: args containing spaces, tabs, or quotes
// are quoted; backslashes immediately before a quote (or the closing quote)
// are doubled; quotes are escaped.
func escapeWindowsArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			backslashes++
			b.WriteByte(c)
		case '"':
			// Double the run of preceding backslashes, then escape the quote.
			for j := 0; j < backslashes+1; j++ {
				b.WriteByte('\\')
			}
			b.WriteByte('"')
			backslashes = 0
		default:
			backslashes = 0
			b.WriteByte(c)
		}
	}
	// Double a trailing backslash run so it cannot escape the closing quote.
	for j := 0; j < backslashes; j++ {
		b.WriteByte('\\')
	}
	b.WriteByte('"')
	return b.String()
}
