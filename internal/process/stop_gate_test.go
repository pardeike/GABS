package process

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// swapStopActionFuncs injects the stop pipeline's action/probe/builtin
// primitives and shrinks the poll interval so verification windows are fast.
func swapStopActionFuncs(t *testing.T,
	action func(*launch.ResolvedHook, string, string) (bool, HookResult),
	probe func(*launch.ResolvedHook, string, string, time.Duration) (string, HookResult),
	term func(string, int) error,
	kill func(string, int) error) {
	t.Helper()
	prevAction, prevProbe := runActionHookFunc, runStatusProbeFunc
	prevTerm, prevKill, prevPoll := builtinTerminateFunc, builtinKillFunc, stopVerifyPollInterval
	if action != nil {
		runActionHookFunc = action
	}
	if probe != nil {
		runStatusProbeFunc = probe
	}
	if term != nil {
		builtinTerminateFunc = term
	}
	if kill != nil {
		builtinKillFunc = kill
	}
	stopVerifyPollInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		runActionHookFunc, runStatusProbeFunc = prevAction, prevProbe
		builtinTerminateFunc, builtinKillFunc, stopVerifyPollInterval = prevTerm, prevKill, prevPoll
	})
}

func actionHookReturning(ok bool, hr HookResult, calls *int) func(*launch.ResolvedHook, string, string) (bool, HookResult) {
	return func(*launch.ResolvedHook, string, string) (bool, HookResult) {
		if calls != nil {
			*calls++
		}
		return ok, hr
	}
}

func probeReturning(verdict string) func(*launch.ResolvedHook, string, string, time.Duration) (string, HookResult) {
	return func(*launch.ResolvedHook, string, string, time.Duration) (string, HookResult) {
		return verdict, HookResult{ExitCode: 0}
	}
}

func stopHook() *launch.ResolvedHook {
	return &launch.ResolvedHook{Command: "/hooks/stop", TimeoutSeconds: 5, VerifyTimeoutSeconds: 1}
}

func killHook() *launch.ResolvedHook {
	return &launch.ResolvedHook{Command: "/hooks/kill", TimeoutSeconds: 5, VerifyTimeoutSeconds: 1}
}

func verifyStatusHook() *launch.ResolvedHook {
	return &launch.ResolvedHook{Command: "/hooks/status", TimeoutSeconds: 1,
		RunningExitCodes: []int{0}, StoppedExitCodes: []int{1}}
}

// activeStopClaim publishes an active claim for gameID with the given
// lifecycle; callers add PID/name evidence per scenario.
func activeStopClaim(t *testing.T, gameID, dir string, lc *launch.ResolvedLifecycle) RuntimeState {
	t.Helper()
	st := NewRuntimeState(m2Spec(gameID), RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.SpawnState = SpawnStateSpawned
	st.Lifecycle = lc
	return st
}

func publishClaim(t *testing.T, dir string, st RuntimeState) {
	t.Helper()
	if err := ClaimRuntimeState(st.GameID, dir, st); err != nil {
		t.Fatalf("failed to publish claim: %v", err)
	}
}

func stopReq(gameID, dir string) StopRequest {
	return StopRequest{GameID: gameID, ConfigDir: dir, InstanceID: "inst-stop", Action: OperationActionStop}
}

func TestStopRefusesWhileOperationInFlight(t *testing.T) {
	dir := t.TempDir()
	hookCalls := 0
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{}, &hookCalls), nil, nil, nil)

	pid, start := ownFingerprint(t)
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	st.Phase = PhaseStopping
	st.Operation = &RuntimeOperation{
		OperationID: NewFencingID(), Action: OperationActionStop,
		ExecutorPID: pid, ExecutorPIDStartTime: start,
		AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || outcome != nil {
		t.Fatalf("in-flight operation must refuse, got outcome=%+v err=%v", outcome, err)
	}
	if refusal == nil || refusal.Code != RefusalOperationInFlight {
		t.Fatalf("expected operation_in_progress, got %+v", refusal)
	}
	if refusal.Phase != PhaseStopping || refusal.Operation == nil || refusal.Operation.Deadline.IsZero() {
		t.Fatalf("refusal must render phase and the attempt's timing: %+v", refusal)
	}
	if hookCalls != 0 {
		t.Fatalf("the hook must not run twice concurrently; ran %d times", hookCalls)
	}

	// Kill during stopping is refused the same way and never runs the hook.
	killReq := stopReq("g1", dir)
	killReq.Action = OperationActionKill
	outcome, refusal, err = ExecuteStopAction(killReq)
	if err != nil || outcome != nil || refusal == nil || refusal.Code != RefusalOperationInFlight {
		t.Fatalf("kill during stopping must be operation_in_progress: %+v %+v %v", outcome, refusal, err)
	}
	if hookCalls != 0 {
		t.Fatalf("kill during stopping must not run any hook; ran %d times", hookCalls)
	}
}

func TestKillProceedsAfterOperationResolves(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probeReturning(StatusStopped), nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Kill: killHook()})
	publishClaim(t, dir, st)

	killReq := stopReq("g1", dir)
	killReq.Action = OperationActionKill
	outcome, refusal, err := ExecuteStopAction(killReq)
	if err != nil || refusal != nil {
		t.Fatalf("kill after resolution must proceed: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminated || !outcome.ClaimRemoved {
		t.Fatalf("expected terminated with claim removed, got %+v", outcome)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim != nil {
		t.Fatalf("terminated must clear the claim, still present: %+v", claim)
	}
}

func TestStopProceedsOverExpiredOperation(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), nil, nil, nil)

	pid, start := ownFingerprint(t)
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	st.Phase = PhaseStopping
	st.Operation = &RuntimeOperation{
		OperationID: NewFencingID(), Action: OperationActionStop,
		ExecutorPID: pid, ExecutorPIDStartTime: start,
		AttemptStartedAt: time.Now().UTC().Add(-2 * time.Minute),
		Deadline:         time.Now().UTC().Add(-time.Minute),
	}
	publishClaim(t, dir, st)

	// No status hook, no PID, no name, no bridge: hook success stands alone
	// and clears the claim (design/06 row 4).
	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("expired operation must not block a fresh stop: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminated || !outcome.ClaimRemoved {
		t.Fatalf("expected terminated, got %+v", outcome)
	}
}

func TestStopUnsupportedForKillOnlyConfig(t *testing.T) {
	dir := t.TempDir()
	hookCalls := 0
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, &hookCalls), probeReturning(StatusStopped), nil, nil)

	// A valid kill-only URL configuration: status + kill hooks, helper-role
	// PID, no stopProcessName (design/06).
	spec := m2Spec("g1")
	spec.Mode = "SteamAppId"
	st := NewRuntimeState(spec, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.SpawnState = SpawnStateSpawned
	st.Lifecycle = &launch.ResolvedLifecycle{Status: verifyStatusHook(), Kill: killHook()}
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || outcome != nil {
		t.Fatalf("stop without any graceful mechanism must refuse: %+v %v", outcome, err)
	}
	if refusal == nil || refusal.Code != RefusalStopUnsupported {
		t.Fatalf("expected stop_unsupported, got %+v", refusal)
	}
	if !refusal.KillCapable {
		t.Fatalf("refusal must point at games_kill when a kill action exists: %+v", refusal)
	}
	if hookCalls != 0 {
		t.Fatalf("stop must never silently escalate to the kill hook; ran %d times", hookCalls)
	}

	killReq := stopReq("g1", dir)
	killReq.Action = OperationActionKill
	outcome, refusal, err = ExecuteStopAction(killReq)
	if err != nil || refusal != nil || outcome.Code != OutcomeTerminated {
		t.Fatalf("kill must work on the kill-only config: %+v %+v %v", outcome, refusal, err)
	}
}

func TestKillUnsupportedNeverFallsBackToStopHook(t *testing.T) {
	dir := t.TempDir()
	hookCalls := 0
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, &hookCalls), nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	st.GamePID = 0 // no workload PID, no name: no force-capable action
	publishClaim(t, dir, st)

	killReq := stopReq("g1", dir)
	killReq.Action = OperationActionKill
	outcome, refusal, err := ExecuteStopAction(killReq)
	if err != nil || outcome != nil {
		t.Fatalf("kill without a force-capable action must refuse: %+v %v", outcome, err)
	}
	if refusal == nil || refusal.Code != RefusalKillUnsupported {
		t.Fatalf("expected kill_unsupported, got %+v", refusal)
	}
	if hookCalls != 0 {
		t.Fatalf("kill must never fall back to the stop hook; ran %d times", hookCalls)
	}
}

func TestStopHookFailurePersistsLastActionResult(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(false, HookResult{ExitCode: 3, StderrTail: "save in progress"}, nil), nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("hook failure is an outcome, not a refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeActionFailed || outcome.ClaimRemoved {
		t.Fatalf("expected action_failed with claim kept, got %+v", outcome)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.Phase != PhaseActive || claim.Operation != nil {
		t.Fatalf("failure must restore the phase and clear the operation: %+v", claim)
	}
	lar := claim.LastActionResult
	if lar == nil || lar.Action != OperationActionStop || lar.Outcome != OutcomeActionFailed {
		t.Fatalf("lastActionResult must persist the failed attempt: %+v", lar)
	}
	if lar.ExitCode == nil || *lar.ExitCode != 3 || lar.StderrTail != "save in progress" {
		t.Fatalf("lastActionResult must carry exit code and stderr tail: %+v", lar)
	}
	if lar.Timestamp.IsZero() {
		t.Fatalf("lastActionResult must be timestamped: %+v", lar)
	}
}

func TestStopHookTimeoutPersistsTreeKillWarning(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(false, HookResult{ExitCode: -1, TimedOut: true, TreeKillWarning: true}, nil), nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, _, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || outcome == nil || outcome.Code != OutcomeActionTimedOut {
		t.Fatalf("expected action_timed_out, got %+v %v", outcome, err)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.LastActionResult == nil || !claim.LastActionResult.TreeKillWarning {
		t.Fatalf("tree-kill warning must persist: %+v", claim)
	}
	if claim.LastActionResult.Outcome != OutcomeActionTimedOut {
		t.Fatalf("outcome must be action_timed_out: %+v", claim.LastActionResult)
	}
}

func TestVerificationRunningKeepsClaimAsSucceededRunning(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probeReturning(StatusRunning), nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeActionSucceededRunning || outcome.ClaimRemoved {
		t.Fatalf("running evidence at the window must be action_succeeded_running: %+v", outcome)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.Phase != PhaseActive || claim.Operation != nil {
		t.Fatalf("claim must stay active with the operation cleared: %+v", claim)
	}
	if claim.LastActionResult == nil || claim.LastActionResult.Outcome != OutcomeActionSucceededRunning {
		t.Fatalf("lastActionResult must record the outcome: %+v", claim.LastActionResult)
	}
}

func TestVerificationAllStoppedTerminates(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probeReturning(StatusStopped), nil, nil)
	swapLivenessProbes(t, nil, nil, func(string) ([]int, error) { return nil, nil })

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Stop: stopHook()})
	st.StopProcessName = "game-workload"
	publishClaim(t, dir, st)

	reaped := false
	req := stopReq("g1", dir)
	req.ReapLauncher = func() { reaped = true }

	startAt := time.Now()
	outcome, refusal, err := ExecuteStopAction(req)
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminated || !outcome.ClaimRemoved {
		t.Fatalf("all-stopped must terminate and clear the claim: %+v", outcome)
	}
	if !reaped {
		t.Fatal("a stopped verdict must reap the launcher child before clearing the claim")
	}
	if elapsed := time.Since(startAt); elapsed > 800*time.Millisecond {
		t.Fatalf("all-stopped should exit the window early, took %v", elapsed)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim != nil {
		t.Fatalf("claim must be removed: %+v", claim)
	}
}

func TestVerificationUnknownKeepsClaimUnverified(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probeReturning(StatusUnknown), nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminationUnverified || outcome.ClaimRemoved {
		t.Fatalf("unknown never cleans state, even after a successful action: %+v", outcome)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.Phase != PhaseActive || claim.Operation != nil {
		t.Fatalf("claim must be kept with phase restored: %+v", claim)
	}
	if claim.LastActionResult == nil || claim.LastActionResult.Outcome != OutcomeTerminationUnverified {
		t.Fatalf("lastActionResult must record the unverified outcome: %+v", claim.LastActionResult)
	}
}

func TestVerificationNoSourcesHookSuccessClears(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), nil, nil, nil)

	// No status hook, no workload PID, no stopProcessName, bridge never
	// attached: the stop-only-wrapper case (design/06 row 4).
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminated || !outcome.ClaimRemoved {
		t.Fatalf("hook success alone must clear the stop-only-wrapper claim: %+v", outcome)
	}
}

func TestStopOnlyWrapperWithLiveGABPStaysUnverified(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	publishClaim(t, dir, st)

	req := stopReq("g1", dir)
	req.GABPLive = func() bool { return true }
	outcome, refusal, err := ExecuteStopAction(req)
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminationUnverified || outcome.ClaimRemoved {
		t.Fatalf("a still-live bridge must keep the claim as unverified: %+v", outcome)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim == nil {
		t.Fatal("claim must survive under a live bridge")
	}
}

func TestCrossProcessAttachmentLeaseKeepsClaimUnverified(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), nil, nil, nil)

	pid, start := ownFingerprint(t)
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	st.Attachment = &RuntimeAttachment{
		ConnectionID: NewFencingID(), OwnerInstanceID: "other-server",
		OwnerPID: pid, OwnerPIDStartTime: start,
		ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminationUnverified || outcome.ClaimRemoved {
		t.Fatalf("a fresh foreign attachment lease must keep the claim: %+v", outcome)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim == nil {
		t.Fatal("claim must not be cleared under another process's live bridge")
	}
}

func TestDeadOwnerAttachmentIsNotEvidence(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), nil, nil, nil)

	pid, start := ownFingerprint(t)
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	st.Attachment = &RuntimeAttachment{
		ConnectionID: NewFencingID(), OwnerInstanceID: "other-server",
		OwnerPID: pid, OwnerPIDStartTime: start + 12345, // fingerprint mismatch: owner died, PID reused
		ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st)

	outcome, _, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || outcome == nil || outcome.Code != OutcomeTerminated {
		t.Fatalf("a dead owner's attachment record is history, not evidence: %+v %v", outcome, err)
	}
}

func TestSelfOwnedAttachmentDefersToLiveConnectionState(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), nil, nil, nil)

	pid, start := ownFingerprint(t)
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	st.Attachment = &RuntimeAttachment{
		ConnectionID: NewFencingID(), OwnerInstanceID: "inst-stop", // == requester
		OwnerPID: pid, OwnerPIDStartTime: start,
		ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st)

	// The requester owns the record and reports its bridge disconnected: the
	// record must not outvote the live in-process connection state.
	req := stopReq("g1", dir)
	req.GABPLive = func() bool { return false }
	outcome, _, err := ExecuteStopAction(req)
	if err != nil || outcome == nil || outcome.Code != OutcomeTerminated {
		t.Fatalf("a self-owned lease with a dead connection must not block clearing: %+v %v", outcome, err)
	}
}

func TestVerificationSeesAttachmentAppearingMidAction(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)
	action := func(*launch.ResolvedHook, string, string) (bool, HookResult) {
		// While the stop hook runs (lock not held), another GABS process
		// attaches its bridge and persists a fresh lease.
		if _, err := TransitionRuntimeState("g1", dir, time.Second, func(s *RuntimeState) error {
			s.Attachment = &RuntimeAttachment{
				ConnectionID: NewFencingID(), OwnerInstanceID: "other-server",
				OwnerPID: pid, OwnerPIDStartTime: start,
				ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
			}
			return nil
		}); err != nil {
			t.Errorf("mid-hook attachment write failed: %v", err)
		}
		return true, HookResult{ExitCode: 0}
	}
	swapStopActionFuncs(t, action, nil, nil, nil)

	// Stop-only wrapper: without the mid-action attachment this would be
	// the no-sources row and clear the claim.
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminationUnverified || outcome.ClaimRemoved {
		t.Fatalf("a bridge attached during the action must keep the claim: %+v", outcome)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.Attachment == nil {
		t.Fatalf("the claim and its live foreign attachment must survive: %+v", claim)
	}
}

func TestStopRemovalRefusedUnderFreshForeignAttachment(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)
	st := activeStopClaim(t, "g1", dir, nil)
	opID := NewFencingID()
	st.Operation = &RuntimeOperation{OperationID: opID, Action: OperationActionStop, AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute)}
	st.Attachment = &RuntimeAttachment{
		ConnectionID: NewFencingID(), OwnerInstanceID: "other-server",
		OwnerPID: pid, OwnerPIDStartTime: start,
		ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st)

	err := removeRuntimeStateForStopCompletion(stopReq("g1", dir), st.LaunchID, opID)
	if !errors.Is(err, errStopAttachmentLive) {
		t.Fatalf("removal must refuse under a live foreign attachment, got %v", err)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim == nil {
		t.Fatal("the claim must survive the refused removal")
	}
}

func TestStopContradictionLiveGABPvsStoppedHook(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probeReturning(StatusStopped), nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Stop: stopHook()})
	publishClaim(t, dir, st)

	req := stopReq("g1", dir)
	req.GABPLive = func() bool { return true }
	outcome, refusal, err := ExecuteStopAction(req)
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	// design/04: hook says stopped while GABP is live → running, with a
	// warning about the hook — never unverified limbo, never cleanup.
	if outcome.Code != OutcomeActionSucceededRunning || outcome.ClaimRemoved {
		t.Fatalf("live bridge vs stopped hook must stay running: %+v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Warnings, "\n"), "exit-code contract") {
		t.Fatalf("the contradiction must be warned about: %v", outcome.Warnings)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim == nil || claim.Phase != PhaseActive {
		t.Fatalf("the claim must stay active: %+v", claim)
	}
}

func TestVerificationStopsPollingWhenClaimSuperseded(t *testing.T) {
	dir := t.TempDir()
	var replacement RuntimeState
	action := func(*launch.ResolvedHook, string, string) (bool, HookResult) {
		if err := RemoveRuntimeState("g1", dir); err != nil {
			t.Errorf("mid-hook removal failed: %v", err)
		}
		replacement = NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
		if err := ClaimRuntimeState("g1", dir, replacement); err != nil {
			t.Errorf("mid-hook reclaim failed: %v", err)
		}
		return true, HookResult{ExitCode: 0} // hook SUCCEEDS: verification runs
	}
	swapStopActionFuncs(t, action, nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeTerminationUnverified {
		t.Fatalf("a superseded verification must not claim a verdict about the successor: %+v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Warnings, "\n"), "removed or replaced") {
		t.Fatalf("supersession must be surfaced: %v", outcome.Warnings)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.LaunchID != replacement.LaunchID {
		t.Fatalf("the successor claim must survive: %+v", claim)
	}
	if claim.LastActionResult != nil || claim.Phase != PhaseStarting || claim.Operation != nil {
		t.Fatalf("the successor must be untouched by the finished stop: %+v", claim)
	}
}

func TestVerificationProbeClippedToRemainingWindow(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var waits []time.Duration
	probe := func(h *launch.ResolvedHook, gameID, profile string, maxWait time.Duration) (string, HookResult) {
		mu.Lock()
		waits = append(waits, maxWait)
		mu.Unlock()
		return StatusUnknown, HookResult{TimedOut: true, ExitCode: -1}
	}
	swapStopActionFuncs(t, actionHookReturning(true, HookResult{ExitCode: 0}, nil), probe, nil, nil)

	slowStatus := verifyStatusHook()
	slowStatus.TimeoutSeconds = 60 // a hanging 60s hook must not push past the deadline
	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: slowStatus, Stop: stopHook()})
	publishClaim(t, dir, st)

	startAt := time.Now()
	outcome, _, err := ExecuteStopAction(stopReq("g1", dir))
	elapsed := time.Since(startAt)
	if err != nil || outcome == nil || outcome.Code != OutcomeTerminationUnverified {
		t.Fatalf("clipped unknown probes end as termination_unverified: %+v %v", outcome, err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("verification must honor the 1s window, took %v", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) == 0 {
		t.Fatal("the status probe must run during verification")
	}
	for _, w := range waits {
		if w > 1100*time.Millisecond {
			t.Fatalf("every probe must be clipped to the remaining window, got %v", w)
		}
	}
}

func TestBuiltinStopSignalsPinnedWorkloadPID(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)

	var signaled []int
	var signalStrategy string
	gone := false
	term := func(strategy string, target int) error {
		signaled = append(signaled, target)
		signalStrategy = strategy
		gone = true
		return nil
	}
	swapStopActionFuncs(t, nil, nil, term, nil)
	swapLivenessProbes(t, nil, func(p int) (int64, error) {
		if p == pid && gone {
			return 0, ErrProcessNotFound
		}
		return start, nil
	}, nil)

	st := activeStopClaim(t, "g1", dir, nil)
	st.GamePID = pid
	st.PIDStartTime = start
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("builtin stop must proceed: %+v %v", refusal, err)
	}
	if len(signaled) != 1 || signaled[0] != pid {
		t.Fatalf("the pinned workload PID must be signaled, got %v", signaled)
	}
	if signalStrategy == "" {
		t.Fatal("the pinned graceful strategy must reach the signal executor")
	}
	if outcome.Code != OutcomeTerminated || !outcome.ClaimRemoved {
		t.Fatalf("workload proven gone must terminate: %+v", outcome)
	}
}

func TestBuiltinStopScanErrorFails(t *testing.T) {
	dir := t.TempDir()
	swapStopActionFuncs(t, nil, nil, nil, nil)
	swapLivenessProbes(t, nil, nil, func(string) ([]int, error) {
		return nil, &scanError{}
	})

	st := activeStopClaim(t, "g1", dir, nil)
	st.GamePID = 0
	st.StopProcessName = "game-workload"
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("scan failure is an outcome: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeActionFailed {
		t.Fatalf("an unenumerable process table must fail the action: %+v", outcome)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.LastActionResult == nil || claim.LastActionResult.Outcome != OutcomeActionFailed {
		t.Fatalf("the failure must persist: %+v", claim)
	}
	if !strings.Contains(claim.LastActionResult.Detail, "game-workload") {
		t.Fatalf("the failure detail must name the scan target: %+v", claim.LastActionResult)
	}
}

type scanError struct{}

func (*scanError) Error() string { return "process table unreadable" }

func TestBuiltinNameCollisionSignalsAllWithWarning(t *testing.T) {
	dir := t.TempDir()
	var signaled []int
	found := []int{11111, 22222}
	term := func(strategy string, target int) error {
		signaled = append(signaled, target)
		return nil
	}
	swapStopActionFuncs(t, nil, nil, term, nil)
	swapLivenessProbes(t, nil, nil, func(string) ([]int, error) {
		if len(signaled) == 2 {
			return nil, nil // both gone after the signals
		}
		return found, nil
	})

	st := activeStopClaim(t, "g1", dir, nil)
	st.GamePID = 0
	st.StopProcessName = "game-workload"
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if len(signaled) != 2 {
		t.Fatalf("all name matches must be signaled, got %v", signaled)
	}
	joined := strings.Join(outcome.Warnings, "\n")
	if !strings.Contains(joined, "2 ") {
		t.Fatalf("a name collision must be warned about: %v", outcome.Warnings)
	}
	if outcome.Code != OutcomeTerminated {
		t.Fatalf("expected terminated after both processes vanish: %+v", outcome)
	}
}

func TestFailedStopCompletionCannotResurrectReplacedClaim(t *testing.T) {
	dir := t.TempDir()
	var replacement RuntimeState
	action := func(*launch.ResolvedHook, string, string) (bool, HookResult) {
		// While the hook runs (lock not held), a kill in another process
		// removes the claim and a new start claims fresh.
		if err := RemoveRuntimeState("g1", dir); err != nil {
			t.Errorf("mid-hook removal failed: %v", err)
		}
		replacement = NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
		if err := ClaimRuntimeState("g1", dir, replacement); err != nil {
			t.Errorf("mid-hook reclaim failed: %v", err)
		}
		return false, HookResult{ExitCode: 7, StderrTail: "boom"}
	}
	swapStopActionFuncs(t, action, nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("unexpected refusal: %+v %v", refusal, err)
	}
	if outcome.Code != OutcomeActionFailed {
		t.Fatalf("the caller still learns the hook failed: %+v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Warnings, "\n"), "superseded") {
		t.Fatalf("the discarded completion must be surfaced: %v", outcome.Warnings)
	}
	claim, _ := LoadRuntimeState("g1", dir)
	if claim == nil || claim.LaunchID != replacement.LaunchID {
		t.Fatalf("the replacement claim must survive untouched: %+v", claim)
	}
	if claim.LastActionResult != nil || claim.Phase != PhaseStarting {
		t.Fatalf("a failed stop must not write into the successor claim: %+v", claim)
	}
}

func TestExternalSnapshotStopRunsHookWithObservedProfile(t *testing.T) {
	dir := t.TempDir()
	var hookProfile string
	action := func(h *launch.ResolvedHook, gameID, profile string) (bool, HookResult) {
		hookProfile = profile
		return true, HookResult{ExitCode: 0}
	}
	swapStopActionFuncs(t, action, probeReturning(StatusStopped), nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Status: verifyStatusHook(), Stop: stopHook()})
	st.Source = SourceExternal
	st.Profile = ""
	st.ObservedProfile = "modded"
	st.AppliedInputsState = AppliedInputsStateUnavailable
	publishClaim(t, dir, st)

	outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
	if err != nil || refusal != nil {
		t.Fatalf("external snapshots support stop: %+v %v", refusal, err)
	}
	if hookProfile != "modded" {
		t.Fatalf("the pinned hook must run with the observed profile, got %q", hookProfile)
	}
	if outcome.Code != OutcomeTerminated {
		t.Fatalf("expected terminated: %+v", outcome)
	}
}

func TestConcurrentStopsRunExactlyOneAction(t *testing.T) {
	dir := t.TempDir()
	var hookCalls int32
	action := func(*launch.ResolvedHook, string, string) (bool, HookResult) {
		atomic.AddInt32(&hookCalls, 1)
		time.Sleep(150 * time.Millisecond) // hold the operation window open
		return true, HookResult{ExitCode: 0}
	}
	swapStopActionFuncs(t, action, nil, nil, nil)

	st := activeStopClaim(t, "g1", dir, &launch.ResolvedLifecycle{Stop: stopHook()})
	publishClaim(t, dir, st)

	const contenders = 4
	var wg sync.WaitGroup
	var terminated, refused int32
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome, refusal, err := ExecuteStopAction(stopReq("g1", dir))
			switch {
			case outcome != nil && outcome.Code == OutcomeTerminated:
				atomic.AddInt32(&terminated, 1)
			case refusal != nil && refusal.Code == RefusalOperationInFlight:
				atomic.AddInt32(&refused, 1)
			case err != nil && errors.Is(err, ErrNoRuntimeClaim):
				// arrived after the winner removed the claim
			default:
				t.Errorf("unexpected result: %+v %+v %v", outcome, refusal, err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&hookCalls); got != 1 {
		t.Fatalf("exactly one contender may run the hook, ran %d times", got)
	}
	if got := atomic.LoadInt32(&terminated); got != 1 {
		t.Fatalf("exactly one terminated outcome expected, got %d", got)
	}
}

func TestReleaseStartClaimIsFenced(t *testing.T) {
	dir := t.TempDir()

	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
	opID := NewFencingID()
	st.Operation = &RuntimeOperation{OperationID: opID, Action: OperationActionStart, AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute)}
	publishClaim(t, dir, st)

	// Wrong launch identity: the successor claim survives.
	if err := ReleaseStartClaim("g1", dir, "inst", NewFencingID(), opID); !errors.Is(err, ErrFencingViolation) {
		t.Fatalf("a foreign launch must never be released, got %v", err)
	}
	// A different operation on our launch: someone admitted work — leave it.
	if err := ReleaseStartClaim("g1", dir, "inst", st.LaunchID, NewFencingID()); !errors.Is(err, ErrFencingViolation) {
		t.Fatalf("a different operation must fence the release, got %v", err)
	}
	// Our own operation: released.
	if err := ReleaseStartClaim("g1", dir, "inst", st.LaunchID, opID); err != nil {
		t.Fatalf("our own claim must release: %v", err)
	}
	if claim, _ := LoadRuntimeState("g1", dir); claim != nil {
		t.Fatalf("claim must be gone: %+v", claim)
	}

	// Operation-free shape (promote-to-active ran before the exit was
	// discovered): still ours, still released.
	st2 := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st2.Phase = PhaseActive
	publishClaim(t, dir, st2)
	if err := ReleaseStartClaim("g1", dir, "inst", st2.LaunchID, NewFencingID()); err != nil {
		t.Fatalf("an operation-free own claim must release: %v", err)
	}

	// A live foreign attachment forbids the release.
	pid, start := ownFingerprint(t)
	st3 := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st3.Attachment = &RuntimeAttachment{
		ConnectionID: NewFencingID(), OwnerInstanceID: "other-server",
		OwnerPID: pid, OwnerPIDStartTime: start,
		ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st3)
	if err := ReleaseStartClaim("g1", dir, "inst", st3.LaunchID, NewFencingID()); !errors.Is(err, ErrFencingViolation) {
		t.Fatalf("a claim under a live foreign bridge must not be released, got %v", err)
	}
}

func TestRemoveRuntimeStateIfCurrentRefusesOperationsAndAttachments(t *testing.T) {
	dir := t.TempDir()
	pid, start := ownFingerprint(t)

	// Any operation — expired or not — refuses the status-path removal.
	st := activeStopClaim(t, "g1", dir, nil)
	st.Operation = &RuntimeOperation{OperationID: NewFencingID(), Action: OperationActionStop, AttemptStartedAt: time.Now().UTC().Add(-time.Hour), Deadline: time.Now().UTC().Add(-time.Minute)}
	publishClaim(t, dir, st)
	if err := RemoveRuntimeStateIfCurrent("g1", dir, "inst", st.LaunchID); !errors.Is(err, ErrFencingViolation) {
		t.Fatalf("an admitted operation must refuse the status removal even when expired, got %v", err)
	}
	if err := RemoveRuntimeState("g1", dir); err != nil {
		t.Fatal(err)
	}

	// A fresh live foreign attachment refuses it too.
	st2 := activeStopClaim(t, "g1", dir, nil)
	st2.Attachment = &RuntimeAttachment{
		ConnectionID: NewFencingID(), OwnerInstanceID: "other-server",
		OwnerPID: pid, OwnerPIDStartTime: start,
		ObservedAt: time.Now().UTC(), LeaseDeadline: time.Now().UTC().Add(time.Minute),
	}
	publishClaim(t, dir, st2)
	if err := RemoveRuntimeStateIfCurrent("g1", dir, "inst", st2.LaunchID); !errors.Is(err, ErrFencingViolation) {
		t.Fatalf("a live foreign attachment must refuse the status removal, got %v", err)
	}
	if err := RemoveRuntimeState("g1", dir); err != nil {
		t.Fatal(err)
	}

	// Clean, operation-free, unattached: removed.
	st3 := activeStopClaim(t, "g1", dir, nil)
	publishClaim(t, dir, st3)
	if err := RemoveRuntimeStateIfCurrent("g1", dir, "inst", st3.LaunchID); err != nil {
		t.Fatalf("a clean stopped claim must remove: %v", err)
	}
}

func TestBuiltinFallbackStrategyPinnedAtClaimCreation(t *testing.T) {
	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
	if st.BuiltinFallback == nil {
		t.Fatal("claims must pin the built-in graceful/force strategy (design/07)")
	}
	if st.BuiltinFallback.GracefulStrategy == "" || st.BuiltinFallback.ForceStrategy == "" {
		t.Fatalf("both strategies must be pinned: %+v", st.BuiltinFallback)
	}

	dir := t.TempDir()
	publishClaim(t, dir, st)
	loaded, err := LoadRuntimeState("g1", dir)
	if err != nil || loaded == nil || loaded.BuiltinFallback == nil {
		t.Fatalf("the pin must round-trip: %+v %v", loaded, err)
	}
	if *loaded.BuiltinFallback != *st.BuiltinFallback {
		t.Fatalf("pin changed across the round-trip: %+v vs %+v", loaded.BuiltinFallback, st.BuiltinFallback)
	}
}
