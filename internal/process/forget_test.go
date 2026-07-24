package process

import (
	"errors"
	"testing"
)

// A stale digest — the claim was replaced by a successor while the user looked at
// the old evidence — must refuse removal (round-19 P1 TOCTOU), leaving the
// current claim in place.
func TestForceForgetRefusesOnChangedClaim(t *testing.T) {
	dir := t.TempDir()
	st := NewRuntimeState(LaunchSpec{GameId: "g1", Mode: "DirectPath", PathOrId: "/opt/g"}, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}

	err := ForceForgetRuntimeClaim("g1", dir, "0000-a-stale-digest-that-cannot-match", false)
	if !errors.Is(err, ErrForgetClaimChanged) {
		t.Fatalf("a stale digest must refuse with ErrForgetClaimChanged, got: %v", err)
	}
	if !RuntimeClaimExists("g1", dir) {
		t.Fatal("the claim must survive a refused forget")
	}
}

// A readable claim's pending history credits must be CREDITED before removal
// when history is healthy — forget bypasses liveness, not the F5 "a pending fact
// survives until credited or explicitly discarded" invariant (round-19 P1).
func TestForceForgetCreditsPendingBeforeRemoval(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	st := NewRuntimeState(LaunchSpec{GameId: "g1", Mode: "DirectPath", PathOrId: "/opt/g", Profile: "combat"}, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.HistoryContextHash = hash
	st.PendingCleanStops = []PendingCredit{{ID: "op-1", Profile: "combat", ContextHash: hash}}
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}

	_, digest, found, err := ReadRuntimeClaim("g1", dir)
	if err != nil || !found {
		t.Fatalf("read claim: found=%v err=%v", found, err)
	}
	if err := ForceForgetRuntimeClaim("g1", dir, digest, false); err != nil {
		t.Fatalf("forget with healthy history must succeed (crediting the pending fact): %v", err)
	}
	if RuntimeClaimExists("g1", dir) {
		t.Fatal("the claim must be removed")
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 1 {
		t.Fatalf("the pending clean-stop must be CREDITED, not silently discarded: got %d", got)
	}
}
