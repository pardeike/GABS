package launch

import (
	"strings"
	"testing"
)

func TestProcessSizeWithinLimits(t *testing.T) {
	argv := []string{"/opt/game", "--x"}
	env := map[string]string{"A": "b"}
	if iss := checkUnixProcessSize(argv, env, unixExecLimits{
		combinedBytes:         2 << 20,
		perStringBytes:        32 * 4096,
		pointerBytes:          8,
		includeExecutablePath: true,
	}); iss != nil {
		t.Fatalf("small spec must pass: %v", iss)
	}
	if iss := checkWindowsProcessSize(argv, env); iss != nil {
		t.Fatalf("small spec must pass on windows: %v", iss)
	}
}

func TestProcessSizeOversizedEnvUnix(t *testing.T) {
	// Combined Linux limit via many medium entries (each under the actual
	// 32-page per-string cap for this simulated 4 KiB-page host).
	env := map[string]string{}
	for i := 0; i < 21; i++ {
		env[strings.Repeat("K", 10)+string(rune('A'+i))] = strings.Repeat("x", 100*1024)
	}
	iss := checkUnixProcessSize([]string{"/opt/game"}, env, unixExecLimits{
		combinedBytes:         2 << 20,
		perStringBytes:        32 * 4096,
		pointerBytes:          8,
		includeExecutablePath: true,
	})
	if iss == nil || iss.Part != "env" {
		t.Fatalf("expected env-part combined size error, got %v", iss)
	}
}

func TestProcessSizeUsesPlatformSpecificUnixLimits(t *testing.T) {
	argv200KiB := []string{"/opt/game", strings.Repeat("y", 200*1024)}

	// Darwin has no Linux MAX_ARG_STRLEN analogue. A 200 KiB argument is
	// valid while its complete argv+env+pointer charge remains below NCARGS.
	darwin := unixExecLimits{
		combinedBytes:      1 << 20,
		pointerBytes:       8,
		pointerTerminators: 2,
		alignStringBytes:   true,
	}
	if iss := checkUnixProcessSize(argv200KiB, map[string]string{}, darwin); iss != nil {
		t.Fatalf("Darwin must accept a 200 KiB argument below its combined hard limit: %v", iss)
	}

	// Linux's individual-string cap is 32 *the host's page size*, not an
	// invariant 128 KiB. Simulate a 16 KiB-page kernel: 200 KiB is valid,
	// while a string beyond 512 KiB is not.
	linux16K := unixExecLimits{
		combinedBytes:         2 << 20,
		perStringBytes:        32 * 16 * 1024,
		pointerBytes:          8,
		includeExecutablePath: true,
	}
	if iss := checkUnixProcessSize(argv200KiB, map[string]string{}, linux16K); iss != nil {
		t.Fatalf("Linux on 16 KiB pages must accept a 200 KiB argument: %v", iss)
	}
	argvTooLong := []string{"/opt/game", strings.Repeat("y", 32*16*1024)}
	iss := checkUnixProcessSize(argvTooLong, map[string]string{}, linux16K)
	if iss == nil || iss.Part != "args" || !strings.Contains(iss.Message, "per-string") {
		t.Fatalf("expected per-string args error, got %v", iss)
	}
	env := map[string]string{"BIG": strings.Repeat("z", 32*16*1024)}
	iss = checkUnixProcessSize([]string{"/opt/game"}, env, linux16K)
	if iss == nil || iss.Part != "env" || !strings.Contains(iss.Message, "per-string") {
		t.Fatalf("expected per-string env error, got %v", iss)
	}
}

func TestProcessSizeLinuxUsesActualCombinedLimit(t *testing.T) {
	limits := unixExecLimits{
		combinedBytes:         2 << 20,
		perStringBytes:        32 * 4096,
		pointerBytes:          8,
		includeExecutablePath: true,
	}
	// About 1.6 MiB: valid on a typical 8 MiB-stack Linux process, and
	// incorrectly rejected by the old macOS-derived 1 MiB check.
	argv := []string{"/opt/game"}
	for i := 0; i < 16; i++ {
		argv = append(argv, strings.Repeat("x", 100*1024))
	}
	if iss := checkUnixProcessSize(argv, map[string]string{}, limits); iss != nil {
		t.Fatalf("multi-argument Linux payload below its 2 MiB limit must pass: %v", iss)
	}

	// Cross the same host's real combined limit without crossing its
	// individual-string limit.
	for i := 0; i < 6; i++ {
		argv = append(argv, strings.Repeat("x", 100*1024))
	}
	if iss := checkUnixProcessSize(argv, map[string]string{}, limits); iss == nil || iss.Part != "args" {
		t.Fatalf("payload over the Linux combined limit must fail as args, got %v", iss)
	}
}

func TestProcessSizeDarwinCountsPointerTablesAndFixedOverhead(t *testing.T) {
	limits := unixExecLimits{
		combinedBytes:      1 << 20,
		pointerBytes:       8,
		pointerTerminators: 2,
		alignStringBytes:   true,
	}

	// Strings alone consume only ~120 KiB; the argv pointer table adds
	// ~960 KiB, so Darwin's kernel rejects this with E2BIG.
	argv := make([]string, 120_000)
	if iss := checkUnixProcessSize(argv, map[string]string{}, limits); iss == nil || iss.Part != "args" {
		t.Fatalf("Darwin pointer-table overflow must be detected before spawn, got %v", iss)
	}

	// XNU charges both NULL vector terminators and tail alignment. One
	// two-byte argument plus its pointer, the two terminators, and six bytes
	// of alignment exactly consume this synthetic 32-byte limit.
	exact := limits
	exact.combinedBytes = 32
	if iss := checkUnixProcessSize([]string{"x"}, map[string]string{}, exact); iss != nil {
		t.Fatalf("exact Darwin hard-limit charge must pass: %v", iss)
	}
	exact.combinedBytes = 31
	if iss := checkUnixProcessSize([]string{"x"}, map[string]string{}, exact); iss == nil {
		t.Fatal("Darwin fixed pointer/alignment overhead must be included")
	}
}

func TestProcessSizeWindowsQuotedCommandLine(t *testing.T) {
	// 20,000 quote characters encode to >40,000 UTF-16 units after
	// CreateProcess quoting; a plain length estimate would pass this.
	argv := []string{"C:\\game.exe", strings.Repeat(`"`, 20000)}
	iss := checkWindowsProcessSize(argv, map[string]string{})
	if iss == nil || iss.Part != "args" {
		t.Fatalf("expected quoted-command-line size error, got %v", iss)
	}
	if !strings.Contains(iss.Message, "after quoting") {
		t.Fatalf("message must reflect quoting-aware count: %v", iss)
	}
}

func TestEscapeWindowsArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `""`},
		{"plain", "plain"},
		{"has space", `"has space"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash "q`, `"back\slash \"q"`}, // backslash not before a quote stays single
		{`a\"b c`, `"a\\\"b c"`},              // backslash before a quote doubles, quote escapes
		{`trail\`, `trail\`},                  // no quoting needed, unchanged
		{`trail me\`, `"trail me\\"`},         // quoted: trailing backslash doubled
	}
	for _, c := range cases {
		if got := escapeWindowsArg(c.in); got != c.want {
			t.Fatalf("escapeWindowsArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
