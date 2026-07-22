package mcp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
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
	errA := s.recordBridgeAttachment("adventure", 40100, "launch-token", func() bool {
		if !published {
			published = true
			// Connection B publishes while A is deciding whether it is
			// still current.
			if berr := s.recordBridgeAttachment("adventure", 40100, "launch-token", func() bool { return true }); berr != nil {
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
	s := NewServerForTesting(util.NewLogger("error"))
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
