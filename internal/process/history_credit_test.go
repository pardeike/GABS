package process

import (
	"testing"
	"time"
)

// TestWorkloadStartCreditIdempotentByLaunchID covers F9: the Stage-4 credit is
// written inside the runtime-state transition callback BEFORE runtime.json is
// saved, so a retried promote (after a claim-save failure) or a passive
// promotion after a crash between the two files must not double-count. Keyed by
// launchID, a repeat is a no-op; a different launch counts again.
func TestWorkloadStartCreditIdempotentByLaunchID(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	st := &RuntimeState{
		Profile:            "",
		HistoryContextHash: "sha256:ctx",
		HistorySuccess:     &HistorySuccessIdentity{Snapshot: ContextSnapshot{Target: "/opt/game"}},
		LaunchID:           "launch-1",
	}

	ApplyPinnedWorkloadStartLocked("g", dir, st, now)
	ApplyPinnedWorkloadStartLocked("g", dir, st, now) // retried promote / crash replay
	if h, _ := LoadHistory("g", dir); h.Profiles[""].WorkloadStarts != 1 {
		t.Fatalf("a repeated credit for the same launch must count once: %d", h.Profiles[""].WorkloadStarts)
	}

	st.LaunchID = "launch-2" // a genuinely new launch
	ApplyPinnedWorkloadStartLocked("g", dir, st, now)
	if h, _ := LoadHistory("g", dir); h.Profiles[""].WorkloadStarts != 2 {
		t.Fatalf("a new launch must credit again: %d", h.Profiles[""].WorkloadStarts)
	}
}

// TestActionFailureRecordsInputNames covers F7: an input-bearing accepted
// attempt's failure serializes its supplied input names (never values).
func TestActionFailureRecordsInputNames(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	ApplyActionFailureLocked("g", dir, "", "sha256:ctx", "unobserved", CauseConfig, []string{"scenario"}, now)
	h, _ := LoadHistory("g", dir)
	e := h.Profiles[""]
	if e == nil || e.LastFailure == nil {
		t.Fatalf("the failure must be recorded: %+v", e)
	}
	if len(e.LastFailure.InputNames) != 1 || e.LastFailure.InputNames[0] != "scenario" {
		t.Fatalf("the failure must record the supplied input names: %+v", e.LastFailure.InputNames)
	}
}
