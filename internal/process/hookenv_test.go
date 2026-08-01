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

// A config unset of a managed name (SteamAppId here) is re-added by the
// managed layer; exporting it as absent too would be contradictory
// metadata, so final absence is computed after the managed layer.
func TestAbsentEnvNamesFilteredAgainstManagedLayer(t *testing.T) {
	c := NewController().(*Controller)
	err := c.Configure(LaunchSpec{
		GameId:         "g",
		Mode:           "SteamManaged",
		PathOrId:       "12345",
		Env:            map[string]string{},
		AbsentEnvNames: []string{"SteamAppId", "TRULY_ABSENT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var absent, steam string
	for _, kv := range c.FinalEnvironment() {
		if strings.HasPrefix(kv, "GABS_ABSENT_ENV=") {
			absent = strings.TrimPrefix(kv, "GABS_ABSENT_ENV=")
		}
		if strings.HasPrefix(kv, "SteamAppId=") {
			steam = kv
		}
	}
	if steam != "SteamAppId=12345" {
		t.Fatalf("managed SteamAppId must be present, got %q", steam)
	}
	if absent != "TRULY_ABSENT" {
		t.Fatalf("GABS_ABSENT_ENV must exclude managed re-adds, got %q", absent)
	}
}
