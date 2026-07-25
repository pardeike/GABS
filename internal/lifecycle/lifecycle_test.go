package lifecycle

import (
	"errors"
	"os"
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
	ev, claim, err := m.Status("g", false)
	if err != nil || claim != nil {
		t.Fatalf("no-claim status: err=%v claim=%v", err, claim)
	}
	if ev.Verdict != process.StatusStopped {
		t.Fatalf("no claim must read stopped, got %q", ev.Verdict)
	}

	// Seed a claim whose workload PID is THIS live test process: liveness reads
	// running via the PID fingerprint (cross-platform).
	spec := process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.GamePID = os.Getpid()
	if fp, ferr := process.ProcessStartTime(os.Getpid()); ferr == nil {
		st.PIDStartTime = fp
	}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	ev, claim, err = m.Status("g", false)
	if err != nil || claim == nil {
		t.Fatalf("seeded status: err=%v claim=%v", err, claim)
	}
	if ev.Verdict != process.StatusRunning {
		t.Fatalf("a claim on a live PID must read running, got %q (%s)", ev.Verdict, ev.Detail)
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
