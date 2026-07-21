package process

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// swapLivenessProbes injects deterministic stand-ins for the three evidence
// probes and restores them on cleanup.
func swapLivenessProbes(t *testing.T, hook func(*launch.ResolvedHook, string, string) (string, HookResult),
	startTime func(int) (int64, error), byName func(string) ([]int, error)) {
	t.Helper()
	prevHook, prevStart, prevName := runStatusHookFunc, processStartTimeFunc, findProcessesByNameFunc
	if hook != nil {
		runStatusHookFunc = hook
	}
	if startTime != nil {
		processStartTimeFunc = startTime
	}
	if byName != nil {
		findProcessesByNameFunc = byName
	}
	t.Cleanup(func() {
		runStatusHookFunc, processStartTimeFunc, findProcessesByNameFunc = prevHook, prevStart, prevName
	})
}

func hookReturning(verdict string, calls *int) func(*launch.ResolvedHook, string, string) (string, HookResult) {
	return func(*launch.ResolvedHook, string, string) (string, HookResult) {
		if calls != nil {
			*calls++
		}
		return verdict, HookResult{ExitCode: 1}
	}
}

func ownFingerprint(t *testing.T) (int, int64) {
	t.Helper()
	pid := os.Getpid()
	start, err := ProcessStartTime(pid)
	if err != nil {
		t.Fatalf("own start time: %v", err)
	}
	return pid, start
}

func TestLivenessGABPWinsWithoutRunningHook(t *testing.T) {
	calls := 0
	swapLivenessProbes(t, hookReturning(StatusStopped, &calls), nil, nil)
	ev := EvaluateLiveness(LivenessInput{
		GABPLive:   true,
		StatusHook: &launch.ResolvedHook{Command: "x"},
	})
	if ev.Verdict != StatusRunning || ev.Source != LivenessSourceGABP {
		t.Fatalf("live GABP must win: %+v", ev)
	}
	if calls != 0 {
		t.Fatalf("hook must not run on the cheap path")
	}
}

func TestLivenessGABPHookContradictionWarning(t *testing.T) {
	swapLivenessProbes(t, hookReturning(StatusStopped, nil), nil, nil)
	ev := EvaluateLiveness(LivenessInput{
		GABPLive:     true,
		StatusHook:   &launch.ResolvedHook{Command: "x"},
		DiagnoseHook: true,
	})
	if ev.Verdict != StatusRunning {
		t.Fatalf("GABP wins the contradiction: %+v", ev)
	}
	if len(ev.Warnings) == 0 || ev.HookResult == nil {
		t.Fatalf("contradiction must be reported, not resolved silently: %+v", ev)
	}
}

func TestLivenessAttachmentLeaseFresh(t *testing.T) {
	pid, start := ownFingerprint(t)
	claim := &RuntimeState{Attachment: &RuntimeAttachment{
		ConnectionID: "c1", OwnerPID: pid, OwnerPIDStartTime: start,
		LeaseDeadline: time.Now().Add(time.Minute),
	}}
	ev := EvaluateLiveness(LivenessInput{Claim: claim})
	if ev.Verdict != StatusRunning || ev.Source != LivenessSourceAttachment {
		t.Fatalf("fresh lease + live fingerprint-matched owner = running: %+v", ev)
	}
}

func TestLivenessAttachmentLeaseExpired(t *testing.T) {
	pid, start := ownFingerprint(t)
	swapLivenessProbes(t, hookReturning(StatusStopped, nil), nil, nil)
	claim := &RuntimeState{Attachment: &RuntimeAttachment{
		ConnectionID: "c1", OwnerPID: pid, OwnerPIDStartTime: start,
		LeaseDeadline: time.Now().Add(-time.Second),
	}}
	ev := EvaluateLiveness(LivenessInput{Claim: claim, StatusHook: &launch.ResolvedHook{Command: "x"}})
	if ev.Verdict != StatusStopped || ev.Source != LivenessSourceStatusHook {
		t.Fatalf("expired lease is history, not evidence: %+v", ev)
	}
}

func TestLivenessAttachmentOwnerGone(t *testing.T) {
	swapLivenessProbes(t, hookReturning(StatusStopped, nil), nil, nil)
	claim := &RuntimeState{Attachment: &RuntimeAttachment{
		ConnectionID: "c1", OwnerPID: 99999999, OwnerPIDStartTime: 12345,
		LeaseDeadline: time.Now().Add(time.Minute),
	}}
	ev := EvaluateLiveness(LivenessInput{Claim: claim, StatusHook: &launch.ResolvedHook{Command: "x"}})
	if ev.Source != LivenessSourceStatusHook {
		t.Fatalf("a fresh lease with a dead owner is not running-evidence: %+v", ev)
	}
}

func TestLivenessStatusHookAuthoritative(t *testing.T) {
	for _, verdict := range []string{StatusRunning, StatusStopped, StatusUnknown} {
		swapLivenessProbes(t, hookReturning(verdict, nil), nil, nil)
		ev := EvaluateLiveness(LivenessInput{StatusHook: &launch.ResolvedHook{Command: "x"}})
		if ev.Verdict != verdict || ev.Source != LivenessSourceStatusHook {
			t.Fatalf("hook verdict %q must be authoritative: %+v", verdict, ev)
		}
	}
}

func TestLivenessPIDFingerprintMatch(t *testing.T) {
	pid, start := ownFingerprint(t)
	ev := EvaluateLiveness(LivenessInput{Claim: &RuntimeState{
		GamePID: pid, PIDStartTime: start, PIDRole: PIDRoleWorkload,
	}})
	if ev.Verdict != StatusRunning || ev.Source != LivenessSourcePID {
		t.Fatalf("matching fingerprint = running: %+v", ev)
	}
}

func TestLivenessPIDReuseIsStopped(t *testing.T) {
	pid, start := ownFingerprint(t)
	ev := EvaluateLiveness(LivenessInput{Claim: &RuntimeState{
		GamePID: pid, PIDStartTime: start + 1, PIDRole: PIDRoleWorkload,
	}})
	if ev.Verdict != StatusStopped {
		t.Fatalf("fingerprint mismatch means the recorded process is gone: %+v", ev)
	}
}

func TestLivenessInspectionFailureIsUnknown(t *testing.T) {
	swapLivenessProbes(t, nil, func(int) (int64, error) {
		return 0, errors.New("permission denied")
	}, nil)
	ev := EvaluateLiveness(LivenessInput{Claim: &RuntimeState{
		GamePID: 12345, PIDStartTime: 99, PIDRole: PIDRoleWorkload,
	}})
	if ev.Verdict != StatusUnknown {
		t.Fatalf("inspection failure is unknown, never stopped: %+v", ev)
	}
}

func TestLivenessInspectionFailureTaintsNameScanStopped(t *testing.T) {
	// PID inspection fails (process may exist) + name scan finds nothing:
	// the name scan must not downgrade the failure to stopped.
	swapLivenessProbes(t, nil,
		func(int) (int64, error) { return 0, errors.New("permission denied") },
		func(string) ([]int, error) { return nil, nil })
	ev := EvaluateLiveness(LivenessInput{Claim: &RuntimeState{
		GamePID: 12345, PIDStartTime: 99, PIDRole: PIDRoleWorkload,
		StopProcessName: "game-bin",
	}})
	if ev.Verdict != StatusUnknown {
		t.Fatalf("scan-none must not override an inspection failure: %+v", ev)
	}
}

func TestLivenessHelperPIDIsNotWorkloadEvidence(t *testing.T) {
	pid, start := ownFingerprint(t)
	// Helper (URL-opener) is alive, but that proves nothing about the workload.
	ev := EvaluateLiveness(LivenessInput{Claim: &RuntimeState{
		GamePID: pid, PIDStartTime: start, PIDRole: PIDRoleHelper,
	}})
	if ev.Verdict != StatusUnknown {
		t.Fatalf("a live helper PID must not prove the workload: %+v", ev)
	}
	// With stopProcessName, a clean empty scan is stopped-evidence.
	swapLivenessProbes(t, nil, nil, func(string) ([]int, error) { return nil, nil })
	ev = EvaluateLiveness(LivenessInput{Claim: &RuntimeState{
		GamePID: pid, PIDStartTime: start, PIDRole: PIDRoleHelper,
		StopProcessName: "game-bin",
	}})
	if ev.Verdict != StatusStopped || ev.Source != LivenessSourceProcessName {
		t.Fatalf("URL-mode workload proof comes from stopProcessName: %+v", ev)
	}
}

func TestLivenessStopProcessNameFallback(t *testing.T) {
	cases := []struct {
		pids    []int
		err     error
		verdict string
	}{
		{[]int{4242}, nil, StatusRunning},
		{nil, nil, StatusStopped},
		{nil, fmt.Errorf("tasklist failed"), StatusUnknown},
	}
	for _, c := range cases {
		swapLivenessProbes(t, nil, nil, func(string) ([]int, error) { return c.pids, c.err })
		// Launcher PID exited (fingerprint says stopped); name scan decides.
		ev := EvaluateLiveness(LivenessInput{Claim: &RuntimeState{
			GamePID: 99999999, PIDRole: PIDRoleWorkload, StopProcessName: "game-bin",
		}})
		if ev.Verdict != c.verdict {
			t.Fatalf("pids=%v err=%v: got %+v, want %q", c.pids, c.err, ev, c.verdict)
		}
	}
}

func TestLivenessNoClaimNoEvidence(t *testing.T) {
	ev := EvaluateLiveness(LivenessInput{})
	if ev.Verdict != StatusStopped || ev.Source != LivenessSourceNone {
		t.Fatalf("nothing to observe: %+v", ev)
	}
}

func TestLivenessNoClaimProbesStopProcessName(t *testing.T) {
	// A missing claim must not skip the lost-claim backstop: a configured
	// stopProcessName is still probed and an untracked instance detected.
	cases := []struct {
		pids    []int
		err     error
		verdict string
	}{
		{[]int{77}, nil, StatusRunning},
		{nil, nil, StatusStopped},
		{nil, errors.New("hidepid"), StatusUnknown},
	}
	for _, c := range cases {
		swapLivenessProbes(t, nil, nil, func(string) ([]int, error) { return c.pids, c.err })
		ev := EvaluateLiveness(LivenessInput{StopProcessName: "game-bin"})
		if ev.Verdict != c.verdict {
			t.Fatalf("pids=%v err=%v: got %+v, want %q", c.pids, c.err, ev, c.verdict)
		}
	}
}

func TestLivenessAttachmentZeroFingerprintNotEvidence(t *testing.T) {
	// Attachments postdate the fingerprint schema: a zero fingerprint is
	// malformed, and whatever process holds that PID now must not
	// impersonate a live bridge owner.
	swapLivenessProbes(t, hookReturning(StatusStopped, nil), nil, nil)
	claim := &RuntimeState{Attachment: &RuntimeAttachment{
		ConnectionID: "c1", OwnerPID: os.Getpid(), OwnerPIDStartTime: 0,
		LeaseDeadline: time.Now().Add(time.Minute),
	}}
	ev := EvaluateLiveness(LivenessInput{Claim: claim, StatusHook: &launch.ResolvedHook{Command: "x"}})
	if ev.Source != LivenessSourceStatusHook {
		t.Fatalf("zero-fingerprint attachment must not be evidence: %+v", ev)
	}
}

func TestLivenessAttachmentContradictionWarning(t *testing.T) {
	pid, start := ownFingerprint(t)
	swapLivenessProbes(t, hookReturning(StatusStopped, nil), nil, nil)
	claim := &RuntimeState{Attachment: &RuntimeAttachment{
		ConnectionID: "c1", OwnerPID: pid, OwnerPIDStartTime: start,
		LeaseDeadline: time.Now().Add(time.Minute),
	}}
	ev := EvaluateLiveness(LivenessInput{
		Claim: claim, StatusHook: &launch.ResolvedHook{Command: "x"}, DiagnoseHook: true,
	})
	if ev.Verdict != StatusRunning || ev.Source != LivenessSourceAttachment {
		t.Fatalf("fresh lease wins the contradiction: %+v", ev)
	}
	if len(ev.Warnings) == 0 || ev.HookResult == nil {
		t.Fatalf("attachment-tier contradictions must be reported too: %+v", ev)
	}
}
