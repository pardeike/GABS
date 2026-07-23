package process

import "testing"

func TestClassifyStaticCodes(t *testing.T) {
	cases := map[string]string{
		"unknown_argument":           CauseCall,
		"game_not_found":             CauseCall,
		"ambiguous_game_reference":   CauseCall,
		"profiles_not_configured":    CauseCall,
		"profile_not_found":          CauseCall,
		"launch_input_not_declared":  CauseCall,
		"launch_input_invalid":       CauseCall,
		"timeout_out_of_range":       CauseCall,
		"config_invalid":             CauseConfig,
		"launch_mode_incompatible":   CauseConfig,
		"spec_too_large":             CauseConfig,
		"already_running":            CauseState,
		"blocked_unknown_state":      CauseState,
		"external_instance_detected": CauseState,
		"operation_in_progress":      CauseState,
		"termination_unverified":     CauseState,
		"spawn_failed":               CauseEnvironment,
		"endpoint_unavailable":       CauseEnvironment,
		"exited_during_start":        CauseGame,
		"stale_bridge_credential":    CauseEnvironment,
	}
	for code, want := range cases {
		got := Classify(code, ClassifyContext{})
		if got.Class != want {
			t.Errorf("%s: class %q, want %q", code, got.Class, want)
		}
	}
}

func TestClassifyProofAdjusted(t *testing.T) {
	// launch_spec_unresolvable and unobserved are the two proof-adjusted
	// codes (design/05 bad-case map): never-proven → config, proven →
	// environment.
	for _, code := range []string{"launch_spec_unresolvable", "unobserved"} {
		never := Classify(code, ClassifyContext{Proven: false})
		if never.Class != CauseConfig {
			t.Errorf("%s on a never-proven context must be config, got %q", code, never.Class)
		}
		proven := Classify(code, ClassifyContext{Proven: true})
		if proven.Class != CauseEnvironment {
			t.Errorf("%s on a proven context must be environment, got %q", code, proven.Class)
		}
	}
}

func TestClassifyUnprovenInputAdjustsConfidenceNotClass(t *testing.T) {
	// A crash (game) with a first-seen input combination on a proven-bare
	// context keeps its outcome-implied class and only adds a secondary
	// note — never becomes config (design/08).
	res := Classify("exited_during_start", ClassifyContext{
		Proven:                true,
		InputCombinationFresh: true,
		SuppliedInputs:        []string{"scenario"},
	})
	if res.Class != CauseGame {
		t.Fatalf("an unproven input combination must not change the class: %q", res.Class)
	}
	if res.SecondaryNote == "" {
		t.Fatal("the candidate-input note must be attached as secondary evidence")
	}
}

func TestClassifyStopSideCodes(t *testing.T) {
	if Classify("stop_unsupported", ClassifyContext{}).Class != CauseConfig {
		t.Error("stop_unsupported is a configuration gap (no stop mechanism)")
	}
	if Classify("kill_unsupported", ClassifyContext{}).Class != CauseConfig {
		t.Error("kill_unsupported is a configuration gap")
	}
	if Classify("action_failed", ClassifyContext{}).Class != CauseEnvironment {
		t.Error("a failed stop/kill action is host/process state")
	}
	if Classify("action_timed_out", ClassifyContext{}).Class != CauseEnvironment {
		t.Error("a timed-out action is host/process state")
	}
}

func TestClassifyTrackRecordLine(t *testing.T) {
	// The split counters sharpen the message: workload proven, bridge never
	// connected points game-side.
	line := TrackRecordLine(&HistoryEntry{WorkloadStarts: 14, BridgeConnects: 0})
	if line == "" {
		t.Fatal("a proven context must render a track-record line")
	}
	if !containsAll(line, "14") {
		t.Fatalf("the line must report the counts: %q", line)
	}
	// A never-proven context still renders an explicit line — the absence
	// of proof is itself the evidence (design/08).
	np := TrackRecordLine(&HistoryEntry{ConsecutiveFailures: 1})
	if np == "" || !containsAll(np, "no successful starts") {
		t.Fatalf("never-proven must render an explicit line: %q", np)
	}
}

func TestClassifyActionSucceededRunning(t *testing.T) {
	if Classify("action_succeeded_running", ClassifyContext{}).Class != CauseState {
		t.Error("action_succeeded_running is a state situation the caller resolves")
	}
}

func TestClassifyExitedIsAlwaysGame(t *testing.T) {
	// exited_during_start is ALWAYS game — the evidence-based default (design/05
	// F6 adjudication): GABS observes only the first process it created and
	// cannot distinguish a game crash from a wrapper/container exit, so no
	// evidence available at classification time flips it to environment.
	if Classify("exited_during_start", ClassifyContext{}).Class != CauseGame {
		t.Error("a bare crash on start is game class")
	}
	if Classify("exited_during_start", ClassifyContext{Proven: true}).Class != CauseGame {
		t.Error("a proven context that exits is still game class — the exit is the workload's")
	}
	if Classify("exited_during_start", ClassifyContext{Proven: true, InputCombinationFresh: true, SuppliedInputs: []string{"scenario"}}).Class != CauseGame {
		t.Error("an unproven input combination adjusts only the secondary note, never the class")
	}
}

// TestClassifyExhaustiveOverStableCodes asserts every stable MCP result code
// (design/10-mcp-surface.md's exhaustive list) maps to exactly one class —
// failure codes to a cause class, success/pending codes to none (round 12 F3;
// design/30:294). It fails if a handler ever needs a code outside this list or
// the classifier drifts.
func TestClassifyExhaustiveOverStableCodes(t *testing.T) {
	want := map[string]string{
		// call
		"unknown_argument": CauseCall, "game_not_found": CauseCall,
		"ambiguous_game_reference": CauseCall, "timeout_out_of_range": CauseCall,
		"profiles_not_configured": CauseCall, "profile_not_found": CauseCall,
		"launch_input_not_declared": CauseCall, "launch_input_invalid": CauseCall,
		// config
		"config_invalid": CauseConfig, "launch_mode_incompatible": CauseConfig,
		"spec_too_large": CauseConfig, "kill_unsupported": CauseConfig,
		"stop_unsupported": CauseConfig,
		// state
		"already_running": CauseState, "blocked_unknown_state": CauseState,
		"external_instance_detected": CauseState, "operation_in_progress": CauseState,
		"termination_unverified": CauseState, "action_succeeded_running": CauseState,
		// environment
		"spawn_failed": CauseEnvironment, "stale_bridge_credential": CauseEnvironment,
		"endpoint_unavailable": CauseEnvironment, "action_failed": CauseEnvironment,
		"action_timed_out": CauseEnvironment,
		// evidence/proof-adjusted (never-proven defaults asserted here)
		"exited_during_start": CauseGame, "launch_spec_unresolvable": CauseConfig,
		"unobserved": CauseConfig,
		// success / pending — no failure cause
		"started_connected": "", "started_bridge_pending": "", "terminated": "",
		"started_attachment_deferred": "",
	}
	for code, class := range want {
		if got := Classify(code, ClassifyContext{}).Class; got != class {
			t.Errorf("Classify(%q) = %q, want %q", code, got, class)
		}
	}
	// An UNMAPPED code must return NO class (round 13 F2) — the classifier no
	// longer silently defaults an unknown code to environment, which masked
	// codes it was never taught. A code reaching the default fails visibly:
	// here, and (since the completion step attributes only a non-empty class)
	// as a missing causeClass in the handler battery.
	if got := Classify("some_unmapped_code_xyz", ClassifyContext{}).Class; got != "" {
		t.Errorf("an unmapped code must return no class (fail visibly), got %q", got)
	}
}

func TestClassifyTerminatedHasNoFailureCause(t *testing.T) {
	// A verified clean stop must carry NO cause class (round 11 P2-6) — it is
	// not a failure, and it must not fall through to the environment default.
	if c := Classify("terminated", ClassifyContext{}).Class; c != "" {
		t.Errorf("a verified stop has no failure cause, got %q", c)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
