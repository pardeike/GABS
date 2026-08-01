package process

import (
	"errors"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// deadExecutorOp is an operation whose executor fingerprint is provably a
// different process: PID exists (ours) but the start time cannot match.
func deadExecutorOp(t *testing.T, action string, deadline time.Time) *RuntimeOperation {
	t.Helper()
	pid, start := ownFingerprint(t)
	return &RuntimeOperation{
		OperationID: NewFencingID(), Action: action,
		ExecutorPID: pid, ExecutorPIDStartTime: start + 987654,
		AttemptStartedAt: time.Now().UTC().Add(-time.Minute),
		Deadline:         deadline,
	}
}

func recoverNow(t *testing.T, gameID, dir string, gabpLive bool) *ClaimRecovery {
	t.Helper()
	claim, err := LoadRuntimeState(gameID, dir)
	if err != nil || claim == nil {
		t.Fatalf("claim must exist before recovery: %v", err)
	}
	rec, err := RecoverInterruptedClaim(gameID, dir, "inst-recover", claim, gabpLive, func(string) bool { return gabpLive }, time.Now().UTC())
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	return rec
}

func TestRecoverStoppingClaimWorkloadRunning(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)

	st := activeStopClaim(t, "g1", dir, nil)
	st.Phase = PhaseStopping
	st.GamePID = pid
	st.PIDStartTime = start
	st.Operation = deadExecutorOp(t, OperationActionStop, time.Now().UTC().Add(time.Minute))
	publishClaim(t, dir, st)

	rec := recoverNow(t, "g1", dir, false)
	if rec == nil || rec.Removed {
		t.Fatalf("expected a kept, normalized claim: %+v", rec)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim.Phase != PhaseActive || claim.Status != RuntimeStateStatusRunning || claim.Operation != nil {
		t.Fatalf("running workload must normalize to active with the operation cleared: %+v", claim)
	}
	lar := claim.LastActionResult
	if lar == nil || lar.Action != OperationActionStop || lar.Outcome != OutcomeInterrupted {
		t.Fatalf("the orphaned attempt must be recorded as interrupted: %+v", lar)
	}
}

func TestRecoverStoppingClaimWorkloadUnknown(t *testing.T) {
	dir := t.TempDir()
	swapLivenessProbes(t, nil, nil, func(string) ([]int, error) { return nil, &scanError{} })

	st := activeStopClaim(t, "g1", dir, nil)
	st.Phase = PhaseStopping
	st.StopProcessName = "game-workload"
	st.Operation = deadExecutorOp(t, OperationActionStop, time.Now().UTC().Add(time.Minute))
	publishClaim(t, dir, st)

	rec := recoverNow(t, "g1", dir, false)
	if rec == nil || rec.Removed {
		t.Fatalf("unknown never cleans state: %+v", rec)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim.Phase != PhaseActive || claim.Operation != nil {
		t.Fatalf("unknown normalizes to active with the unknown verdict reported: %+v", claim)
	}
	if claim.LastActionResult == nil || claim.LastActionResult.Outcome != OutcomeInterrupted {
		t.Fatalf("the interruption must be recorded: %+v", claim.LastActionResult)
	}
	if rec.Evidence.Verdict != StatusUnknown {
		t.Fatalf("the unknown verdict must be surfaced: %+v", rec.Evidence)
	}
}

func TestRecoverKillingClaimWorkloadStopped(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)

	st := activeStopClaim(t, "g1", dir, nil)
	st.Phase = PhaseKilling
	st.GamePID = pid
	st.PIDStartTime = start + 13579 // fingerprint mismatch: PID reused, workload gone
	st.Operation = deadExecutorOp(t, OperationActionKill, time.Now().UTC().Add(-time.Second))
	publishClaim(t, dir, st)

	rec := recoverNow(t, "g1", dir, false)
	if rec == nil || !rec.Removed {
		t.Fatalf("definitive stopped evidence must remove the claim: %+v", rec)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim != nil {
		t.Fatalf("claim must be gone: %+v", claim)
	}
}

func TestRecoveryPreservesAttachmentAndFencesLateCompletion(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)

	// A CLI stop executor died while the server owning the bridge stays
	// alive: normalize as interrupted, leave the attachment untouched.
	st := activeStopClaim(t, "g1", dir, nil)
	st.Phase = PhaseStopping
	st.Operation = deadExecutorOp(t, OperationActionStop, time.Now().UTC().Add(time.Minute))
	oldOpID := st.Operation.OperationID
	st.Attachment = &RuntimeAttachment{
		ConnectionID: NewFencingID(), OwnerInstanceID: "server-owner",
		OwnerPID: pid, OwnerPIDStartTime: start,
		ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st)

	rec := recoverNow(t, "g1", dir, false)
	if rec == nil || rec.Removed {
		t.Fatalf("the fresh attachment lease is running-evidence; claim kept: %+v", rec)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim.Phase != PhaseActive || claim.Operation != nil {
		t.Fatalf("expected normalized active claim: %+v", claim)
	}
	if claim.Attachment == nil || claim.Attachment.OwnerInstanceID != "server-owner" {
		t.Fatalf("the attachment record must survive recovery untouched: %+v", claim.Attachment)
	}
	// The dead executor's late completion is rejected by its operationID.
	if _, err := FencedTransition("g1", dir, claim.LaunchID, oldOpID, func(s *RuntimeState) error {
		s.Phase = PhaseActive
		return nil
	}); !errors.Is(err, ErrFencingViolation) {
		t.Fatalf("a late completion from the dead executor must be rejected, got %v", err)
	}
}

func TestRecoverySpawningWindowVerdicts(t *testing.T) {
	installProbeFake(t)
	cases := []struct {
		verdict     string
		wantRemoved bool
		wantPhase   string // when kept
		wantOpKept  bool
	}{
		{verdict: StatusRunning, wantPhase: PhaseActive},
		{verdict: StatusStopped, wantRemoved: true},
		{verdict: StatusUnknown, wantPhase: PhaseStarting, wantOpKept: true}, // occupied, untouched
	}
	for _, c := range cases {
		dir := t.TempDir()
		st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
		st.SpawnState = SpawnStateSpawning // crash between spawning and PID persist
		st.Lifecycle = probeHookLifecycle(c.verdict)
		st.Operation = deadExecutorOp(t, OperationActionStart, time.Now().UTC().Add(time.Minute))
		publishClaim(t, dir, st)

		rec := recoverNow(t, "g1", dir, false)
		if rec == nil {
			t.Fatalf("%s: a dead start attempt must be evaluated", c.verdict)
		}
		if rec.Removed != c.wantRemoved {
			t.Fatalf("%s: removed=%v, want %v", c.verdict, rec.Removed, c.wantRemoved)
		}
		if c.wantRemoved {
			if claim, _ := LoadRuntimeState("g1", dir); claim != nil {
				t.Fatalf("%s: claim must be gone: %+v", c.verdict, claim)
			}
			continue
		}
		claim, _ := LoadRuntimeState("g1", dir)
		if claim == nil || claim.Phase != c.wantPhase {
			t.Fatalf("%s: phase=%v, want %v", c.verdict, claim, c.wantPhase)
		}
		if c.wantOpKept != (claim.Operation != nil) {
			t.Fatalf("%s: operation kept=%v, want %v", c.verdict, claim.Operation != nil, c.wantOpKept)
		}
		if !c.wantOpKept && claim.Status != RuntimeStateStatusRunning {
			t.Fatalf("%s: promotion must set status running: %+v", c.verdict, claim)
		}
		if claim.LastActionResult != nil {
			t.Fatalf("%s: a dead start attempt is not a stop/kill result: %+v", c.verdict, claim.LastActionResult)
		}
	}
}

func TestRecoverPreflightDeadOwnerRemovesWithoutProbes(t *testing.T) {
	dir := t.TempDir()
	hookCalls := 0
	swapLivenessProbes(t, hookReturning(StatusRunning, &hookCalls), nil, nil)

	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
	st.Lifecycle = probeHookLifecycle(StatusRunning) // must never be consulted
	st.Operation = deadExecutorOp(t, OperationActionStart, time.Now().UTC().Add(time.Minute))
	publishClaim(t, dir, st) // spawnState preflight from NewRuntimeState

	rec := recoverNow(t, "g1", dir, false)
	if rec == nil || !rec.Removed {
		t.Fatalf("preflight + dead owner is the one safe removal: %+v", rec)
	}
	if hookCalls != 0 {
		t.Fatalf("preflight removal needs no liveness puzzle; hook ran %d times", hookCalls)
	}
}

func TestRecoveryNoOpForLiveOperation(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)

	st := activeStopClaim(t, "g1", dir, nil)
	st.Phase = PhaseStopping
	st.Operation = &RuntimeOperation{
		OperationID: NewFencingID(), Action: OperationActionStop,
		ExecutorPID: pid, ExecutorPIDStartTime: start, // genuinely alive: us
		AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st)

	claim, _ := LoadRuntimeState("g1", dir)
	rec, err := RecoverInterruptedClaim("g1", dir, "inst-recover", claim, false, func(string) bool { return false }, time.Now().UTC())
	if err != nil || rec != nil {
		t.Fatalf("a live bounded attempt is never normalized: %+v %v", rec, err)
	}
	after, _ := LoadRuntimeState("g1", dir)
	if after.Operation == nil || after.Phase != PhaseStopping {
		t.Fatalf("the live attempt must be untouched: %+v", after)
	}
}

func TestRecoveryNeverReplaysActionHooks(t *testing.T) {
	dir := t.TempDir()
	actionCalls := 0
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, &actionCalls), nil, nil, nil)
	installProbeFake(t)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{
		Status: probeHookLifecycle(StatusRunning).Status,
		Stop:   stopHook(),
	})
	st.Phase = PhaseStopping
	st.Operation = deadExecutorOp(t, OperationActionStop, time.Now().UTC().Add(time.Minute))
	publishClaim(t, dir, st)

	rec := recoverNow(t, "g1", dir, false)
	if rec == nil || rec.Removed {
		t.Fatalf("expected normalization: %+v", rec)
	}
	if actionCalls != 0 {
		t.Fatalf("recovery is liveness-driven and never replays stop/kill hooks; ran %d times", actionCalls)
	}
}

func TestGateStartPreflightDeadOwnerProceedsWithoutRepair(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()

	// Killed between claim creation and endpoint allocation: spawnState
	// preflight proves process creation was never attempted, so the next
	// start clears it safely — no liveness puzzle, no repair — even though
	// the pinned hook would answer running if consulted (the fresh claim's
	// own Stage 2 probes are the external-instance backstop).
	spec := m2Spec("g1")
	spec.Lifecycle = probeHookLifecycle(StatusRunning)
	st := NewRuntimeState(spec, RuntimeStateStatusStarting)
	st.Operation = deadExecutorOp(t, OperationActionStart, time.Now().UTC().Add(time.Minute))
	publishClaim(t, dir, st)

	res, err := GateStart(gateFor("g1", dir))
	if err != nil || res.Refusal != nil || res.Claim == nil {
		t.Fatalf("preflight + dead owner must clear and proceed: %+v %v", res, err)
	}
	if res.Claim.LaunchID == st.LaunchID {
		t.Fatal("the new claim must carry a fresh launch identity")
	}
}

func TestStopAdmissionProceedsOverDeadExecutorInWindow(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	st.Phase = PhaseStopping
	st.Operation = deadExecutorOp(t, OperationActionStop, time.Now().UTC().Add(time.Minute))
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("a provably dead executor must not block a retry within the window: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminated {
		t.Fatalf("expected the retried stop to complete: %+v", outcome)
	}
}
