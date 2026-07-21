package process

import (
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/launch"
)

// Windows environments are case-insensitive: unsetEnv must remove every
// case variant and hook env must replace an inherited variant, not shadow
// it (design/01 via the resolver's folded-key semantics).
func TestHookEnvironmentCaseFoldingWindows(t *testing.T) {
	prev := hookEnvCaseInsensitive
	hookEnvCaseInsensitive = true
	t.Cleanup(func() { hookEnvCaseInsensitive = prev })

	t.Setenv("MiXeD_Case_Var", "inherited")
	t.Setenv("ReplaceMe", "old")

	h := &launch.ResolvedHook{
		UnsetEnv: []string{"mixed_case_var"},
		Env:      map[string]string{"REPLACEME": "new"},
	}
	env := hookEnvironment(h, "g", "")

	joined := "\n" + strings.Join(env, "\n") + "\n"
	if strings.Contains(joined, "MiXeD_Case_Var=") {
		t.Fatalf("unsetEnv must remove case variants:\n%s", joined)
	}
	if strings.Contains(joined, "ReplaceMe=old") {
		t.Fatalf("hook env must replace the inherited case variant:\n%s", joined)
	}
	if !strings.Contains(joined, "REPLACEME=new") {
		t.Fatalf("hook env value missing:\n%s", joined)
	}

	// exact-case platforms keep distinct spellings distinct
	hookEnvCaseInsensitive = false
	env = hookEnvironment(h, "g", "")
	joined = "\n" + strings.Join(env, "\n") + "\n"
	if !strings.Contains(joined, "MiXeD_Case_Var=inherited") {
		t.Fatalf("exact-case platforms must not fold:\n%s", joined)
	}
}
