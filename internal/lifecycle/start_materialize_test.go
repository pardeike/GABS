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
}

func (c *spyMaterializeController) Configure(spec process.LaunchSpec) error { return nil }
func (c *spyMaterializeController) SetBridgeInfo(port int, token string)    {}
func (c *spyMaterializeController) MaterializeSpawnSpec() (string, string, error) {
	return c.resolvedExe, c.resolvedCwd, nil
}
func (c *spyMaterializeController) Start() error {
	// computeSpawnDigests and the FencedTransition that persists them run BEFORE
	// the starter calls Start(). Capture the persisted digests, then fail fast so
	// the start ends here — the claim is cleaned, but we already have what we
	// need to prove what was digested.
	if st, _ := process.LoadRuntimeState(c.gameID, c.configDir); st != nil {
		c.captured = st.ContextDigests
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
