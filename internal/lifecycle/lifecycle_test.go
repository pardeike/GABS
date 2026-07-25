package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func testManager(t *testing.T, gamesConfig *config.GamesConfig) *Manager {
	t.Helper()
	return NewManager(util.NewLogger("error"), t.TempDir(), "test-instance", gamesConfig, 0, nil, nil)
}

// managerAt binds a manager to a specific config dir (so seeding + reads share it).
func managerAt(configDir string, gamesConfig *config.GamesConfig) *Manager {
	return NewManager(util.NewLogger("error"), configDir, "test-instance", gamesConfig, 0, nil, nil)
}

func TestStartBudgetForDefaultsAndConfig(t *testing.T) {
	m := testManager(t, nil)
	if got := m.StartBudgetFor("DirectPath"); got != 10*time.Second {
		t.Fatalf("default non-URL budget = %v, want 10s", got)
	}
	if got := m.StartBudgetFor("SteamAppId"); got != 60*time.Second {
		t.Fatalf("URL-mode budget = %v, want 60s", got)
	}
	// Explicit config wins.
	cfg := &config.GamesConfig{Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{ProcessStartSeconds: 42}}}
	m = testManager(t, cfg)
	if got := m.StartBudgetFor("DirectPath"); got != 42*time.Second {
		t.Fatalf("configured budget = %v, want 42s", got)
	}
}

func TestRuntimeOwnerLeaseForOperation(t *testing.T) {
	m := testManager(t, nil)
	base := m.RuntimeOwnerLeaseDuration()
	if base <= 0 {
		t.Fatalf("default lease must be positive, got %v", base)
	}
	if got := m.RuntimeOwnerLeaseForOperation(0); got != base {
		t.Fatalf("zero operation timeout must keep the base lease, got %v", got)
	}
	// A long operation extends the lease past the base.
	long := base + 30*time.Second
	if got := m.RuntimeOwnerLeaseForOperation(long); got != long+5*time.Second {
		t.Fatalf("long operation lease = %v, want %v", got, long+5*time.Second)
	}
}

func TestSpecBuilders(t *testing.T) {
	game := config.GameConfig{ID: "g", LaunchMode: "DirectPath", Target: "/bin/true", Args: []string{"a"}, WorkingDir: "/wd", StopProcessName: "proc"}
	base := LaunchSpecFromGame(game)
	if base.GameId != "g" || base.PathOrId != "/bin/true" || base.StopProcessName != "proc" {
		t.Fatalf("LaunchSpecFromGame mapped wrong: %+v", base)
	}
	// resolved=nil keeps the base target/args.
	if fromNil := LaunchSpecFromResolved(game, nil); fromNil.PathOrId != "/bin/true" {
		t.Fatalf("LaunchSpecFromResolved(nil) target = %q", fromNil.PathOrId)
	}
	m := testManager(t, nil)
	spec := m.LaunchSpecWithRuntimeDir(base)
	if spec.RuntimeDir == "" {
		t.Fatal("LaunchSpecWithRuntimeDir must stamp a runtime dir")
	}
}

func TestStartErrorTypes(t *testing.T) {
	if e := (&StartRefusalError{Refusal: &process.StartRefusal{Message: "nope"}}); e.Error() != "nope" {
		t.Fatalf("StartRefusalError.Error() = %q", e.Error())
	}
	if e := (&UnobservedStartError{}); e.Error() == "" {
		t.Fatal("UnobservedStartError.Error() must be non-empty")
	}
	exited := &ExitedDuringStartError{ExitCode: 7}
	if got := exited.Error(); got == "" || !contains(got, "7") {
		t.Fatalf("ExitedDuringStartError.Error() = %q, want exit code 7", got)
	}
	underlying := errors.New("boom")
	ep := &EndpointUnavailableError{GameID: "g", Err: underlying}
	if !errors.Is(ep, underlying) {
		t.Fatal("EndpointUnavailableError must unwrap to its cause")
	}
	active := &GameAlreadyActiveError{Status: process.RuntimeStateStatusStarting}
	if active.ToolMessage(config.GameConfig{ID: "g", Name: "G"}) == "" {
		t.Fatal("GameAlreadyActiveError.ToolMessage must render")
	}
}

func TestStatusNoClaimAndLiveClaim(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	// No claim -> stopped, nil claim.
	status, _, claim, err := m.Status("g", false, nil)
	if err != nil || claim != nil {
		t.Fatalf("no-claim status: err=%v claim=%v", err, claim)
	}
	if status != "stopped" {
		t.Fatalf("no claim must read stopped, got %q", status)
	}

	// An active claim on THIS live test process reads running (PID fingerprint).
	seedLiveActive(t, dir, "g")
	status, _, claim, err = m.Status("g", false, nil)
	if err != nil || claim == nil {
		t.Fatalf("seeded status: err=%v claim=%v", err, claim)
	}
	if status != process.RuntimeStateStatusRunning {
		t.Fatalf("a claim on a live PID must read running, got %q", status)
	}
}

// The shared status machine, driven with a CLI's false+nil evidence, must
// PASSIVELY PROMOTE a live, operation-less phase=starting claim to active and
// credit the workload start — the exact divergence the review flagged (a thin
// EvaluateLiveness read would leave the claim untouched).
func TestStatusPassivePromotionCreditsAndPromotes(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, &config.GamesConfig{Version: "1.0", Games: map[string]config.GameConfig{"g": {ID: "g"}}})

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusStarting)
	st.LaunchID = "L1"
	st.Phase = process.PhaseStarting // completed-unobserved shape
	st.Operation = nil
	st.SpawnState = process.SpawnStateSpawned
	st.HistoryContextHash = "ctx-1"
	st.HistorySuccess = &process.HistorySuccessIdentity{}
	st.GamePID = os.Getpid()
	if fp, ferr := process.ProcessStartTime(os.Getpid()); ferr == nil {
		st.PIDStartTime = fp
	}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	status, _, claim, err := m.Status("g", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != process.RuntimeStateStatusRunning {
		t.Fatalf("status = %q, want running (promoted)", status)
	}
	if claim == nil || claim.Phase != process.PhaseActive {
		t.Fatalf("the machine must promote the claim to active, got %+v", claim)
	}
	if h, herr := process.LoadHistory("g", dir); herr != nil || h == nil || h.Profiles[""] == nil || h.Profiles[""].WorkloadStarts < 1 {
		t.Fatalf("passive promotion must credit the workload start, history=%+v err=%v", h, herr)
	}
}

// A definitively-stopped claim (dead PID, positive stopped evidence via a stop
// process-name scan miss) is fenced-removed by the machine.
func TestStatusRemovesDefinitivelyStoppedClaim(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true", StopProcessName: "gabs-definitely-not-running-xyz"}, process.RuntimeStateStatusRunning)
	st.LaunchID = "L1"
	st.Phase = process.PhaseActive
	st.GamePID = -1 // no such PID -> stopped by PID evidence
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}
	status, _, claim, err := m.Status("g", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != "stale-runtime-cleaned" {
		t.Fatalf("status = %q, want stale-runtime-cleaned", status)
	}
	if claim != nil || process.RuntimeClaimExists("g", dir) {
		t.Fatalf("a definitively stopped claim must be removed, claim=%v exists=%v", claim, process.RuntimeClaimExists("g", dir))
	}
}

// When liveness is definitively stopped but the fenced removal fails for a
// non-fencing reason (write/lock/permission), Status must return a usable
// status ("unknown"), NOT the empty supersession-retry sentinel, and the claim
// must be retained (design/06 — only a durable removal clears a claim).
func TestStatusStoppedRemovalFailureReturnsUsableStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory-permission removal blocking is a POSIX behavior")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := t.TempDir()
	m := managerAt(dir, nil)

	// A definitively-stopped claim (no such PID, no such stop-process): the
	// machine will reach the fenced-removal branch.
	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true", StopProcessName: "gabs-definitely-not-running-xyz"}, process.RuntimeStateStatusRunning)
	st.LaunchID = "L1"
	st.Phase = process.PhaseActive
	st.GamePID = -1
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	// Make the claim's directory read-only so the unlink (or lock create) fails
	// with EACCES — a non-fencing removal failure. Reads still work (r-x), so the
	// initial load and the post-observation reload both succeed.
	claimDir := filepath.Dir(runtimeStatePathFor(t, dir, "g"))
	if err := os.Chmod(claimDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(claimDir, 0o755) })

	status, _, claim, err := m.Status("g", false, nil)
	if err != nil {
		t.Fatalf("a readable claim must not surface an error here: %v", err)
	}
	if status != "unknown" {
		t.Fatalf("status = %q, want unknown when a stopped claim cannot be removed", status)
	}
	if claim == nil || !process.RuntimeClaimExists("g", dir) {
		t.Fatalf("a claim whose removal failed must be RETAINED, claim=%v exists=%v", claim, process.RuntimeClaimExists("g", dir))
	}
}

// A post-observation reload FAILURE (a real filesystem I/O/permission fault on
// the claim or runtime dir) must be surfaced, not silently rendered as a
// successfully-removed claim: otherwise a caller presents an unverified state as
// a successful stop (design/04 — the persisted claim is authoritative).
func TestStatusPropagatesPostObservationReloadFailure(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	// A live claim (self PID): the machine returns running and touches nothing,
	// so only the injected post-observation reload can fail.
	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.LaunchID = "L1"
	st.Phase = process.PhaseActive
	st.GamePID = os.Getpid()
	if fp, ferr := process.ProcessStartTime(os.Getpid()); ferr == nil {
		st.PIDStartTime = fp
	}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("runtime dir unreadable")
	m.reloadRuntimeState = func(string, string) (*process.RuntimeState, error) {
		return nil, wantErr
	}

	status, _, claim, err := m.Status("g", false, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("a post-observation reload failure must be propagated, got err=%v", err)
	}
	if claim != nil {
		t.Fatalf("a failed reload must not hand back a claim (that reads as removed), got %+v", claim)
	}
	if status != "unknown" {
		t.Fatalf("status = %q, want unknown on reload failure", status)
	}
	// The failure was READING the claim, not removing it: it is still on disk.
	if !process.RuntimeClaimExists("g", dir) {
		t.Fatal("the claim must remain on disk after a reload-read failure")
	}
}

// runtimeStatePathFor returns the on-disk runtime.json path for a game so a test
// can manipulate the claim's directory (SafeRuntimeStatePath is the same path
// LoadRuntimeState reads).
func runtimeStatePathFor(t *testing.T, configDir, gameID string) string {
	t.Helper()
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		t.Fatal(err)
	}
	path, err := cp.SafeRuntimeStatePath(gameID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Pending-credit reconciliation: a live claim carrying pending clean-stop /
// delivery credits (a history write that failed earlier) is reconciled and
// drained on observation.
func TestStatusReconcilesPendingCredits(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.LaunchID = "L1"
	st.Phase = process.PhaseActive
	st.HistoryContextHash = "ctx-1"
	st.GamePID = os.Getpid()
	if fp, ferr := process.ProcessStartTime(os.Getpid()); ferr == nil {
		st.PIDStartTime = fp
	}
	st.PendingCleanStops = []process.PendingCredit{{ID: "evt-1", Profile: "", ContextHash: "ctx-1", At: time.Now().UTC()}}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	status, _, _, err := m.Status("g", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != process.RuntimeStateStatusRunning {
		t.Fatalf("status = %q, want running", status)
	}
	cur, _ := process.LoadRuntimeState("g", dir)
	if cur == nil || len(cur.PendingCleanStops) != 0 {
		t.Fatalf("observation must drain pending credits, got %+v", cur)
	}
}

// Dead-executor recovery WITH an attachment: an expired operation plus a fresh
// FOREIGN attachment lease (a still-alive owner in another process) reads
// running via the lease and the dead operation is normalized away.
func TestStatusRecoversDeadOperationWithAttachment(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusStarting)
	st.LaunchID = "L1"
	st.Phase = process.PhaseActive
	st.GamePID = 0 // no workload PID: the attachment lease is the running-evidence
	fp, _ := process.ProcessStartTime(os.Getpid())
	st.Attachment = &process.RuntimeAttachment{
		ConnectionID:      "c1",
		OwnerInstanceID:   "other-instance", // FOREIGN owner
		OwnerPID:          os.Getpid(),      // alive
		OwnerPIDStartTime: fp,
		ObservedAt:        time.Now().UTC(),
		LeaseDeadline:     time.Now().Add(time.Hour), // fresh
	}
	st.Operation = &process.RuntimeOperation{
		OperationID:      "op-1",
		Action:           process.OperationActionStart,
		ExecutorPID:      -1, // provably-gone executor
		AttemptStartedAt: time.Now().Add(-time.Hour),
		Deadline:         time.Now().Add(-time.Minute), // expired
	}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}
	status, _, _, err := m.Status("g", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != process.RuntimeStateStatusRunning {
		t.Fatalf("a dead operation with a fresh foreign attachment lease must recover to running, got %q", status)
	}
	if cur, _ := process.LoadRuntimeState("g", dir); cur != nil && cur.Operation != nil {
		t.Fatalf("recovery must clear the dead operation, got %+v", cur.Operation)
	}
}

// Supersession during a slow probe: while the observed (in-memory) claim was
// being judged, a successor replaced it on disk. The stale stopped verdict must
// NOT delete the successor — the machine detects the lost fence and re-evaluates
// the CURRENT claim.
func TestStatusSupersededByOnDiskSuccessor(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	// The successor (B) is live on disk.
	successor := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	successor.LaunchID = "B"
	successor.Phase = process.PhaseActive
	successor.GamePID = os.Getpid()
	if fp, ferr := process.ProcessStartTime(os.Getpid()); ferr == nil {
		successor.PIDStartTime = fp
	}
	if err := process.ClaimRuntimeState("g", dir, successor); err != nil {
		t.Fatal(err)
	}

	// The stale in-memory claim (A) that the probe was judging: a dead PID reads
	// stopped, so the machine would try to remove it — but the on-disk claim is
	// now B, so the fenced removal loses and it re-evaluates B.
	stale := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true", StopProcessName: "gabs-definitely-not-running-xyz"}, process.RuntimeStateStatusRunning)
	stale.LaunchID = "A"
	stale.Phase = process.PhaseActive
	stale.GamePID = -1 // no PID + a name scan that finds nothing -> stopped verdict

	status, _ := m.ObserveClaimStatus("g", &stale, false, nil)
	if status != process.RuntimeStateStatusRunning {
		t.Fatalf("supersession must re-evaluate the live successor to running, got %q", status)
	}
	if !process.RuntimeClaimExists("g", dir) {
		t.Fatal("the stale stopped verdict must NOT delete the live successor claim")
	}
}

// Dead-operation recovery: a claim with an expired operation is normalized on
// observation and reported per its liveness, never as in-progress.
func TestStatusRecoversDeadOperation(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusStarting)
	st.LaunchID = "L1"
	st.Phase = process.PhaseActive
	st.GamePID = os.Getpid()
	if fp, ferr := process.ProcessStartTime(os.Getpid()); ferr == nil {
		st.PIDStartTime = fp
	}
	// An operation whose deadline has already passed (dead attempt).
	st.Operation = &process.RuntimeOperation{
		OperationID:      "op-1",
		Action:           process.OperationActionStart,
		ExecutorPID:      -1, // provably gone executor
		AttemptStartedAt: time.Now().Add(-time.Hour),
		Deadline:         time.Now().Add(-time.Minute),
	}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}
	status, _, _, err := m.Status("g", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Never "starting" / in-progress: recovery normalized the dead attempt and
	// the live PID reads running.
	if status == process.RuntimeStateStatusStarting {
		t.Fatalf("a dead operation must be recovered, not reported in-progress; got %q", status)
	}
	if cur, _ := process.LoadRuntimeState("g", dir); cur != nil && cur.Operation != nil {
		t.Fatalf("recovery must clear the dead operation, got %+v", cur.Operation)
	}
}

func TestLoadStopClaim(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)
	if claim, err := m.LoadStopClaim("g", "DirectPath", ""); err != nil || claim != nil {
		t.Fatalf("no claim must be (nil, nil), got claim=%v err=%v", claim, err)
	}
	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}
	if claim, err := m.LoadStopClaim("g", "DirectPath", ""); err != nil || claim == nil {
		t.Fatalf("seeded claim must load, got claim=%v err=%v", claim, err)
	}
}

func TestSupersededStartRefusalByPhase(t *testing.T) {
	dir := t.TempDir()
	m := managerAt(dir, nil)

	// No claim -> the successor finished: operation_in_progress.
	if code := refusalCode(t, m.SupersededStartRefusal("g")); code != process.RefusalOperationInFlight {
		t.Fatalf("no-claim supersession code = %q", code)
	}
	// Active successor -> already_running.
	seed(t, dir, "g", process.PhaseActive)
	if code := refusalCode(t, m.SupersededStartRefusal("g")); code != process.RefusalAlreadyRunning {
		t.Fatalf("active-successor supersession code = %q", code)
	}
	// A non-active, operation-less claim -> blocked_unknown_state.
	_ = process.RemoveRuntimeState("g", dir)
	seed(t, dir, "g", process.PhaseStarting)
	if code := refusalCode(t, m.SupersededStartRefusal("g")); code != process.RefusalBlockedUnknown {
		t.Fatalf("starting-successor supersession code = %q", code)
	}
}

func TestComputeHistoryContextAndProven(t *testing.T) {
	dir := t.TempDir()
	game := config.GameConfig{ID: "g", Name: "G", LaunchMode: "DirectPath", Target: "/bin/true"}
	cfg := &config.GamesConfig{Version: "1.0", Games: map[string]config.GameConfig{"g": game}}
	m := managerAt(dir, cfg)
	snap := &config.Snapshot{Config: cfg, Revision: "r", ConfigDir: dir}

	hc := m.ComputeHistoryContext(snap, game, nil, nil)
	if hc.ContextHash == "" {
		t.Fatal("ComputeHistoryContext must produce a context hash for a resolvable game")
	}
	if m.ContextProven("g", hc) {
		t.Fatal("a fresh context must not be proven")
	}
	// The history write is fenced to a live claim with the same launchID, so
	// seed one, then record a verified start against exactly this context.
	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.LaunchID = "launch1"
	st.Phase = process.PhaseActive
	st.HistoryContextHash = hc.ContextHash
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}
	if err := process.RecordWorkloadStart("g", dir, "launch1", hc.Profile, hc.ContextHash, hc.Snapshot, process.SuccessBucket{}, time.Now().UTC()); err != nil {
		t.Fatalf("record workload start: %v", err)
	}
	if !m.ContextProven("g", hc) {
		t.Fatal("after a recorded workload start, the context must be proven")
	}
}

func TestNewInstanceIDUnique(t *testing.T) {
	a, b := NewInstanceID(), NewInstanceID()
	if a == b || a == "" {
		t.Fatalf("instance ids must be unique and non-empty: %q %q", a, b)
	}
}

// --- helpers ---

func seedLiveActive(t *testing.T, dir, gameID string) {
	t.Helper()
	st := process.NewRuntimeState(process.LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.GamePID = os.Getpid()
	if fp, ferr := process.ProcessStartTime(os.Getpid()); ferr == nil {
		st.PIDStartTime = fp
	}
	if err := process.ClaimRuntimeState(gameID, dir, st); err != nil {
		t.Fatalf("seed live-active claim: %v", err)
	}
}

func seed(t *testing.T, dir, gameID, phase string) {
	t.Helper()
	st := process.NewRuntimeState(process.LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.Phase = phase
	st.Operation = nil
	if err := process.ClaimRuntimeState(gameID, dir, st); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
}

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	var re *StartRefusalError
	if !errors.As(err, &re) {
		t.Fatalf("expected a *StartRefusalError, got %T (%v)", err, err)
	}
	return re.Refusal.Code
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
