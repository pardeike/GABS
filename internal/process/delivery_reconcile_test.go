package process

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// TestPendingDeliveryReplayNoDoubleCreditBeyondCap is the round-17 F5 P1
// reproduction: with more pending events than the dedup cap, a reconcile whose
// runtime-save (pruning) fails must NOT double-credit on replay — the dedup
// identity must remain durable until the pending record is durably removed.
func TestPendingDeliveryReplayNoDoubleCreditBeyondCap(t *testing.T) {
	dir := t.TempDir()
	var pending []PendingCredit
	for i := 0; i < 33; i++ {
		pending = append(pending, PendingCredit{ID: fmt.Sprintf("conn-%02d", i), ContextHash: "sha256:ctx"})
	}
	lid := seedDeliveryClaim(t, dir, "g1", pending, nil)

	// Reconcile with the final runtime save failing: the 33 history credits land,
	// but the pending list is not durably pruned.
	restore := SetSaveRuntimeStateFailHookForTesting(func() error { return errors.New("runtime down") })
	err := ReconcilePendingCredits("g1", dir, lid)
	restore()
	if err == nil {
		t.Fatal("expected the runtime save to fail")
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 33 {
		t.Fatalf("all 33 must credit exactly once: got %d", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); len(c.PendingDeliveries) != 33 {
		t.Fatalf("pending must remain durable when the save fails: got %d", len(c.PendingDeliveries))
	}

	// Replay with a healthy runtime save: must NOT double-credit the pending
	// records whose dedup ids would have been evicted by an LRU cap.
	if err := ReconcilePendingCredits("g1", dir, lid); err != nil {
		t.Fatal(err)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 33 {
		t.Fatalf("replay must not double-credit: got %d, want 33", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); len(c.PendingDeliveries) != 0 {
		t.Fatalf("pending must be pruned after the successful replay: got %d", len(c.PendingDeliveries))
	}
}

func seedDeliveryClaim(t *testing.T, dir, gameID string, pending []PendingCredit, attach *RuntimeAttachment) string {
	t.Helper()
	spec := LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game"}
	st := NewRuntimeState(spec, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = "sha256:ctx"
	st.PendingDeliveries = pending
	st.Attachment = attach
	if err := ClaimRuntimeState(gameID, dir, st); err != nil {
		t.Fatal(err)
	}
	return st.LaunchID
}

func deliveriesVerified(t *testing.T, dir, gameID, profile string) uint64 {
	t.Helper()
	h, _ := LoadHistory(gameID, dir)
	if e := h.Profiles[profile]; e != nil {
		return e.DeliveriesVerified
	}
	return 0
}

func creditedDelivery(t *testing.T, dir, gameID, profile, connectionID string) bool {
	t.Helper()
	h, _ := LoadHistory(gameID, dir)
	e := h.Profiles[profile]
	if e == nil {
		return false
	}
	for _, k := range e.CreditedPendingEvents {
		if k == "delivery:"+connectionID {
			return true
		}
	}
	return false
}

// TestPendingDeliveryCreditedWithoutLiveAttachment is the round-16 F5 finding-2
// reviewer reproduction: a verified delivery whose credit failed, then the
// bridge DISCONNECTED (no live Attachment), must still be reconciled from its
// self-contained pending event — independent of the attachment — exactly once.
// This is also the restart case: the pending event is durable in runtime.json.
func TestPendingDeliveryCreditedWithoutLiveAttachment(t *testing.T) {
	dir := t.TempDir()
	lid := seedDeliveryClaim(t, dir, "g1", []PendingCredit{{ID: "conn-A", ContextHash: "sha256:ctx"}}, nil)

	if err := ReconcilePendingCredits("g1", dir, lid); err != nil {
		t.Fatal(err)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 1 {
		t.Fatalf("a pending delivery must be credited without a live attachment, got %d", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); len(c.PendingDeliveries) != 0 {
		t.Fatalf("the pending delivery must be pruned once credited: %+v", c.PendingDeliveries)
	}
	// Exactly once.
	if err := ReconcilePendingCredits("g1", dir, lid); err != nil {
		t.Fatal(err)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 1 {
		t.Fatalf("a second reconcile must not double-credit, got %d", got)
	}
}

// TestPendingDeliveryNeverCreditsSuccessorConnection is the round-16 F5 finding-2
// reproduction: only A reported a verified delivery, then B attached (replacing
// Attachment) without reporting. Reconciliation must credit A's event, NEVER
// attribute A's verdict to the current attachment B.
func TestPendingDeliveryNeverCreditsSuccessorConnection(t *testing.T) {
	dir := t.TempDir()
	lid := seedDeliveryClaim(t, dir, "g1",
		[]PendingCredit{{ID: "conn-A", ContextHash: "sha256:ctx"}},
		&RuntimeAttachment{ConnectionID: "conn-B", OwnerPID: 1, OwnerPIDStartTime: 1})

	if err := ReconcilePendingCredits("g1", dir, lid); err != nil {
		t.Fatal(err)
	}
	// Exactly A's pending delivery credits — the credit is keyed by the pending
	// record's own connection id (conn-A), never the current attachment (conn-B),
	// so B can never be credited from A's verdict. The counter is the durable
	// proof; A's marker is GC'd behind the durable prune (round 17 P1 ordering).
	if got := deliveriesVerified(t, dir, "g1", ""); got != 1 {
		t.Fatalf("exactly A's delivery must credit, got %d", got)
	}
	if creditedDelivery(t, dir, "g1", "", "conn-B") {
		t.Fatal("delivery:conn-B must NEVER be credited from A's verdict")
	}
	if creditedDelivery(t, dir, "g1", "", "conn-A") {
		t.Fatal("A's marker must be GC'd after its record is durably pruned")
	}
	// Idempotent: a replay finds the record pruned and re-credits nothing.
	if err := ReconcilePendingCredits("g1", dir, lid); err != nil {
		t.Fatal(err)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 1 {
		t.Fatalf("replay must not double-credit A, got %d", got)
	}
}

// TestSuccessorPartialPreservesPendingVerified is the round-16 F5 reproduction:
// A reported verified (pending), then successor B reports partial. B's partial
// verdict renders, but B is NOT credited and A's pending verified credit is
// preserved.
func TestSuccessorPartialPreservesPendingVerified(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	managed := map[string]string{"K": "v"}
	context := map[string]string{"CONTENT": "combat-pack"}
	digests, err := ComputeContextDigests([]string{"-p", "combat"}, cwd, false, managed, context, nil)
	if err != nil {
		t.Fatal(err)
	}

	spec := LaunchSpec{GameId: "g1", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := NewRuntimeState(spec, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = "sha256:ctx"
	st.ContextDigests = digests
	st.ContextDelivery = &RuntimeContextDelivery{Overall: DeliveryVerified}
	st.PendingDeliveries = []PendingCredit{{ID: "conn-A", ContextHash: "sha256:ctx"}}
	st.Attachment = &RuntimeAttachment{ConnectionID: "conn-B", OwnerPID: 1, OwnerPIDStartTime: 1}
	lid := st.LaunchID
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}

	// B reports a MISMATCHING (partial) welcome: wrong context value.
	bObs := &ObservedContext{Argv: []string{"/opt/game", "-p", "combat"}, Cwd: cwd,
		EnvValues: map[string]string{"K": "v", "CONTENT": "WRONG-pack"}}
	if err := AppendPendingDelivery("g1", dir, lid, "conn-B", bObs, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	cur, _ := LoadRuntimeState("g1", dir)
	if cur.ContextDelivery == nil || cur.ContextDelivery.Overall == DeliveryVerified {
		t.Fatalf("B's non-verified verdict must render: %+v", cur.ContextDelivery)
	}
	if len(cur.PendingDeliveries) != 1 || cur.PendingDeliveries[0].ID != "conn-A" {
		t.Fatalf("A's pending verified credit must be preserved, B not appended: %+v", cur.PendingDeliveries)
	}
	// Reconcile: only A credits.
	if err := ReconcilePendingCredits("g1", dir, lid); err != nil {
		t.Fatal(err)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 1 || creditedDelivery(t, dir, "g1", "", "conn-B") {
		t.Fatalf("only A's verified delivery may credit, got %d creditedB=%v", got, creditedDelivery(t, dir, "g1", "", "conn-B"))
	}
}

// TestPendingDeliveryReconciledAtClaimRemoval is the round-16 gap the reviewer
// had not yet written but the advisor flagged: a verified delivery pending when
// the claim is REMOVED (here by a superseding start) must be credited before
// removal, not lost with the claim.
func TestPendingDeliveryReconciledAtClaimRemoval(t *testing.T) {
	dir := t.TempDir()
	swapLivenessProbes(t,
		func(*launch.ResolvedHook, string, string) (string, HookResult) { return StatusStopped, HookResult{ExitCode: 1} },
		nil, func(string) ([]int, error) { return nil, nil })

	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st.Phase = PhaseStopping
	st.SpawnState = SpawnStateSpawned
	st.Lifecycle = &launch.ResolvedLifecycle{Status: verifyStatusHook()}
	st.HistoryContextHash = "sha256:ctx"
	st.PendingDeliveries = []PendingCredit{{ID: "conn-A", Profile: "combat", ContextHash: "sha256:ctx"}}
	st.Operation = &RuntimeOperation{
		OperationID: NewFencingID(), Action: OperationActionStop,
		ExecutorPID: 999999999, ExecutorPIDStartTime: 1,
		AttemptStartedAt: time.Now().UTC().Add(-time.Hour),
		Deadline:         time.Now().UTC().Add(-time.Minute),
	}
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}

	res, err := GateStart(gateFor("g1", dir))
	if err != nil {
		t.Fatalf("GateStart: %v", err)
	}
	if res.Refusal != nil {
		t.Fatalf("clearable: %+v", res.Refusal)
	}
	if got := deliveriesVerified(t, dir, "g1", "combat"); got != 1 {
		t.Fatalf("a pending delivery must be credited before the claim is removed, got %d", got)
	}
}

// TestPendingDeliveryPreservedAtSaturation is the round-17 F5 P2 reproduction:
// with the pending list already full, a NEW verified welcome report (already
// consumed) must still be persisted — never dropped at a cap.
func TestPendingDeliveryPreservedAtSaturation(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	managed := map[string]string{"K": "v"}
	context := map[string]string{"CONTENT": "combat-pack"}
	digests, err := ComputeContextDigests([]string{"-p", "combat"}, cwd, false, managed, context, nil)
	if err != nil {
		t.Fatal(err)
	}

	var pending []PendingCredit
	for i := 0; i < 512; i++ { // well past any prior cap
		pending = append(pending, PendingCredit{ID: fmt.Sprintf("old-%03d", i), ContextHash: "sha256:ctx"})
	}
	spec := LaunchSpec{GameId: "g1", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := NewRuntimeState(spec, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = "sha256:ctx"
	st.ContextDigests = digests
	st.PendingDeliveries = pending
	st.Attachment = &RuntimeAttachment{ConnectionID: "overflow-conn", OwnerPID: 1, OwnerPIDStartTime: 1}
	lid := st.LaunchID
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}

	obs := &ObservedContext{Argv: []string{"/opt/game", "-p", "combat"}, Cwd: cwd,
		EnvValues: map[string]string{"K": "v", "CONTENT": "combat-pack"}}
	if err := AppendPendingDelivery("g1", dir, lid, "overflow-conn", obs, time.Now().UTC()); err != nil {
		t.Fatalf("append at saturation must not error: %v", err)
	}

	cur, _ := LoadRuntimeState("g1", dir)
	found := false
	for _, p := range cur.PendingDeliveries {
		if p.ID == "overflow-conn" {
			found = true
		}
	}
	if !found {
		t.Fatal("the one-shot verified delivery must be persisted at saturation, never dropped")
	}
}

// TestPendingDeliveryCreditedByStatusFunnelRemoval covers the round-16 F5 status
// funnel: a dead active claim (no operation) carrying a pending delivery is
// removed by RemoveRuntimeStateIfCurrent, which must credit the delivery first.
func TestPendingDeliveryCreditedByStatusFunnelRemoval(t *testing.T) {
	dir := t.TempDir()
	lid := seedDeliveryClaim(t, dir, "g1", []PendingCredit{{ID: "conn-A", ContextHash: "sha256:ctx"}}, nil)

	if err := RemoveRuntimeStateIfCurrent("g1", dir, "inst", lid, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 1 {
		t.Fatalf("the status-funnel removal must credit the pending delivery, got %d", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); c != nil {
		t.Fatalf("the claim must be removed after crediting: %+v", c)
	}
}

// TestPendingDeliveryNotStrandedWhenHistoryFails: if the credit write still
// fails at removal, the removal ABORTS and the claim + pending delivery survive
// for a later retry — never stranded (round 16 F5).
func TestPendingDeliveryNotStrandedWhenHistoryFails(t *testing.T) {
	dir := t.TempDir()
	lid := seedDeliveryClaim(t, dir, "g1", []PendingCredit{{ID: "conn-A", ContextHash: "sha256:ctx"}}, nil)

	restore := SetSaveHistoryFailHookForTesting(func() error { return errors.New("history down") })
	err := RemoveRuntimeStateIfCurrent("g1", dir, "inst", lid, func(string) bool { return false })
	restore()
	if err == nil {
		t.Fatal("a credit-write failure must abort the removal")
	}
	c, _ := LoadRuntimeState("g1", dir)
	if c == nil || len(c.PendingDeliveries) != 1 {
		t.Fatalf("the pending delivery must survive a failed credit, not be stranded: %+v", c)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 0 {
		t.Fatalf("no credit may land while history fails, got %d", got)
	}
	// History recovers: the retry credits exactly once and removes.
	if err := RemoveRuntimeStateIfCurrent("g1", dir, "inst", lid, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if got := deliveriesVerified(t, dir, "g1", ""); got != 1 {
		t.Fatalf("the retry must credit exactly once, got %d", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); c != nil {
		t.Fatalf("the claim must be removed after the retry: %+v", c)
	}
}
