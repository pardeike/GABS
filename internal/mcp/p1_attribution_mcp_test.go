package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestFinalizerAttributesEveryStableFailureCode drives EVERY stable failure
// code (design/10's list) through the dispatch finalizer and asserts each gets
// causeClass + the neutral track-record line, while success/pending codes get
// nothing (round 12 F1). This is the exhaustive handler-level net: any escaper
// (endpoint-in-use, stale-bridge-credential, the generic fallback) that reaches
// dispatch with one of these codes is covered.
func TestFinalizerAttributesEveryStableFailureCode(t *testing.T) {
	s := newProfiledServer(t)
	failureCodes := []string{
		"unknown_argument", "config_invalid", "game_not_found", "ambiguous_game_reference",
		"timeout_out_of_range", "launch_spec_unresolvable", "profiles_not_configured",
		"profile_not_found", "launch_input_not_declared", "launch_input_invalid",
		"launch_mode_incompatible", "already_running", "blocked_unknown_state",
		"external_instance_detected", "spawn_failed", "exited_during_start", "unobserved",
		"operation_in_progress", "kill_unsupported", "stop_unsupported",
		"termination_unverified", "stale_bridge_credential", "endpoint_unavailable",
		"spec_too_large", "action_failed", "action_timed_out", "action_succeeded_running",
	}
	for _, code := range failureCodes {
		result := &ToolResult{StructuredContent: map[string]interface{}{"code": code, "gameId": "g"}}
		s.ensureFailureAttribution("games.start", result)
		if _, ok := result.StructuredContent["causeClass"]; !ok {
			t.Errorf("%s: the finalizer must attach causeClass", code)
		}
		if line, ok := result.StructuredContent["trackRecord"].(string); !ok || !strings.Contains(line, "no successful starts") {
			t.Errorf("%s: the finalizer must attach the neutral track-record line", code)
		}
	}
	for _, code := range []string{"started_connected", "started_bridge_pending", "terminated", "started_attachment_deferred"} {
		result := &ToolResult{StructuredContent: map[string]interface{}{"code": code, "gameId": "g"}}
		s.ensureFailureAttribution("games.start", result)
		if _, ok := result.StructuredContent["causeClass"]; ok {
			t.Errorf("%s: a success/pending code must never acquire causeClass", code)
		}
	}
}

// TestEveryLifecycleFailureCarriesAttribution is the F1 handler-level net:
// failures that return directly (unknown/ambiguous game, and the generic
// paths) must still carry causeClass + a track-record line via the final
// safety-net finalizer at dispatch — none may escape (design/08:53).
func TestEveryLifecycleFailureCarriesAttribution(t *testing.T) {
	cases := []struct {
		tool  string
		code  string
		class string
	}{
		{"games.start", "game_not_found", process.CauseCall},
		{"games.connect", "game_not_found", process.CauseCall},
		{"games.stop", "game_not_found", process.CauseCall},
		{"games.kill", "game_not_found", process.CauseCall},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			s := newProfiledServer(t)
			raw, structured := callTool(t, s, tc.tool, map[string]interface{}{"gameId": "does-not-exist"})
			if structured["code"] != tc.code {
				t.Fatalf("%s: expected %s, got %v (%s)", tc.tool, tc.code, structured["code"], raw)
			}
			if structured["causeClass"] != tc.class {
				t.Fatalf("%s: a direct-return failure must still carry causeClass, got %#v", tc.tool, structured["causeClass"])
			}
			if line, ok := structured["trackRecord"].(string); !ok || !strings.Contains(line, "no successful starts") {
				t.Fatalf("%s: must carry the neutral track-record line, got %#v", tc.tool, structured["trackRecord"])
			}
		})
	}
}

// TestProvenInputComboOnMissingTargetIsNotFirstRun covers F8: the read-only
// failure path computes the value digest when the bucket key exists, so a
// launch_spec_unresolvable on a target that vanished — with a previously
// PROVEN exact input combination — is not mislabeled "first run with this
// input combination".
func TestProvenInputComboOnMissingTargetIsNotFirstRun(t *testing.T) {
	cfgDir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "game.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	game := config.GameConfig{
		ID: "vanish", Name: "Vanish", LaunchMode: "DirectPath", Target: target,
		LaunchInputs: map[string]config.LaunchInputConfig{
			"scenario": {Description: "pick", Type: "string", Enum: []string{"arena", "tutorial"}, Args: []string{"-scenario=${value}"}},
		},
	}
	s := NewServerForTesting(util.NewLogger("error"))
	s.SetConfigDir(cfgDir)
	t.Cleanup(s.Shutdown)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0", Games: map[string]config.GameConfig{game.ID: game},
	}, 0, 0)

	// Prove BOTH the context and the exact scenario=arena input combination.
	snap, _ := s.currentSnapshot()
	base, _ := launch.ResolveBaseContext(snap, game.ID, "", launch.Options{InheritedEnv: os.Environ(), CaseInsensitiveEnv: runtime.GOOS == "windows"})
	hash := process.ContextHash(base)
	key, _ := process.EnsureBucketKey(game.ID, s.configDir)
	bucket := process.SuccessBucket{
		InputNames:   []string{"scenario"},
		PerInputDecl: map[string]string{"scenario": process.InputDeclHash(game, []string{"scenario"})},
		DeclHash:     process.InputDeclHash(game, []string{"scenario"}),
		ValueDigest:  process.BucketValueDigest(key, map[string]string{"scenario": "arena"}),
	}
	lid := seedTrackClaim(t, game.ID, s.configDir)
	if err := process.RecordWorkloadStart(game.ID, s.configDir, lid, "", hash, process.ContextSnapshot{}, bucket, timeNow()); err != nil {
		t.Fatal(err)
	}
	_ = process.RemoveRuntimeState(game.ID, s.configDir)

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	_, structured := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": game.ID, "launchInputs": map[string]interface{}{"scenario": "arena"},
	})
	if structured["code"] != "launch_spec_unresolvable" {
		t.Fatalf("expected launch_spec_unresolvable, got %v", structured["code"])
	}
	if structured["causeClass"] != process.CauseEnvironment {
		t.Fatalf("a proven context now missing is environment, got %#v", structured["causeClass"])
	}
	if note, ok := structured["candidateInputNote"].(string); ok && strings.Contains(note, "first run") {
		t.Fatalf("a proven input combination must NOT read as first run: %q", note)
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
			// design/08 §call-class: the response still carries track-record
			// evidence — the neutral "no successful starts" line (round 12 F2).
			line, ok := structured["trackRecord"].(string)
			if !ok || !strings.Contains(line, "no successful starts") {
				t.Fatalf("a call-class error must carry the neutral track-record line: %#v", structured["trackRecord"])
			}
			if h, _ := process.LoadHistory("adventure", s.configDir); len(h.Profiles) != 0 {
				t.Fatalf("a call-class error must mutate no history: %+v", h.Profiles)
			}
		})
	}
}
