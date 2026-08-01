package lifecycle

import (
	"errors"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/steam"
	"github.com/pardeike/gabs/internal/util"
)

// spyMaterializeController resolves a DISTINCT executable + working directory
// from MaterializeSpawnSpec (as a real SteamManaged launch does) and captures
// the context digests the start persisted just before spawn.
type spyMaterializeController struct {
	resolvedExe, resolvedCwd string
	configDir, gameID        string
	captured                 *process.RuntimeContextDigests
	startCalled              bool
	deadlineAtStart          time.Time
}

func (c *spyMaterializeController) Configure(spec process.LaunchSpec) error { return nil }
func (c *spyMaterializeController) SetBridgeInfo(port int, token string)    {}
func (c *spyMaterializeController) MaterializeSpawnSpec() (string, string, error) {
	return c.resolvedExe, c.resolvedCwd, nil
}
func (c *spyMaterializeController) Start() error {
	c.startCalled = true
	// computeSpawnDigests and the FencedTransition that persists them run BEFORE
	// the starter calls Start(). Capture the persisted digests, then fail fast so
	// the start ends here — the claim is cleaned, but we already have what we
	// need to prove what was digested.
	if st, _ := process.LoadRuntimeState(c.gameID, c.configDir); st != nil {
		c.captured = st.ContextDigests
		if st.Operation != nil {
			c.deadlineAtStart = st.Operation.Deadline
		}
	}
	return &process.ProcessError{Type: process.ProcessErrorTypeStart, Context: "spy stop", Err: errors.New("captured")}
}
func (c *spyMaterializeController) Stop(grace time.Duration) error      { return nil }
func (c *spyMaterializeController) Kill() error                         { return nil }
func (c *spyMaterializeController) IsRunning() bool                     { return false }
func (c *spyMaterializeController) GetPID() int                         { return 0 }
func (c *spyMaterializeController) SpawnFingerprint() (int, int64)      { return 0, 0 }
func (c *spyMaterializeController) GetLaunchMode() string               { return "SteamManaged" }
func (c *spyMaterializeController) GetStopProcessName() string          { return "" }
func (c *spyMaterializeController) IsLauncherProcessRunning() bool      { return false }
func (c *spyMaterializeController) FinalEnvironment() []string          { return nil }
func (c *spyMaterializeController) LaunchLogTail(maxBytes int64) string { return "" }
func (c *spyMaterializeController) SetSpawnObservers(before func() error, after func(pid int, startTime int64, spawnErr error)) {
}
func (c *spyMaterializeController) DirectChildExited() bool { return true }
func (c *spyMaterializeController) ExitCode() int           { return 1 }
func (c *spyMaterializeController) TerminateDirectChild()   {}

// Finding 1 (round 6): production m.Start must MATERIALIZE the SteamManaged spec
// before digesting. Without it the cwd digest is GABS's own os.Getwd(), so a
// correct welcome (reporting the resolved app directory) is recorded as a cwd
// MISMATCH / partial delivery. Drive a real start and evaluate the persisted
// digest against the resolved app dir: it must VERIFY.
func TestStartMaterializesSteamManagedBeforeDigesting(t *testing.T) {
	// Report the Steam client as running so the start skips store-launcher
	// assistance (no real side effects).
	restore := steam.SetClientControlForTesting(nil, func() bool { return true }, 0, 0)
	defer restore()
	readyRestore := steam.SetFunctionalReadinessForTesting(false, nil)
	defer readyRestore()

	dir := t.TempDir()
	appDir := t.TempDir() // the RESOLVED app working dir — distinct from os.Getwd()
	resolvedExe := appDir + "/game"

	spy := &spyMaterializeController{resolvedExe: resolvedExe, resolvedCwd: appDir, configDir: dir, gameID: "steamgame"}
	m := NewManager(util.NewLogger("error"), dir, "inst-1", &config.GamesConfig{Version: "1.0"}, 0,
		process.NewSerializedStarterForTesting(),
		func() process.ControllerInterface { return spy })

	game := config.GameConfig{ID: "steamgame", Name: "S", LaunchMode: "SteamManaged", Target: "123456"}
	_, err := m.Start(StartRequest{
		Game: game,
		// No WorkingDir configured: the resolved app dir must be digested.
		LaunchSpec:     process.LaunchSpec{GameId: "steamgame", Mode: "SteamManaged", PathOrId: "123456"},
		HistoryContext: HistoryContext{},
	})
	if err == nil {
		t.Fatal("the spy controller fails the spawn on purpose to end the start")
	}
	if spy.captured == nil {
		t.Fatal("the start did not persist context digests before spawn")
	}
	// The persisted cwd digest must be the RESOLVED app dir, so a welcome
	// reporting that dir VERIFIES — never a false mismatch against os.Getwd().
	del := process.EvaluateContextDelivery(spy.captured, &process.ObservedContext{Cwd: appDir})
	if del.Channels[process.DeliveryChannelCwd] != process.DeliveryVerified {
		t.Fatalf("the resolved app dir must verify against the persisted digest (materialize-before-digest); got %+v", del.Channels)
	}
}

func TestMacSteamManagedReadinessFailureReleasesClaimWithoutSpawnOrHistory(t *testing.T) {
	dir := t.TempDir()
	appDir := t.TempDir()
	spy := &spyMaterializeController{
		resolvedExe: appDir + "/game", resolvedCwd: appDir,
		configDir: dir, gameID: "factory",
	}
	m := NewManager(util.NewLogger("error"), dir, "inst-ready-fail", &config.GamesConfig{
		Version:  "1.0",
		Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{GABPConnectSeconds: 7}},
	}, 0,
		process.NewSerializedStarterForTesting(), func() process.ControllerInterface { return spy })

	var receivedAppID string
	var receivedTimeout time.Duration
	var readinessObservedAt, deadlineDuringReadiness, processDeadlineDuringReadiness time.Time
	restore := steam.SetFunctionalReadinessForTesting(true, func(appID string, timeout time.Duration) steam.ReadinessResult {
		receivedAppID = appID
		receivedTimeout = timeout
		readinessObservedAt = time.Now()
		if claim, _ := process.LoadRuntimeState("factory", dir); claim != nil && claim.Operation != nil {
			deadlineDuringReadiness = claim.Operation.Deadline
			processDeadlineDuringReadiness = claim.ProcessStartDeadline
		}
		return steam.ReadinessResult{
			Reason: steam.ReadinessReasonTimeout, Stage: steam.ReadinessStageSteamAPI,
			Detail: "Steamworks API initialization failed", Retryable: true,
			Waited: 25 * time.Millisecond, Timeout: timeout,
		}
	})
	t.Cleanup(restore)

	_, err := m.Start(StartRequest{
		Game:       config.GameConfig{ID: "factory", Name: "Factory", LaunchMode: "SteamManaged", Target: "123456"},
		LaunchSpec: process.LaunchSpec{GameId: "factory", Mode: "SteamManaged", PathOrId: "123456"},
	})
	var readinessErr *StoreClientNotReadyError
	if !errors.As(err, &readinessErr) {
		t.Fatalf("Start error = %T %v, want StoreClientNotReadyError", err, err)
	}
	if readinessErr.Reason != string(steam.ReadinessReasonTimeout) || !readinessErr.Retryable || readinessErr.ProcessStarted {
		t.Fatalf("readiness error = %+v", readinessErr)
	}
	if receivedTimeout != 7*time.Second {
		t.Fatalf("omitted readiness timeout = %v, want configured GABP timeout 7s", receivedTimeout)
	}
	if receivedAppID != "123456" {
		t.Fatalf("readiness app ID = %q, want configured target", receivedAppID)
	}
	if deadlineDuringReadiness.Sub(readinessObservedAt) < 6*time.Second || !deadlineDuringReadiness.Equal(processDeadlineDuringReadiness) {
		t.Fatalf("claim was not fenced to the readiness deadline: operation=%v process=%v", deadlineDuringReadiness, processDeadlineDuringReadiness)
	}
	if spy.startCalled {
		t.Fatal("the game process was started before Steam readiness was proven")
	}
	if claim, loadErr := process.LoadRuntimeState("factory", dir); loadErr != nil || claim != nil {
		t.Fatalf("fresh claim must be released: claim=%+v err=%v", claim, loadErr)
	}
	history, historyErr := process.LoadHistory("factory", dir)
	if historyErr != nil {
		t.Fatal(historyErr)
	}
	if len(history.Profiles) != 0 {
		t.Fatalf("pre-spawn readiness failure mutated history: %+v", history.Profiles)
	}
}

func TestMacSteamManagedReadinessSuccessRestampsFullProcessBudget(t *testing.T) {
	dir := t.TempDir()
	appDir := t.TempDir()
	spy := &spyMaterializeController{
		resolvedExe: appDir + "/game", resolvedCwd: appDir,
		configDir: dir, gameID: "adventure",
	}
	m := NewManager(util.NewLogger("error"), dir, "inst-ready-success", &config.GamesConfig{Version: "1.0"}, 0,
		process.NewSerializedStarterForTesting(), func() process.ControllerInterface { return spy })
	var probeFinishedAt time.Time
	restore := steam.SetFunctionalReadinessForTesting(true, func(appID string, timeout time.Duration) steam.ReadinessResult {
		if appID != "123456" {
			t.Fatalf("readiness app ID = %q", appID)
		}
		time.Sleep(25 * time.Millisecond)
		probeFinishedAt = time.Now()
		return steam.ReadinessResult{Ready: true, Stage: steam.ReadinessStageAppState, Waited: 25 * time.Millisecond, Timeout: timeout}
	})
	t.Cleanup(restore)

	_, err := m.Start(StartRequest{
		Game:                  config.GameConfig{ID: "adventure", Name: "Adventure", LaunchMode: "SteamManaged", Target: "123456"},
		LaunchSpec:            process.LaunchSpec{GameId: "adventure", Mode: "SteamManaged", PathOrId: "123456"},
		StoreReadinessTimeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("the spy fails spawn after capturing the restamped deadline")
	}
	if !spy.startCalled {
		t.Fatal("ready SteamManaged start never reached process spawn")
	}
	if remaining := spy.deadlineAtStart.Sub(probeFinishedAt); remaining < 9*time.Second {
		t.Fatalf("readiness consumed the normal process-start budget: remaining=%v deadline=%v", remaining, spy.deadlineAtStart)
	}
}

func TestMacSteamManagedReadinessSuccessRestampsExpiredLeaseByFencingIdentity(t *testing.T) {
	dir := t.TempDir()
	appDir := t.TempDir()
	spy := &spyMaterializeController{
		resolvedExe: appDir + "/game", resolvedCwd: appDir,
		configDir: dir, gameID: "factory",
	}
	m := NewManager(util.NewLogger("error"), dir, "inst-ready-expired", &config.GamesConfig{Version: "1.0"}, 0,
		process.NewSerializedStarterForTesting(), func() process.ControllerInterface { return spy })

	var expireErr error
	restore := steam.SetFunctionalReadinessForTesting(true, func(appID string, timeout time.Duration) steam.ReadinessResult {
		if appID != "123456" {
			t.Fatalf("readiness app ID = %q", appID)
		}
		claim, err := process.LoadRuntimeState("factory", dir)
		if err != nil {
			expireErr = err
			return steam.ReadinessResult{Ready: true, Stage: steam.ReadinessStageAppState, Timeout: timeout}
		}
		if claim == nil || claim.Operation == nil {
			expireErr = errors.New("readiness claim or operation is missing")
			return steam.ReadinessResult{Ready: true, Stage: steam.ReadinessStageAppState, Timeout: timeout}
		}
		_, expireErr = process.FencedTransition("factory", dir, claim.LaunchID, claim.Operation.OperationID, func(st *process.RuntimeState) error {
			expired := time.Now().UTC().Add(-time.Second)
			st.Operation.Deadline = expired
			st.ProcessStartDeadline = expired
			return nil
		})
		return steam.ReadinessResult{Ready: true, Stage: steam.ReadinessStageAppState, Timeout: timeout}
	})
	t.Cleanup(restore)

	_, err := m.Start(StartRequest{
		Game:                  config.GameConfig{ID: "factory", Name: "Factory", LaunchMode: "SteamManaged", Target: "123456"},
		LaunchSpec:            process.LaunchSpec{GameId: "factory", Mode: "SteamManaged", PathOrId: "123456"},
		StoreReadinessTimeout: time.Second,
	})
	if expireErr != nil {
		t.Fatalf("expiring the old readiness lease: %v", expireErr)
	}
	if err == nil {
		t.Fatal("the spy fails spawn after proving that the restamp reached it")
	}
	if !spy.startCalled {
		t.Fatal("a completed readiness proof must restamp the unchanged fencing identity even after the old lease deadline")
	}
}
