package launch

import (
	"strings"
	"testing"
)

func TestSpecSizeWithinLimits(t *testing.T) {
	r := &Resolved{Args: []string{"--x"}, Env: map[string]string{"A": "b"}}
	if iss := checkSpecSizeFor(r, "linux"); iss != nil {
		t.Fatalf("small spec must pass: %v", iss)
	}
	if iss := checkSpecSizeFor(r, "windows"); iss != nil {
		t.Fatalf("small spec must pass on windows: %v", iss)
	}
}

func TestSpecSizeOversizedEnvUnix(t *testing.T) {
	r := &Resolved{Env: map[string]string{"BIG": strings.Repeat("x", unixCombinedLimit)}}
	iss := checkSpecSizeFor(r, "linux")
	if iss == nil || iss.Part != "env" {
		t.Fatalf("expected env-part size error, got %v", iss)
	}
	if !strings.Contains(iss.Message, "exceeding") {
		t.Fatalf("message must name the limit: %v", iss)
	}
}

func TestSpecSizeOversizedArgsWindows(t *testing.T) {
	r := &Resolved{Args: []string{strings.Repeat("y", windowsCommandLineLimit)}}
	iss := checkSpecSizeFor(r, "windows")
	if iss == nil || iss.Part != "args" {
		t.Fatalf("expected args-part size error on windows, got %v", iss)
	}
}
