package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// A connection whose claim vanished during the handshake has no binding:
// it is closed, typed superseded, and never mirrors tools.
func TestConnectorClosesConnectionWhenClaimVanishesDuringHandshake(t *testing.T) {
	s := newProfiledServer(t)

	addr, _ := fakeGABPServerWithHello(t, "adventure", func() {
		// The claim disappears exactly while the handshake is in flight.
		_ = process.RemoveRuntimeState("adventure", s.configDir)
	})
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)

	seedClaimEndpointForTest(t, s.configDir, "adventure", port, "launch-token")

	connector := NewServerGABPConnector(s, 10*time.Millisecond, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := connector.AttemptConnection(ctx, "adventure", port, "launch-token")

	var superseded *supersededConnectionError
	if !errors.As(err, &superseded) {
		t.Fatalf("an unpublishable connection must be typed superseded, got %v", err)
	}
	s.mu.RLock()
	_, hasClient := s.gabpClients["adventure"]
	s.mu.RUnlock()
	if hasClient {
		t.Fatal("the unbound client must be closed and removed, never left to mirror tools")
	}
	if len(s.getGameSpecificTools("adventure")) != 0 {
		t.Fatal("no tools may be mirrored without a persisted attachment binding")
	}
}

// Rollback after a lost stillCurrent race must target EXACTLY the record
// the loser created — never whichever record is current by then.
func TestAttachmentRollbackTargetsExactRecord(t *testing.T) {
	s := newProfiledServer(t)

	seedClaimEndpointForTest(t, s.configDir, "adventure", 40100, "launch-token")

	published := false
	_, errA := attachForTest(s, "adventure", 40100, "launch-token", func() bool {
		if !published {
			published = true
			// Connection B publishes while A is deciding whether it is
			// still current.
			if _, berr := attachForTest(s, "adventure", 40100, "launch-token", func() bool { return true }); berr != nil {
				t.Errorf("B's publication failed: %v", berr)
			}
		}
		return false // A is no longer current
	})
	if !errors.Is(errA, errAttachmentSuperseded) {
		t.Fatalf("A must report superseded, got %v", errA)
	}

	claim, err := process.LoadRuntimeState("adventure", s.configDir)
	if err != nil || claim == nil || claim.Attachment == nil {
		t.Fatalf("B's persisted record must survive A's rollback: %+v %v", claim, err)
	}
	s.mu.RLock()
	ref, hasRef := s.bridgeAttachments["adventure"]
	s.mu.RUnlock()
	if !hasRef || ref.connectionID != claim.Attachment.ConnectionID {
		t.Fatalf("B's in-memory binding must survive A's rollback: ref=%+v record=%+v", ref, claim.Attachment)
	}
}

// A legacy migration whose claim is replaced during handshake validation
// must fail terminally: the client closes, nothing is persisted into the
// successor, and no tools are exposed.
func TestLegacyMigrationPersistFailureIsTerminal(t *testing.T) {
	s := newLegacyServer(t, "DirectPath")
	writeLegacyClaim(t, "oldgame", s.configDir, 0, "")

	var successor process.RuntimeState
	addr, _ := fakeGABPServerWithHello(t, "oldgame", func() {
		// The normalized claim is replaced while the candidate validates.
		_ = process.RemoveRuntimeState("oldgame", s.configDir)
		successor = process.NewRuntimeState(process.LaunchSpec{GameId: "oldgame", Mode: "DirectPath", PathOrId: "/opt/game"}, process.RuntimeStateStatusStarting)
		successor.SpawnState = process.SpawnStateSpawned
		if err := process.ClaimRuntimeState("oldgame", s.configDir, successor); err != nil {
			t.Errorf("mid-handshake reclaim failed: %v", err)
		}
	})
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	if _, err := config.WriteBridgeJSONWithEndpoint("oldgame", s.configDir, port, "legacy-token"); err != nil {
		t.Fatal(err)
	}

	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "oldgame", "timeout": 5})
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("a migration that lost its fence must fail, not report connected: %s", raw)
	}
	if s.hasLiveGABPClient("oldgame") {
		t.Fatal("the migration client must be closed on a fenced-out persist")
	}
	claim, err := process.LoadRuntimeState("oldgame", s.configDir)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if claim.LaunchID != successor.LaunchID || claim.Endpoint != nil || claim.Attachment != nil {
		t.Fatalf("a legacy bridge must never be persisted under a successor claim: %+v", claim)
	}
	if len(s.getGameSpecificTools("oldgame")) != 0 {
		t.Fatal("no tools may be exposed for a failed migration")
	}
}

// A Stage 4 unobserved outcome is emitted only when its fenced completion
// lands; a claim replaced during the start window reports supersession.
func TestStartUnobservedSupersededDuringStage4(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep binary as the launcher stand-in")
	}
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"steamish": {ID: "steamish", Name: "S", LaunchMode: "SteamAppId", Target: "123456", StopProcessName: "steamish-workload"},
		},
		Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{ProcessStartSeconds: 1}},
	}, 0, 0)

	restoreLauncher := process.SetLaunchCommandFactoriesForTesting(
		func(target string) (string, []string) { return "/bin/sleep", []string{"5"} },
		nil,
	)
	defer restoreLauncher()

	var replacement process.RuntimeState
	calls := 0
	restoreFinder := process.SetFindProcessesByNameForTesting(func(name string) ([]int, error) {
		if name != "steamish-workload" {
			return nil, nil
		}
		calls++
		if calls == 1 {
			return nil, nil // the pre-start scan: clean
		}
		if replacement.LaunchID == "" {
			// A successor replaces the claim during the Stage 4 window.
			_ = process.RemoveRuntimeState("steamish", dir)
			replacement = process.NewRuntimeState(process.LaunchSpec{GameId: "steamish", Mode: "SteamAppId", PathOrId: "123456"}, process.RuntimeStateStatusStarting)
			replacement.SpawnState = process.SpawnStateSpawned
			if err := process.ClaimRuntimeState("steamish", dir, replacement); err != nil {
				t.Errorf("mid-window reclaim failed: %v", err)
			}
		}
		return nil, &fakeScanError{} // unknown evidence: the unobserved path
	})
	defer restoreFinder()

	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "steamish", "timeout": 1})
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("a superseded Stage 4 completion must not report a stable outcome: %s", raw)
	}
	if structured["code"] == "unobserved" {
		t.Fatalf("unobserved may only be reported after its transition lands: %s", raw)
	}
	if !strings.Contains(raw, "superseded") {
		t.Fatalf("the supersession must be surfaced: %s", raw)
	}
	claim, _ := process.LoadRuntimeState("steamish", dir)
	if claim == nil || claim.LaunchID != replacement.LaunchID {
		t.Fatalf("the successor claim must survive untouched: %+v", claim)
	}
	if claim.Operation != nil || claim.LastActionResult != nil {
		t.Fatalf("the successor must carry no writes from the dead start: %+v", claim)
	}
}

type fakeScanError struct{}

func (*fakeScanError) Error() string { return "process table unavailable" }

// A delivery report from connection A must be persisted under A's exact
// connectionID, even when connection B replaced the attachment before A
// reports (review round 9): the attribution is carried, not reacquired.
func TestDeliveryReportAttributedToProducingConnection(t *testing.T) {
	s := newProfiledServer(t)
	cwd := t.TempDir()
	digests, err := process.ComputeContextDigests([]string{"-x"}, cwd, false,
		map[string]string{"GABS_GAME_ID": "adventure"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := process.LaunchSpec{GameId: "adventure", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.GamePID = os.Getpid()
	st.Endpoint = &process.RuntimeEndpoint{Port: 40200, Token: "launch-token"}
	st.ContextDigests = digests
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}

	// Connection A publishes and captures its exact ref.
	refA, errA := attachForTest(s, "adventure", 40200, "launch-token", func() bool { return true })
	if errA != nil {
		t.Fatalf("A publish failed: %v", errA)
	}
	// Connection B replaces the attachment.
	refB, errB := attachForTest(s, "adventure", 40200, "launch-token", func() bool { return true })
	if errB != nil {
		t.Fatalf("B publish failed: %v", errB)
	}
	if refA.connectionID == refB.connectionID {
		t.Fatal("A and B must have distinct connection identities")
	}

	// A reports LATE, carrying its own ref: the write is fenced to A's
	// connectionID, which is no longer current — so it must NOT overwrite
	// B's verdict.
	s.recordContextDelivery("adventure", refA, &gabp.ObservedContext{
		Argv: []string{"bin", "-x"}, Cwd: cwd,
		EnvValues: map[string]string{"GABS_GAME_ID": "adventure"},
	})
	claim, _ := process.LoadRuntimeState("adventure", s.configDir)
	if claim.Attachment == nil || claim.Attachment.ConnectionID != refB.connectionID {
		t.Fatalf("B must still own the attachment: %+v", claim.Attachment)
	}
	if claim.ContextDelivery != nil {
		t.Fatalf("A's late report must not persist under B's connection: %+v", claim.ContextDelivery)
	}

	// B's own report DOES persist — proving the mechanism works when the
	// identity is current.
	s.recordContextDelivery("adventure", refB, &gabp.ObservedContext{
		Argv: []string{"bin", "-x"}, Cwd: cwd,
		EnvValues: map[string]string{"GABS_GAME_ID": "adventure"},
	})
	claim, _ = process.LoadRuntimeState("adventure", s.configDir)
	if claim.ContextDelivery == nil || claim.ContextDelivery.Overall != process.DeliveryVerified {
		t.Fatalf("B's own current report must persist verified: %+v", claim.ContextDelivery)
	}
}

// A pending, not-yet-authenticated client must never satisfy a
// claim-bound lookup (review round 9): combining an unauthenticated
// client B with attachment reference A must not report B as bound.
func TestPendingClientIsNotClaimBound(t *testing.T) {
	s := newProfiledServer(t)
	seedClaimEndpointForTest(t, s.configDir, "adventure", 40300, "launch-token")

	// Simulate a dialed-but-unauthenticated client sitting in the map with
	// no published binding (the reconnect-B window).
	pending := gabp.NewClient(s.log)
	s.mu.Lock()
	s.gabpClients["adventure"] = pending
	s.mu.Unlock()

	if client, _ := s.claimBoundClient("adventure"); client != nil {
		t.Fatal("an unauthenticated client with no published binding must never be claim-bound")
	}
	if s.hasLiveGABPClient("adventure") {
		t.Fatal("GABP evidence requires a bound authenticated client")
	}
}

// A default-workingDir SteamManaged launch must digest the RESOLVED app
// directory, not GABS's own cwd (review round 9): materialization pins the
// executable and working directory BEFORE digesting.
func TestSteamManagedDefaultCwdDigestsResolvedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix app directory fixture")
	}
	appDir := t.TempDir()
	exe := appDir + "/game"
	restore := process.SetSteamResolveAppForTesting(func(appID string) (process.SteamApp, error) {
		return process.SteamApp{Executable: exe, WorkingDir: appDir}, nil
	})
	defer restore()

	controller := process.NewController()
	if err := controller.Configure(process.LaunchSpec{
		GameId: "steamgame", Mode: "SteamManaged", PathOrId: "123456",
		// No WorkingDir configured: the resolved app dir must be used.
	}); err != nil {
		t.Fatal(err)
	}
	spawnExe, spawnCwd, err := controller.MaterializeSpawnSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spawnExe != exe {
		t.Fatalf("the resolved executable must be pinned, got %q", spawnExe)
	}
	canonicalApp, err := process.CanonicalizeCwd(appDir)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSpawn, err := process.CanonicalizeCwd(spawnCwd)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalSpawn != canonicalApp {
		t.Fatalf("the materialized cwd must be the resolved app dir, not GABS's cwd: got %q want %q", canonicalSpawn, canonicalApp)
	}

	// Digesting that materialized cwd and reporting the resolved app dir
	// must verify — never a false mismatch against GABS's own directory.
	d, err := process.ComputeContextDigests([]string{"-x"}, spawnCwd, false, map[string]string{"GABS_GAME_ID": "steamgame"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	del := process.EvaluateContextDelivery(d, &process.ObservedContext{Cwd: appDir})
	if del.Channels[process.DeliveryChannelCwd] != process.DeliveryVerified {
		t.Fatalf("the resolved app dir must verify: %+v", del)
	}
}

// A start whose promote/endpoint transition loses its fence to a successor
// must emit a STABLE code (design/10), never an unclassified error.
func TestStartSupersededDuringStage4EmitsStableCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep binary")
	}
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"steamish": {ID: "steamish", Name: "S", LaunchMode: "SteamAppId", Target: "123456", StopProcessName: "steamish-workload"},
		},
		Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{ProcessStartSeconds: 1}},
	}, 0, 0)

	restoreLauncher := process.SetLaunchCommandFactoriesForTesting(
		func(target string) (string, []string) { return "/bin/sleep", []string{"5"} }, nil)
	defer restoreLauncher()

	var successor process.RuntimeState
	calls := 0
	restoreFinder := process.SetFindProcessesByNameForTesting(func(name string) ([]int, error) {
		if name != "steamish-workload" {
			return nil, nil
		}
		calls++
		if calls == 1 {
			return nil, nil // pre-start scan: clean
		}
		if successor.LaunchID == "" {
			// Replace the claim with an active successor while Stage 4 runs.
			_ = process.RemoveRuntimeState("steamish", dir)
			successor = process.NewRuntimeState(process.LaunchSpec{GameId: "steamish", Mode: "SteamAppId", PathOrId: "123456"}, process.RuntimeStateStatusRunning)
			successor.Phase = process.PhaseActive
			successor.SpawnState = process.SpawnStateSpawned
			if err := process.ClaimRuntimeState("steamish", dir, successor); err != nil {
				t.Errorf("reclaim failed: %v", err)
			}
		}
		return []int{os.Getpid()}, nil // running: promote path
	})
	defer restoreFinder()

	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "steamish", "timeout": 1})
	code, _ := structured["code"].(string)
	stable := map[string]bool{
		"already_running": true, "operation_in_progress": true, "blocked_unknown_state": true,
	}
	if !stable[code] {
		t.Fatalf("a superseded Stage 4 completion must carry a stable code, got %q: %v", code, structured)
	}
}
