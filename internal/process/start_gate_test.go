package process

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// probeHookLifecycle builds a resolved lifecycle whose status hook encodes
// its verdict in the command; installProbeFake decodes it.
func probeHookLifecycle(verdict string) *launch.ResolvedLifecycle {
	return &launch.ResolvedLifecycle{Status: &launch.ResolvedHook{Command: "verdict:" + verdict, TimeoutSeconds: 1}}
}

func installProbeFake(t *testing.T) {
	t.Helper()
	prev := runStatusHookFunc
	runStatusHookFunc = func(h *launch.ResolvedHook, gameID, profile string) (string, HookResult) {
		return strings.TrimPrefix(h.Command, "verdict:"), HookResult{ExitCode: 0}
	}
	t.Cleanup(func() { runStatusHookFunc = prev })
}

func gateFor(gameID, dir string) StartGate {
	return StartGate{
		GameID:     gameID,
		ConfigDir:  dir,
		InstanceID: "inst-1",
		Spec:       m2Spec(gameID),
		Budget:     10 * time.Second,
	}
}

func TestGateStartCreatesCompletePreflightClaim(t *testing.T) {
	dir := t.TempDir()
	res, err := GateStart(gateFor("g1", dir))
	if err != nil || res.Refusal != nil {
		t.Fatalf("clean gate must pass: %+v %v", res, err)
	}
	claim, err := LoadRuntimeState("g1", dir)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if claim.Phase != PhaseStarting || claim.SpawnState != SpawnStatePreflight {
		t.Fatalf("preflight claim wrong: %+v", claim)
	}
	op := claim.Operation
	if op == nil || op.Action != OperationActionStart || op.OperationID == "" {
		t.Fatalf("operation must be stamped: %+v", op)
	}
	if op.ExecutorPID != os.Getpid() || op.ExecutorPIDStartTime == 0 {
		t.Fatalf("executor fingerprint must be stamped: %+v", op)
	}
	if op.Deadline.IsZero() || claim.ProcessStartDeadline.IsZero() {
		t.Fatalf("deadlines must be pinned: %+v", claim)
	}
}

func TestGateStartAlreadyRunning(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()
	spec := m2Spec("g2")
	spec.Lifecycle = probeHookLifecycle(StatusRunning)
	state := NewRuntimeState(spec, RuntimeStateStatusRunning)
	if err := ClaimRuntimeState("g2", dir, state); err != nil {
		t.Fatal(err)
	}

	g := gateFor("g2", dir)
	g.RequestedProfile = "other"
	res, err := GateStart(g)
	if err != nil || res.Refusal == nil || res.Refusal.Code != RefusalAlreadyRunning {
		t.Fatalf("running claim must refuse already_running: %+v %v", res, err)
	}
	if res.Refusal.ActiveProfile != "combat" || res.Refusal.RequestedProfile != "other" {
		t.Fatalf("both profiles must be reported: %+v", res.Refusal)
	}
}

func TestGateStartBlockedUnknown(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()
	spec := m2Spec("g3")
	spec.Lifecycle = probeHookLifecycle(StatusUnknown)
	if err := ClaimRuntimeState("g3", dir, NewRuntimeState(spec, RuntimeStateStatusRunning)); err != nil {
		t.Fatal(err)
	}
	res, err := GateStart(gateFor("g3", dir))
	if err != nil || res.Refusal == nil || res.Refusal.Code != RefusalBlockedUnknown {
		t.Fatalf("unknown evidence with a claim must block: %+v %v", res, err)
	}
	if res.Refusal.Evidence == nil {
		t.Fatalf("blocked refusal must carry the evidence")
	}
}

func TestGateStartClearsStaleClaimAndProceeds(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()
	spec := m2Spec("g4")
	spec.Lifecycle = probeHookLifecycle(StatusStopped)
	old := NewRuntimeState(spec, RuntimeStateStatusRunning)
	if err := ClaimRuntimeState("g4", dir, old); err != nil {
		t.Fatal(err)
	}
	res, err := GateStart(gateFor("g4", dir))
	if err != nil || res.Refusal != nil || res.Claim == nil {
		t.Fatalf("stopped evidence must clear the stale claim: %+v %v", res, err)
	}
	if res.Claim.LaunchID == old.LaunchID {
		t.Fatalf("the new claim must be a fresh launch identity")
	}
}

func TestGateStartOperationInProgress(t *testing.T) {
	dir := t.TempDir()
	// first start holds its preflight claim with a live executor (this test
	// process); a second gate must refuse with the operation timing.
	if res, err := GateStart(gateFor("g5", dir)); err != nil || res.Refusal != nil {
		t.Fatalf("first gate must pass: %+v %v", res, err)
	}
	res, err := GateStart(gateFor("g5", dir))
	if err != nil || res.Refusal == nil || res.Refusal.Code != RefusalOperationInFlight {
		t.Fatalf("concurrent start must see operation_in_progress: %+v %v", res, err)
	}
	if res.Refusal.Operation == nil || res.Refusal.Operation.Action != OperationActionStart {
		t.Fatalf("refusal must carry the operation timing: %+v", res.Refusal)
	}
}

func TestGateStartDeadExecutorFallsThrough(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()
	spec := m2Spec("g6")
	spec.Lifecycle = probeHookLifecycle(StatusStopped)
	state := NewRuntimeState(spec, RuntimeStateStatusStarting)
	state.Operation = &RuntimeOperation{
		OperationID: NewFencingID(), Action: OperationActionStart,
		ExecutorPID: 99999999, ExecutorPIDStartTime: 12345,
		AttemptStartedAt: time.Now().UTC(), Deadline: time.Now().Add(time.Hour),
	}
	if err := ClaimRuntimeState("g6", dir, state); err != nil {
		t.Fatal(err)
	}
	res, err := GateStart(gateFor("g6", dir))
	if err != nil || res.Refusal != nil || res.Claim == nil {
		t.Fatalf("a dead executor's orphaned attempt must not block forever: %+v %v", res, err)
	}
}

func TestGateStartUnobservedSupersession(t *testing.T) {
	dir := t.TempDir()
	// an unobserved starting claim past its budget, with nothing observable
	spec := m2Spec("g7")
	state := NewRuntimeState(spec, RuntimeStateStatusStarting)
	state.GamePID = 0
	state.ProcessStartDeadline = time.Now().Add(-time.Minute)
	if err := ClaimRuntimeState("g7", dir, state); err != nil {
		t.Fatal(err)
	}
	res, err := GateStart(gateFor("g7", dir))
	if err != nil || res.Refusal != nil || res.Claim == nil {
		t.Fatalf("expired unobserved claim with no evidence must be reclaimable: %+v %v", res, err)
	}

	// the same claim within its budget still blocks
	state2 := NewRuntimeState(spec, RuntimeStateStatusStarting)
	state2.ProcessStartDeadline = time.Now().Add(time.Hour)
	_ = RemoveRuntimeState("g7", dir)
	if err := ClaimRuntimeState("g7", dir, state2); err != nil {
		t.Fatal(err)
	}
	res, err = GateStart(gateFor("g7", dir))
	if err != nil || res.Refusal == nil || res.Refusal.Code != RefusalBlockedUnknown {
		t.Fatalf("an unexpired starting claim must still block: %+v %v", res, err)
	}
}

func TestGateStartProbeDetectsSingleExternalProfile(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()
	g := gateFor("g8", dir)
	g.Probes = map[string]*launch.ResolvedLifecycle{
		"vanilla": probeHookLifecycle(StatusStopped),
		"combat":  probeHookLifecycle(StatusRunning),
	}
	res, err := GateStart(g)
	if err != nil || res.Refusal == nil || res.Refusal.Code != RefusalExternalInstance {
		t.Fatalf("running probe must refuse the start: %+v %v", res, err)
	}
	if !res.Refusal.SnapshotPersisted || res.Refusal.ActiveProfile != "combat" {
		t.Fatalf("single attribution must persist a snapshot: %+v", res.Refusal)
	}

	snap, err := LoadRuntimeState("g8", dir)
	if err != nil || snap == nil {
		t.Fatal(err)
	}
	if snap.Source != SourceExternal || snap.Phase != PhaseActive || snap.ObservedProfile != "combat" {
		t.Fatalf("external snapshot wrong: %+v", snap)
	}
	if snap.AppliedInputsState != AppliedInputsStateUnavailable {
		t.Fatalf("external snapshot must mark inputs unavailable: %+v", snap)
	}
	if snap.Lifecycle == nil || snap.Lifecycle.Status == nil {
		t.Fatalf("external snapshot must pin the detected profile's hooks: %+v", snap)
	}
	if snap.Endpoint != nil || snap.Operation != nil {
		t.Fatalf("external snapshot must not carry GABS-only fields: %+v", snap)
	}
}

func TestGateStartProbeMultipleCandidatesNoSnapshot(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()
	g := gateFor("g9", dir)
	g.Probes = map[string]*launch.ResolvedLifecycle{
		"a": probeHookLifecycle(StatusRunning),
		"b": probeHookLifecycle(StatusRunning),
	}
	res, err := GateStart(g)
	if err != nil || res.Refusal == nil || res.Refusal.Code != RefusalExternalInstance {
		t.Fatalf("ambiguous detection must refuse: %+v %v", res, err)
	}
	if res.Refusal.SnapshotPersisted || len(res.Refusal.Candidates) != 2 {
		t.Fatalf("GABS never guesses among candidates: %+v", res.Refusal)
	}
	if claim, _ := LoadRuntimeState("g9", dir); claim != nil {
		t.Fatalf("no snapshot may persist on ambiguity: %+v", claim)
	}
}

func TestGateStartUnknownProbesWarnAndProceed(t *testing.T) {
	installProbeFake(t)
	dir := t.TempDir()
	g := gateFor("g10", dir)
	g.Probes = map[string]*launch.ResolvedLifecycle{
		"a": probeHookLifecycle(StatusUnknown),
		"b": probeHookLifecycle(StatusStopped),
	}
	res, err := GateStart(g)
	if err != nil || res.Refusal != nil || res.Claim == nil {
		t.Fatalf("unknown probes must not brick starts (no claim = GABS owns nothing): %+v %v", res, err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "a") {
		t.Fatalf("unprobeable profiles must be named in a warning: %v", res.Warnings)
	}
}

func TestGateStartStopProcessNameDetection(t *testing.T) {
	dir := t.TempDir()
	swapLivenessProbes(t, nil, nil, func(string) ([]int, error) { return []int{4242}, nil })
	g := gateFor("g11", dir)
	g.StopProcessName = "game-bin"
	res, err := GateStart(g)
	if err != nil || res.Refusal == nil || res.Refusal.Code != RefusalExternalInstance {
		t.Fatalf("unique name match must refuse: %+v %v", res, err)
	}
	if !res.Refusal.SnapshotPersisted {
		t.Fatalf("unique name match persists a snapshot: %+v", res.Refusal)
	}
	snap, _ := LoadRuntimeState("g11", dir)
	if snap == nil || snap.ObservedProfile != ObservedProfileUnknown || snap.Source != SourceExternal {
		t.Fatalf("name-based snapshot must record observedProfile unknown: %+v", snap)
	}
	if snap.StopProcessName != "game-bin" {
		t.Fatalf("built-in fallback must be pinned as the control mechanism: %+v", snap)
	}
}

func TestGateStartStopProcessNameCollision(t *testing.T) {
	dir := t.TempDir()
	swapLivenessProbes(t, nil, nil, func(string) ([]int, error) { return []int{1, 2}, nil })
	g := gateFor("g12", dir)
	g.StopProcessName = "game-bin"
	res, err := GateStart(g)
	if err != nil || res.Refusal == nil || res.Refusal.SnapshotPersisted {
		t.Fatalf("colliding matches must refuse without a snapshot: %+v %v", res, err)
	}
	if claim, _ := LoadRuntimeState("g12", dir); claim != nil {
		t.Fatalf("no claim may remain: %+v", claim)
	}
}

func TestFencedTransition(t *testing.T) {
	dir := t.TempDir()
	res, err := GateStart(gateFor("g13", dir))
	if err != nil || res.Claim == nil {
		t.Fatal(err)
	}
	launchID := res.Claim.LaunchID
	opID := res.Claim.Operation.OperationID

	// matching identity applies and bumps the generation
	updated, err := FencedTransition("g13", dir, launchID, opID, func(s *RuntimeState) error {
		s.SpawnState = SpawnStateSpawning
		return nil
	})
	if err != nil || updated.SpawnState != SpawnStateSpawning || updated.Generation != res.Claim.Generation+1 {
		t.Fatalf("fenced transition must apply: %+v %v", updated, err)
	}

	// wrong launch identity is discarded
	if _, err := FencedTransition("g13", dir, NewFencingID(), opID, func(s *RuntimeState) error {
		s.SpawnState = SpawnStateFailed
		return nil
	}); err != ErrFencingViolation {
		t.Fatalf("stale launch identity must be fenced: %v", err)
	}
	// wrong operation identity is discarded
	if _, err := FencedTransition("g13", dir, launchID, NewFencingID(), func(s *RuntimeState) error {
		return nil
	}); err != ErrFencingViolation {
		t.Fatalf("stale operation identity must be fenced: %v", err)
	}
	if after, _ := LoadRuntimeState("g13", dir); after.SpawnState != SpawnStateSpawning {
		t.Fatalf("fenced-off mutations must not persist: %+v", after)
	}
}
