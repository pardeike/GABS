package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// missingTargetServer configures a DirectPath game whose target exists now
// (so proof can be established) and returns the input-free context hash the
// default-profile start will use. Deleting the returned target path makes the
// next start fail launch_spec_unresolvable while the context hash is unchanged
// (the hash is over the target PATH, not its existence) — so a seeded proof
// still applies.
func missingTargetServer(t *testing.T) (*Server, config.GameConfig, string, string) {
	t.Helper()
	cfgDir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "game.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	game := config.GameConfig{ID: "vanish", Name: "Vanish", LaunchMode: "DirectPath", Target: target}
	s := NewServerForTesting(util.NewLogger("error"))
	s.SetConfigDir(cfgDir)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0", Games: map[string]config.GameConfig{game.ID: game},
	}, 0, 0)

	snap, _ := s.currentSnapshot()
	base, berr := launch.ResolveBaseContext(snap, game.ID, "", launch.Options{
		InheritedEnv: os.Environ(), CaseInsensitiveEnv: runtime.GOOS == "windows",
	})
	if berr != nil {
		t.Fatalf("resolve base: %v", berr)
	}
	return s, game, target, process.ContextHash(base)
}

// TestProvenMissingTargetIsEnvironment is the P1-1 linchpin: the proof-aware
// rule for launch_spec_unresolvable was previously unreachable (classified
// before the history context existed). A target that vanished after proven
// starts must render causeClass:environment ("it existed before — moved or
// uninstalled?"), never unclassified.
func TestProvenMissingTargetIsEnvironment(t *testing.T) {
	s, game, target, hash := missingTargetServer(t)

	// Establish proof for this exact context, then remove the claim.
	lid := seedTrackClaim(t, game.ID, s.configDir)
	if err := process.RecordWorkloadStart(game.ID, s.configDir, lid, "", hash,
		process.ContextSnapshot{}, process.SuccessBucket{}, timeNow()); err != nil {
		t.Fatal(err)
	}
	_ = process.RemoveRuntimeState(game.ID, s.configDir)

	// The target disappears.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1})
	if structured["code"] != "launch_spec_unresolvable" {
		t.Fatalf("expected launch_spec_unresolvable, got %v", structured["code"])
	}
	if structured["causeClass"] != process.CauseEnvironment {
		t.Fatalf("a proven target now missing is environment, got %#v", structured["causeClass"])
	}
	if _, ok := structured["trackRecord"].(string); !ok {
		t.Fatalf("a resolved context must render a track-record line: %#v", structured)
	}
	// A read-only failure must not mutate history.
	h, _ := process.LoadHistory(game.ID, s.configDir)
	if e := h.Profiles[""]; e == nil || e.WorkloadStarts != 1 || e.ConsecutiveFailures != 0 {
		t.Fatalf("launch_spec_unresolvable must not mutate history: %+v", e)
	}
}

// TestNeverProvenMissingTargetIsConfig is the other half: the same error on a
// never-proven context reads config ("probably a typo").
func TestNeverProvenMissingTargetIsConfig(t *testing.T) {
	s, game, target, _ := missingTargetServer(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1})
	if structured["code"] != "launch_spec_unresolvable" {
		t.Fatalf("expected launch_spec_unresolvable, got %v", structured["code"])
	}
	if structured["causeClass"] != process.CauseConfig {
		t.Fatalf("a never-proven unresolvable target is config, got %#v", structured["causeClass"])
	}
	// No proof existed and none is written.
	if h, _ := process.LoadHistory(game.ID, s.configDir); len(h.Profiles) != 0 {
		t.Fatalf("a never-proven failure must mutate no history: %+v", h.Profiles)
	}
}

// TestCallClassFailuresCarryCallClassAndMutateNoHistory covers the pre-
// resolution call-class errors: each must carry causeClass:call, no track
// line, and mutate no history.
func TestCallClassFailuresCarryCallClassAndMutateNoHistory(t *testing.T) {
	cases := []struct {
		name string
		args map[string]interface{}
		code string
	}{
		{"unknown profile", map[string]interface{}{"gameId": "adventure", "profile": "nope"}, "profile_not_found"},
		{"bad input", map[string]interface{}{"gameId": "adventure", "launchInputs": map[string]interface{}{"scenario": "not-in-enum"}}, "launch_input_invalid"},
		{"timeout out of range", map[string]interface{}{"gameId": "adventure", "timeout": 99999}, "timeout_out_of_range"},
		{"unknown argument", map[string]interface{}{"gameId": "adventure", "bogusArg": true}, "unknown_argument"},
		{"malformed profile", map[string]interface{}{"gameId": "adventure", "profile": 42}, "invalid_argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newProfiledServer(t)
			raw, structured := callTool(t, s, "games.start", tc.args)
			if structured["code"] != tc.code {
				t.Fatalf("expected %s, got %v (%s)", tc.code, structured["code"], raw)
			}
			if structured["causeClass"] != process.CauseCall {
				t.Fatalf("%s must be call-class, got %#v", tc.code, structured["causeClass"])
			}
			if _, ok := structured["trackRecord"]; ok {
				t.Fatalf("a pre-resolution error has no context and must render no track line: %s", raw)
			}
			if h, _ := process.LoadHistory("adventure", s.configDir); len(h.Profiles) != 0 {
				t.Fatalf("a call-class error must mutate no history: %+v", h.Profiles)
			}
		})
	}
}
