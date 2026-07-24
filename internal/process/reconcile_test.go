package process

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// TestPendingCleanStopReplayNoDoubleCreditBeyondCap is the clean-stop analogue
// of the P1 delivery replay: beyond any dedup cap, a reconcile whose runtime
// prune fails must not double-credit clean stops on replay.
func TestPendingCleanStopReplayNoDoubleCreditBeyondCap(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	var pending []PendingCredit
	for i := 0; i < 40; i++ {
		pending = append(pending, PendingCredit{ID: fmt.Sprintf("op-%03d", i), Profile: "combat", ContextHash: hash})
	}
	spec := LaunchSpec{GameId: "g1", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := NewRuntimeState(spec, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = hash
	st.PendingCleanStops = pending
	lid := st.LaunchID
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}

	restore := SetSaveRuntimeStateFailHookForTesting(func() error { return errors.New("runtime down") })
	err := ReconcilePendingCredits("g1", dir, lid)
	restore()
	if err == nil {
		t.Fatal("expected the runtime save to fail")
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 40 {
		t.Fatalf("all 40 must credit once: got %d", got)
	}
	if err := ReconcilePendingCredits("g1", dir, lid); err != nil {
		t.Fatal(err)
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 40 {
		t.Fatalf("replay must not double-credit: got %d, want 40", got)
	}
}

// TestInterveningReconcileDoesNotReplayStopCredit is the round-17 F5 P1 (final)
// reproduction: a stop completion's credit is committed to history but its claim
// removal fails, so the stop event is durable in history yet was never written
// to runtime.json. An UNRELATED reconcile of another pending event must not
// garbage-collect that stop's marker — else the still-current completion,
// retried, credits the same clean stop twice. The GC must run only after the
// runtime transition (prune/removal) that de-references the event is durable.
func TestInterveningReconcileDoesNotReplayStopCredit(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	opA := NewFencingID()
	connB := NewFencingID()

	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st.Phase = PhaseStopping
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = hash
	st.PendingDeliveries = []PendingCredit{{ID: connB, Profile: "combat", ContextHash: hash}}
	st.Operation = &RuntimeOperation{
		OperationID: opA, Action: OperationActionStop,
		ExecutorPID: os.Getpid(), ExecutorPIDStartTime: 1,
		AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute),
	}
	launchID := st.LaunchID
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}
	req := StopRequest{GameID: "g1", ConfigDir: dir, InstanceID: "inst", HistoryProfile: "combat", HistoryContextHash: hash}

	// (1-2) Complete stop A; the removal fails, so A's credit is durable in
	// history while the durable claim still lists only delivery B.
	restore := SetRemoveRuntimeStateFailHookForTesting(func() error { return errors.New("unlink down") })
	err := removeRuntimeStateForStopCompletion(req, launchID, opA)
	restore()
	if err == nil {
		t.Fatal("expected the removal to fail after the credit committed")
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 1 {
		t.Fatalf("stop A must credit once: got %d", got)
	}
	c, _ := LoadRuntimeState("g1", dir)
	if c == nil || len(c.PendingDeliveries) != 1 {
		t.Fatalf("the durable claim must still list delivery B: %+v", c)
	}

	// (3) An unrelated reconcile of delivery B must NOT drop stop A's marker.
	if err := ReconcilePendingCredits("g1", dir, launchID); err != nil {
		t.Fatal(err)
	}

	// (4) Retry the still-current fenced completion A: it must credit nothing.
	if err := removeRuntimeStateForStopCompletion(req, launchID, opA); err != nil {
		t.Fatalf("retry must succeed: %v", err)
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 1 {
		t.Fatalf("intervening reconciliation made the stop credit replay: got %d, want 1", got)
	}
}

// TestPendingCleanStopDeleterReplayNoDoubleCredit pins exactly-once on the
// DELETER path (a verified stop completion): the credit commits inside the lock,
// then RemoveRuntimeState fails, so the claim survives already-credited — a
// retry of the completion must re-credit nothing. This exercises the round-17
// P1 lifetime through stop_gate's removeRuntimeStateForStopCompletion rather
// than the live ReconcilePendingCredits path.
func TestPendingCleanStopDeleterReplayNoDoubleCredit(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	opID := NewFencingID()

	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st.Phase = PhaseStopping
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = hash
	for i := 0; i < 40; i++ { // beyond the old LRU cap
		st.PendingCleanStops = append(st.PendingCleanStops, PendingCredit{ID: fmt.Sprintf("old-%03d", i), Profile: "combat", ContextHash: hash})
	}
	st.Operation = &RuntimeOperation{
		OperationID: opID, Action: OperationActionStop,
		ExecutorPID: os.Getpid(), ExecutorPIDStartTime: 1,
		AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute),
	}
	launchID := st.LaunchID
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}
	req := StopRequest{GameID: "g1", ConfigDir: dir, InstanceID: "inst", HistoryProfile: "combat", HistoryContextHash: hash}

	// Credit commits, but the file removal fails -> the claim survives credited.
	restore := SetRemoveRuntimeStateFailHookForTesting(func() error { return errors.New("unlink down") })
	err := removeRuntimeStateForStopCompletion(req, launchID, opID)
	restore()
	if err == nil {
		t.Fatal("expected the removal to fail after the credit committed")
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 41 {
		t.Fatalf("first pass must credit all 41 clean stops once: got %d", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); c == nil {
		t.Fatal("the claim must survive a removal failure for a later retry")
	}

	// Retry the same completion: nothing may credit twice, and the claim clears.
	if err := removeRuntimeStateForStopCompletion(req, launchID, opID); err != nil {
		t.Fatalf("retry must succeed: %v", err)
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 41 {
		t.Fatalf("deleter-path replay must not double-credit: got %d, want 41", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); c != nil {
		t.Fatalf("the claim must be removed after the retry: %+v", c)
	}
}

// TestPendingCleanStopPreservedAtSaturation is the round-17 F5 P2 reproduction:
// a verified-termination clean stop must be recorded even when the pending list
// is already full — the action already executed, so a drop would be permanent
// loss.
func TestPendingCleanStopPreservedAtSaturation(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	opID := NewFencingID()

	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st.Phase = PhaseStopping
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = hash
	for i := 0; i < 512; i++ { // well past any prior cap
		st.PendingCleanStops = append(st.PendingCleanStops, PendingCredit{ID: fmt.Sprintf("old-%03d", i), Profile: "combat", ContextHash: hash})
	}
	st.Operation = &RuntimeOperation{
		OperationID: opID, Action: OperationActionStop,
		ExecutorPID: os.Getpid(), ExecutorPIDStartTime: 1,
		AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute),
	}
	launchID := st.LaunchID
	if err := ClaimRuntimeState("g1", dir, st); err != nil {
		t.Fatal(err)
	}

	req := StopRequest{GameID: "g1", ConfigDir: dir, InstanceID: "inst", HistoryProfile: "combat", HistoryContextHash: hash}
	if err := removeRuntimeStateForStopCompletion(req, launchID, opID); err != nil {
		t.Fatalf("completion at saturation must not error: %v", err)
	}

	// All 513 clean stops (512 pending + the completion) credited; claim removed.
	if got := cleanStops(t, dir, "g1", "combat"); got != 513 {
		t.Fatalf("every verified clean stop must credit at saturation: got %d, want 513", got)
	}
	if c, _ := LoadRuntimeState("g1", dir); c != nil {
		t.Fatalf("the claim must be removed after crediting: %+v", c)
	}
}

// failedVerifiedStop runs a real stop that VERIFIES termination while its
// clean-stop history write fails, leaving the claim with one self-contained
// pending clean-stop credit (round 16 F5). Returns the pending operationID.
func failedVerifiedStop(t *testing.T, dir, gameID, hash, profile string) string {
	t.Helper()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probeReturning(StatusStopped), nil, nil)
	st := activeStopClaim(t, gameID, dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Stop: stopHook()})
	st.StopProcessName = "game-workload"
	st.HistoryContextHash = hash
	publishClaim(t, dir, st)

	req := stopReq(gameID, dir)
	req.HistoryContextHash = hash
	req.HistoryProfile = profile

	restore := SetSaveHistoryFailHookForTesting(func() error { return errors.New("history disk full") })
	outcome, refusal, err := ExecuteStopAction(req)
	restore()
	if err != nil || refusal != nil || outcome.Code != OutcomeTerminated || outcome.ClaimRemoved {
		t.Fatalf("setup: verified stop with a failed credit expected: %+v refusal=%+v err=%v", outcome, refusal, err)
	}
	claim, _ := LoadRuntimeState(gameID, dir)
	if claim == nil || len(claim.PendingCleanStops) != 1 {
		t.Fatalf("setup: exactly one self-contained pending clean-stop expected: %+v", claim)
	}
	if h, _ := LoadHistory(gameID, dir); h.Profiles[profile] != nil && h.Profiles[profile].CleanStops != 0 {
		t.Fatalf("setup: no credit may exist yet: %+v", h.Profiles[profile])
	}
	return claim.PendingCleanStops[0].ID
}

func cleanStops(t *testing.T, dir, gameID, profile string) uint64 {
	t.Helper()
	h, _ := LoadHistory(gameID, dir)
	if e := h.Profiles[profile]; e != nil {
		return e.CleanStops
	}
	return 0
}

// TestPendingCleanStopCreditedAfterOperationCleared is the round-16 F5 finding-1
// reviewer reproduction: after a verified stop's history write fails, a later
// failed stop clears the operation, so the pending event's identity is no longer
// derivable from Operation. A normal cleanup must still credit the ORIGINAL
// verified stop by its stored operationID.
func TestPendingCleanStopCreditedAfterOperationCleared(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	swapLivenessProbes(t,
		func(*launch.ResolvedHook, string, string) (string, HookResult) { return StatusStopped, HookResult{ExitCode: 1} },
		nil, func(string) ([]int, error) { return nil, nil })

	opA := failedVerifiedStop(t, dir, "g1", hash, "combat")

	// A subsequent failed stop cleared Operation (persistStopCompletion), leaving
	// the pending clean-stop behind with NO current operation to derive it from.
	claim, _ := LoadRuntimeState("g1", dir)
	claim.Operation = nil
	if err := SaveRuntimeState("g1", dir, *claim); err != nil {
		t.Fatal(err)
	}

	// A superseding start clears the stopped claim → must credit the original
	// pending clean-stop by its STORED operationID, though Operation is nil.
	res, err := GateStart(gateFor("g1", dir))
	if err != nil {
		t.Fatalf("GateStart: %v", err)
	}
	if res.Refusal != nil {
		t.Fatalf("the stopped claim must be clearable: %+v", res.Refusal)
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 1 {
		t.Fatalf("the pending clean-stop (op %s) must be credited by its stored id, got cleanStops=%d", opA, got)
	}
}

// TestBothVerifiedStopsCounted is the round-16 F5 reviewer reproduction: a
// pending clean-stop plus a later second VERIFIED stop must count as two logical
// events — the pending list preserves the first while the second is appended.
func TestBothVerifiedStopsCounted(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"

	opA := failedVerifiedStop(t, dir, "g1", hash, "combat")

	// Expire the first stop's operation so a second stop can admit and replace it.
	claim, _ := LoadRuntimeState("g1", dir)
	claim.Operation.Deadline = time.Now().UTC().Add(-time.Minute)
	claim.Operation.ExecutorPID = 999999999
	if err := SaveRuntimeState("g1", dir, *claim); err != nil {
		t.Fatal(err)
	}

	// A second verified stop (history healthy now) admits, appends its own
	// pending event, and reconciles BOTH → cleanStops == 2, claim removed.
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probeReturning(StatusStopped), nil, nil)
	req := stopReq("g1", dir)
	req.HistoryContextHash = hash
	req.HistoryProfile = "combat"
	outcome, refusal, err := ExecuteStopAction(req)
	if err != nil || refusal != nil {
		t.Fatalf("second stop: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminated || !outcome.ClaimRemoved {
		t.Fatalf("the second verified stop must terminate and remove: %+v", outcome)
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 2 {
		t.Fatalf("both verified stops must count (op %s + second), got cleanStops=%d", opA, got)
	}
}

// TestInterruptedRecoveryReplaysCleanStopCredit: a verified-stop credit failure
// leaves a self-contained pending event, and ordinary interrupted recovery
// credits it exactly once before removal.
func TestInterruptedRecoveryReplaysCleanStopCredit(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	swapLivenessProbes(t,
		func(*launch.ResolvedHook, string, string) (string, HookResult) { return StatusStopped, HookResult{ExitCode: 1} },
		nil, func(string) ([]int, error) { return nil, nil })

	_ = failedVerifiedStop(t, dir, "g1", hash, "combat")

	claim, _ := LoadRuntimeState("g1", dir)
	claim.Operation.Deadline = time.Now().UTC().Add(-time.Minute)
	claim.Operation.ExecutorPID = 999999999
	if err := SaveRuntimeState("g1", dir, *claim); err != nil {
		t.Fatal(err)
	}

	rec := recoverNow(t, "g1", dir, false)
	if rec == nil || !rec.Removed {
		t.Fatalf("recovery must remove the definitively-stopped claim: %+v", rec)
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 1 {
		t.Fatalf("recovery must credit the clean-stop exactly once, got %d", got)
	}
	if got, _ := LoadRuntimeState("g1", dir); got != nil {
		t.Fatalf("the claim must be gone after recovery: %+v", got)
	}
}

// TestSupersedingStartCreditsPendingCleanStop: a games_start superseding a claim
// that carries a pending clean-stop credits it (by stored id) before clearing.
func TestSupersedingStartCreditsPendingCleanStop(t *testing.T) {
	dir := t.TempDir()
	const hash = "sha256:ctx"
	swapLivenessProbes(t,
		func(*launch.ResolvedHook, string, string) (string, HookResult) { return StatusStopped, HookResult{ExitCode: 1} },
		nil, func(string) ([]int, error) { return nil, nil })

	opID := NewFencingID()
	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st.Phase = PhaseStopping
	st.SpawnState = SpawnStateSpawned
	st.Lifecycle = &launch.ResolvedLifecycle{Status: verifyStatusHook()}
	st.HistoryContextHash = hash
	st.PendingCleanStops = []PendingCredit{{ID: opID, Profile: "combat", ContextHash: hash}}
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
		t.Fatalf("the stale claim must be clearable: %+v", res.Refusal)
	}
	if got := cleanStops(t, dir, "g1", "combat"); got != 1 {
		t.Fatalf("a superseding start must credit the pending clean-stop once, got %d", got)
	}
}

// TestConcurrentDeleterDoesNotLoseOrDoubleCleanStop hammers a verified-stop
// credit failure against a concurrent interrupted-recovery of the same claim.
// Because the pending event is published under the SAME lock that observes the
// credit failure (no post-release gap, round 16 F5), the clean-stop is credited
// exactly once — never lost to a racing deleter, never doubled.
func TestConcurrentDeleterDoesNotLoseOrDoubleCleanStop(t *testing.T) {
	for iter := 0; iter < 40; iter++ {
		dir := t.TempDir()
		const hash = "sha256:ctx"
		swapLivenessProbes(t,
			func(*launch.ResolvedHook, string, string) (string, HookResult) { return StatusStopped, HookResult{ExitCode: 1} },
			nil, func(string) ([]int, error) { return nil, nil })

		_ = failedVerifiedStop(t, dir, "g1", hash, "combat")
		// Expire the operation so recovery is eligible.
		claim, _ := LoadRuntimeState("g1", dir)
		claim.Operation.Deadline = time.Now().UTC().Add(-time.Minute)
		claim.Operation.ExecutorPID = 999999999
		if err := SaveRuntimeState("g1", dir, *claim); err != nil {
			t.Fatal(err)
		}

		done := make(chan struct{}, 2)
		for i := 0; i < 2; i++ {
			go func() {
				defer func() { done <- struct{}{} }()
				if c, _ := LoadRuntimeState("g1", dir); c != nil {
					_, _ = RecoverInterruptedClaim("g1", dir, "inst", c, false, func(string) bool { return false }, time.Now().UTC())
				}
			}()
		}
		<-done
		<-done

		if got := cleanStops(t, dir, "g1", "combat"); got != 1 {
			t.Fatalf("iter %d: clean-stop must be credited exactly once under concurrency, got %d", iter, got)
		}
	}
}
