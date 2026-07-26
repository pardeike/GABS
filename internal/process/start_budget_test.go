package process

import (
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// TestGateStartPreservesFullStageFourBudget pins the budget contract: Stage 2
// work — claim evaluation and pre-start probes — runs on its own preflight
// deadline, and the full process-start budget is re-stamped once the probes
// complete. A valid game whose status hook is merely slow must never lose its
// Stage 4 verification window to that hook, and a just-spawned unobserved
// launch must not be immediately supersedable.
func TestGateStartPreservesFullStageFourBudget(t *testing.T) {
	dir := t.TempDir()
	prev := runStatusHookFunc
	runStatusHookFunc = func(h *launch.ResolvedHook, gameID, profile string) (string, HookResult) {
		time.Sleep(1200 * time.Millisecond)
		return StatusStopped, HookResult{ExitCode: 0}
	}
	t.Cleanup(func() { runStatusHookFunc = prev })

	g := gateFor("g1", dir)
	g.Budget = 2 * time.Second
	g.Probes = map[string]*launch.ResolvedLifecycle{"": probeHookLifecycle(StatusStopped)}

	res, err := GateStart(g)
	if err != nil || res.Refusal != nil {
		t.Fatalf("a slow-but-stopped probe must not refuse the start: %+v %v", res, err)
	}

	remaining := time.Until(res.Claim.Operation.Deadline)
	if remaining < g.Budget-500*time.Millisecond {
		t.Fatalf("probe time was charged against the Stage 4 budget: %v of %v remain", remaining, g.Budget)
	}
	if !res.Claim.ProcessStartDeadline.Equal(res.Claim.Operation.Deadline) {
		t.Fatalf("ProcessStartDeadline must track the re-stamped operation deadline: %v vs %v",
			res.Claim.ProcessStartDeadline, res.Claim.Operation.Deadline)
	}

	// The persisted claim carries the re-stamped window too — the on-disk
	// deadline is what concurrent starts and supersession judge.
	claim, err := LoadRuntimeState("g1", dir)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if !claim.Operation.Deadline.Equal(res.Claim.Operation.Deadline) {
		t.Fatalf("persisted deadline differs from returned claim: %v vs %v",
			claim.Operation.Deadline, res.Claim.Operation.Deadline)
	}
}

// TestPreflightProbeBudgetCoversSlowestHook pins that the preflight deadline
// scales with the slowest configured status-hook timeout: concurrent probes
// are bounded by the slowest hook, not the sum, and never by the process-start
// budget.
func TestPreflightProbeBudgetCoversSlowestHook(t *testing.T) {
	g := StartGate{Probes: map[string]*launch.ResolvedLifecycle{
		"a": {Status: &launch.ResolvedHook{TimeoutSeconds: 3}},
		"b": {Status: &launch.ResolvedHook{TimeoutSeconds: 45}},
		"c": nil,
	}}
	got := preflightProbeBudget(g)
	want := 45*time.Second + preflightProbeMargin
	if got != want {
		t.Fatalf("preflight budget must cover the slowest hook plus margin: got %v, want %v", got, want)
	}
	if empty := preflightProbeBudget(StartGate{}); empty != preflightProbeMargin {
		t.Fatalf("no probes must still leave the fixed margin: %v", empty)
	}
}
