package process

import (
	"errors"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

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
