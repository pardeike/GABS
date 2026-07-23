package process

import (
	"errors"
	"testing"
	"time"
)

// TestAfterCommitRunsOnlyAfterSuccessfulSave covers round-13 F5: the history
// counters (moved to the afterCommit hook) must advance ONLY after the runtime
// save commits, so a save failure can never leave history ahead of the claim
// and a retry cannot double-count.
func TestAfterCommitRunsOnlyAfterSuccessfulSave(t *testing.T) {
	dir := t.TempDir()
	spec := LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := NewRuntimeState(spec, RuntimeStateStatusStarting)
	st.Phase = PhaseStarting
	if err := ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	// A save failure: mutate applies in memory, save fails, afterCommit must
	// NOT run.
	ran := false
	restore := SetSaveRuntimeStateFailHookForTesting(func() error { return errors.New("simulated disk failure") })
	_, err := TransitionRuntimeStateThen("g", dir, time.Second,
		func(s *RuntimeState) error { s.Phase = PhaseActive; return nil },
		func(s *RuntimeState) { ran = true })
	restore()
	if err == nil {
		t.Fatal("expected the injected save failure to surface")
	}
	if ran {
		t.Fatal("afterCommit must NOT run when the runtime save fails (F5): history would advance ahead of the claim")
	}
	// The runtime state is unchanged on disk (still starting) — the retry path.
	if cur, _ := LoadRuntimeState("g", dir); cur == nil || cur.Phase != PhaseStarting {
		t.Fatalf("a failed transition must not mutate the persisted claim: %+v", cur)
	}

	// The retry: save succeeds, afterCommit runs exactly once.
	ran = false
	_, err = TransitionRuntimeStateThen("g", dir, time.Second,
		func(s *RuntimeState) error { s.Phase = PhaseActive; return nil },
		func(s *RuntimeState) { ran = true })
	if err != nil {
		t.Fatalf("retry must succeed: %v", err)
	}
	if !ran {
		t.Fatal("afterCommit must run after a successful save")
	}
}

// TestWorkloadStartNotDoubleCountedAcrossSaveFailure proves the end-to-end
// consequence for a real counter: a Stage-4 credit whose runtime save fails is
// not recorded, and the retry records it exactly once (F5).
func TestWorkloadStartNotDoubleCountedAcrossSaveFailure(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	spec := LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/opt/game", Profile: ""}
	st := NewRuntimeState(spec, RuntimeStateStatusStarting)
	st.Phase = PhaseStarting
	st.HistoryContextHash = "sha256:ctx"
	st.HistorySuccess = &HistorySuccessIdentity{Snapshot: ContextSnapshot{Target: "/opt/game"}}
	if err := ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}
	launchID := st.LaunchID

	credit := func() error {
		_, err := FencedTransitionThen("g", dir, launchID, "",
			func(s *RuntimeState) error { s.Phase = PhaseActive; return nil },
			func(s *RuntimeState) { ApplyPinnedWorkloadStartLocked("g", dir, s, now) })
		return err
	}

	// Attempt 1: the runtime save fails → no credit.
	restore := SetSaveRuntimeStateFailHookForTesting(func() error { return errors.New("disk full") })
	if err := credit(); err == nil {
		t.Fatal("expected a save failure")
	}
	restore()
	if h, _ := LoadHistory("g", dir); h.Profiles[""] != nil && h.Profiles[""].WorkloadStarts != 0 {
		t.Fatalf("a save failure must not advance workloadStarts: %+v", h.Profiles[""])
	}

	// Attempt 2 (retry): commits and credits exactly once.
	if err := credit(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if h, _ := LoadHistory("g", dir); h.Profiles[""] == nil || h.Profiles[""].WorkloadStarts != 1 {
		t.Fatalf("the retry must credit exactly one workloadStart: %+v", h.Profiles[""])
	}
}
