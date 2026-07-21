package launch

import (
	"strings"
	"testing"
)

func TestProcessSizeWithinLimits(t *testing.T) {
	argv := []string{"/opt/game", "--x"}
	env := map[string]string{"A": "b"}
	if iss := checkProcessSizeFor(argv, env, "linux"); iss != nil {
		t.Fatalf("small spec must pass: %v", iss)
	}
	if iss := checkProcessSizeFor(argv, env, "windows"); iss != nil {
		t.Fatalf("small spec must pass on windows: %v", iss)
	}
}

func TestProcessSizeOversizedEnvUnix(t *testing.T) {
	// combined limit via many medium entries (each under the per-string cap)
	env := map[string]string{}
	for i := 0; i < 20; i++ {
		env[strings.Repeat("K", 10)+string(rune('A'+i))] = strings.Repeat("x", 100*1024)
	}
	iss := checkProcessSizeFor([]string{"/opt/game"}, env, "linux")
	if iss == nil || iss.Part != "env" {
		t.Fatalf("expected env-part combined size error, got %v", iss)
	}
}

func TestProcessSizePerStringUnix(t *testing.T) {
	// a single 200 KiB argument is under 1 MiB combined but over
	// MAX_ARG_STRLEN — it must be rejected per-string
	argv := []string{"/opt/game", strings.Repeat("y", 200*1024)}
	iss := checkProcessSizeFor(argv, map[string]string{}, "linux")
	if iss == nil || iss.Part != "args" || !strings.Contains(iss.Message, "per-string") {
		t.Fatalf("expected per-string args error, got %v", iss)
	}
	env := map[string]string{"BIG": strings.Repeat("z", 200*1024)}
	iss = checkProcessSizeFor([]string{"/opt/game"}, env, "linux")
	if iss == nil || iss.Part != "env" || !strings.Contains(iss.Message, "per-string") {
		t.Fatalf("expected per-string env error, got %v", iss)
	}
}

func TestProcessSizeWindowsQuotedCommandLine(t *testing.T) {
	// 20,000 quote characters encode to >40,000 UTF-16 units after
	// CreateProcess quoting; a plain length estimate would pass this.
	argv := []string{"C:\\game.exe", strings.Repeat(`"`, 20000)}
	iss := checkProcessSizeFor(argv, map[string]string{}, "windows")
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
		{`trail\`, `trail\`},          // no quoting needed, unchanged
		{`trail me\`, `"trail me\\"`}, // quoted: trailing backslash doubled
	}
	for _, c := range cases {
		if got := escapeWindowsArg(c.in); got != c.want {
			t.Fatalf("escapeWindowsArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
