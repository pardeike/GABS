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
