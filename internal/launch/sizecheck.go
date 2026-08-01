package launch

import (
	"fmt"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Windows process-creation hard limits. Unix limits are discovered from the
// running kernel in platform files: Darwin exposes kern.argmax; Linux derives
// the current ceiling from RLIMIT_STACK and the kernel's page size.
const (
	// The environment block is limited to 32767 UTF-16 code units (including
	// terminators); the command line to 32766 characters. Keep the established
	// small safety margin for the Windows launcher representation.
	windowsEnvBlockLimit    = 32760
	windowsCommandLineLimit = 32760
)

// unixExecLimits describes what the current kernel charges against its exec
// argument-space hard limit. The accounting differs materially by OS:
//
//   - Linux charges argv/env strings, one pointer per string, and an internal
//     copy of the executable path. It independently caps each string at 32
//     pages.
//   - Darwin charges argv/env strings, one pointer per string, both vector NULL
//     terminators, and tail alignment. It has no Linux-style per-string cap.
//
// Zero combinedBytes means the platform has no exact pre-spawn check available;
// in that case GABS lets exec report the platform error rather than inventing a
// smaller limit and rejecting a valid launch.
type unixExecLimits struct {
	combinedBytes         uint64
	perStringBytes        uint64
	pointerBytes          uint64
	pointerTerminators    uint64
	alignStringBytes      bool
	includeExecutablePath bool
}

// SpecSizeIssue reports a materialized spec exceeding a platform hard limit.
type SpecSizeIssue struct {
	Part    string // "env" or "args"
	Message string
}

func (i *SpecSizeIssue) Error() string { return i.Message }

// CheckProcessSize validates the fully materialized process spec — the
// complete argv (executable first) and complete environment (managed layer
// included) — against the current platform's actual hard limits. The check is
// intentionally performed only where the limit and kernel accounting are
// known exactly; a conservative heuristic is not a valid pre-spawn rejection.
func CheckProcessSize(argv []string, env map[string]string) *SpecSizeIssue {
	if runtime.GOOS == "windows" {
		return checkWindowsProcessSize(argv, env)
	}
	limits, ok := currentUnixExecLimits()
	if !ok {
		return nil
	}
	return checkUnixProcessSize(argv, env, limits)
}

// checkWindowsProcessSize is separate from Unix accounting so tests cannot
// accidentally apply the current host's limits to a different target OS.
func checkWindowsProcessSize(argv []string, env map[string]string) *SpecSizeIssue {
	envUnits := 0
	for k, v := range env {
		envUnits += utf16Len(k) + utf16Len(v) + 2 // '=' and NUL terminator
	}
	if envUnits > windowsEnvBlockLimit {
		return &SpecSizeIssue{Part: "env", Message: fmt.Sprintf(
			"materialized environment block is %d UTF-16 units, exceeding the Windows limit of %d", envUnits, windowsEnvBlockLimit)}
	}
	// Count the actual quoted command line the process launcher builds
	// (CreateProcess quoting doubles backslashes before quotes and escapes
	// quotes) — a plain string-length estimate undercounts pathological args.
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

func checkUnixProcessSize(argv []string, env map[string]string, limits unixExecLimits) *SpecSizeIssue {
	if limits.combinedBytes == 0 {
		return nil
	}

	var envStrings, argStrings uint64
	for k, v := range env {
		// k=v plus the terminating NUL. Linux's MAX_ARG_STRLEN includes the
		// terminator; Darwin has no independent per-string limit.
		n := uint64(len(k) + len(v) + 2)
		if limits.perStringBytes > 0 && n > limits.perStringBytes {
			return &SpecSizeIssue{Part: "env", Message: fmt.Sprintf(
				"environment entry %s is %d bytes including its terminator, exceeding the per-string exec limit of %d", k, n, limits.perStringBytes)}
		}
		envStrings += n
	}
	for i, arg := range argv {
		n := uint64(len(arg) + 1)
		if limits.perStringBytes > 0 && n > limits.perStringBytes {
			return &SpecSizeIssue{Part: "args", Message: fmt.Sprintf(
				"argument %d is %d bytes including its terminator, exceeding the per-string exec limit of %d", i, n, limits.perStringBytes)}
		}
		argStrings += n
	}

	envCharge := envStrings + uint64(len(env))*limits.pointerBytes
	argCharge := argStrings + uint64(len(argv))*limits.pointerBytes
	if limits.pointerTerminators > 0 {
		// Darwin charges argv's and envp's NULL slots. Attribute one to each
		// part; the platform descriptor supplies two for the native ABI.
		argCharge += limits.pointerBytes
		envCharge += limits.pointerBytes
		if limits.pointerTerminators > 2 {
			argCharge += (limits.pointerTerminators - 2) * limits.pointerBytes
		}
	}
	if limits.includeExecutablePath && len(argv) > 0 {
		// Linux copies the exec pathname into the same bounded stack area in
		// addition to argv[0]. Materialization supplies that path as argv[0].
		argCharge += uint64(len(argv[0]) + 1)
	}
	if limits.alignStringBytes && limits.pointerBytes > 1 {
		stringsTotal := envStrings + argStrings
		padding := (limits.pointerBytes - stringsTotal%limits.pointerBytes) % limits.pointerBytes
		argCharge += padding
	}

	total := envCharge + argCharge
	if total > limits.combinedBytes {
		part := "args"
		if envCharge > argCharge {
			part = "env"
		}
		return &SpecSizeIssue{Part: part, Message: fmt.Sprintf(
			"materialized argv+environment requires %d bytes including pointer tables and platform overhead, exceeding the platform limit of %d", total, limits.combinedBytes)}
	}
	return nil
}

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// escapeWindowsArg mirrors the CreateProcess quoting the Go runtime applies
// when building the command line: args containing spaces, tabs, or quotes are
// quoted; backslashes immediately before a quote (or the closing quote) are
// doubled; quotes are escaped.
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
	for j := 0; j < backslashes; j++ {
		b.WriteByte('\\')
	}
	b.WriteByte('"')
	return b.String()
}
