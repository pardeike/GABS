package process

import (
	"errors"
	"strings"
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

// TestGateStartErrorCarriesEarnedWarnings pins evidence preservation on the
// gate's own error path: when a probe has warned and a later preflight step
// fails (here: persisting the stopProcessName external snapshot), the error
// return must still carry the probe warnings for the frontends.
func TestGateStartErrorCarriesEarnedWarnings(t *testing.T) {
	dir := t.TempDir()
	installProbeFake(t)

	findProcessesByNameMu.Lock()
	prevFind := findProcessesByNameFunc
	findProcessesByNameFunc = func(string) ([]int, error) { return []int{4242}, nil }
	findProcessesByNameMu.Unlock()
	t.Cleanup(func() {
		findProcessesByNameMu.Lock()
		findProcessesByNameFunc = prevFind
		findProcessesByNameMu.Unlock()
	})
	restore := SetSaveRuntimeStateFailHookForTesting(func() error { return errors.New("simulated snapshot outage") })
	t.Cleanup(restore)

	g := gateFor("g1", dir)
	g.Probes = map[string]*launch.ResolvedLifecycle{"p": probeHookLifecycle(StatusUnknown)}
	g.StopProcessName = "workload"

	res, err := GateStart(g)
	if err == nil {
		t.Fatalf("the forced snapshot outage must surface as an error, got %+v", res)
	}
	if res == nil || len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "could not probe") {
		t.Fatalf("the gate error must still carry the earned probe warnings, got %+v", res)
	}
}

// TestExternalSnapshotClearsRefusedHistoryIdentity pins track-record
// integrity: the fresh claim converted into an external snapshot was
// initialized from the REFUSED request, so its history identity must not
// follow the external workload — stop/kill accounting would otherwise credit
// or reset that context for a launch GABS never performed.
func TestExternalSnapshotClearsRefusedHistoryIdentity(t *testing.T) {
	dir := t.TempDir()
	g := gateFor("g1", dir)

	state := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
	state.HistoryContextHash = "refused-context-hash"
	state.HistorySuccess = &HistorySuccessIdentity{}
	state.Operation = &RuntimeOperation{
		OperationID:      NewFencingID(),
		Action:           OperationActionStart,
		AttemptStartedAt: time.Now().UTC(),
		Deadline:         time.Now().UTC().Add(10 * time.Second),
	}
	if err := ClaimRuntimeState("g1", dir, state); err != nil {
		t.Fatal(err)
	}

	if err := persistExternalSnapshot(g, &state, "p", nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	claim, err := LoadRuntimeState("g1", dir)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if claim.HistoryContextHash != "" || claim.HistorySuccess != nil {
		t.Fatalf("the refused request's history identity must be cleared from the external snapshot: %+v", claim)
	}
}

// TestRestampedClaimSurvivesStaleRemoval pins the expired-probe race: a
// concurrent start that judged a preflight claim while its deadline was
// expired must not remove it after the probing executor re-stamped the
// deadline and advanced — launch and operation IDs survive that transition,
// so only the generation fence proves the judgment is stale.
func TestRestampedClaimSurvivesStaleRemoval(t *testing.T) {
	dir := t.TempDir()
	g := gateFor("g1", dir)

	state := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
	state.Operation = &RuntimeOperation{
		OperationID:      NewFencingID(),
		Action:           OperationActionStart,
		AttemptStartedAt: time.Now().UTC().Add(-time.Minute),
		Deadline:         time.Now().UTC().Add(-30 * time.Second), // expired mid-probe
	}
	if err := ClaimRuntimeState("g1", dir, state); err != nil {
		t.Fatal(err)
	}
	evaluated, err := LoadRuntimeState("g1", dir)
	if err != nil || evaluated == nil {
		t.Fatal(err)
	}

	// The probing executor completes and re-stamps its full budget — same
	// launch and operation identity, new deadline, bumped generation.
	if _, err := FencedTransition("g1", dir, evaluated.LaunchID, evaluated.Operation.OperationID, func(s *RuntimeState) error {
		s.Operation.Deadline = time.Now().UTC().Add(10 * time.Second)
		s.ProcessStartDeadline = s.Operation.Deadline
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := removeEvaluatedClaim(g, evaluated); err != ErrFencingViolation {
		t.Fatalf("a stale judgment must never remove the re-stamped claim: %v", err)
	}
	if cur, _ := LoadRuntimeState("g1", dir); cur == nil {
		t.Fatal("the re-stamped claim must survive")
	}
}

// TestPreflightDeadlineIsTheConfiguredBudget pins design/12's "no derived
// probe budget": the deadline published with the preflight claim is the
// configured process-start budget, never a window derived from hook timeouts.
// A probe observes the on-disk claim mid-probe, so a derived deadline (the
// 45-second hook timeout plus margin) would be caught here.
func TestPreflightDeadlineIsTheConfiguredBudget(t *testing.T) {
	dir := t.TempDir()
	var observed time.Time
	prev := runStatusHookFunc
	runStatusHookFunc = func(h *launch.ResolvedHook, gameID, profile string) (string, HookResult) {
		if claim, err := LoadRuntimeState("g1", dir); err == nil && claim != nil && claim.Operation != nil {
			observed = claim.Operation.Deadline
		}
		return StatusStopped, HookResult{ExitCode: 0}
	}
	t.Cleanup(func() { runStatusHookFunc = prev })

	g := gateFor("g1", dir)
	g.Budget = 2 * time.Second
	g.Probes = map[string]*launch.ResolvedLifecycle{
		"": {Status: &launch.ResolvedHook{Command: "verdict:stopped", TimeoutSeconds: 45}},
	}
	before := time.Now()
	if res, err := GateStart(g); err != nil || res.Refusal != nil {
		t.Fatalf("gate must pass: %+v %v", res, err)
	}

	if observed.IsZero() {
		t.Fatal("the probe must have observed the published preflight claim")
	}
	if observed.After(before.Add(10 * time.Second)) {
		t.Fatalf("the preflight deadline must be the configured budget, not derived from the 45s hook timeout: %v after %v", observed, before)
	}
	if !observed.After(before) {
		t.Fatalf("the preflight deadline must be a real forward window: %v vs %v", observed, before)
	}
}
