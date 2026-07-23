package mcp

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// writeLegacyClaim persists a pre-profile (schema-0) runtime record owned
// by this process, like pre-upgrade GABS versions produced.
func writeLegacyClaim(t *testing.T, gameID, dir string, gamePID int, stopName string) {
	t.Helper()
	st := process.RuntimeState{
		GameID:          gameID,
		Status:          process.RuntimeStateStatusRunning,
		OwnerPID:        os.Getpid(),
		GamePID:         gamePID,
		StopProcessName: stopName,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := process.SaveRuntimeState(gameID, dir, st); err != nil {
		t.Fatal(err)
	}
}

func newLegacyServer(t *testing.T, mode string) *Server {
	t.Helper()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"oldgame": {ID: "oldgame", Name: "Old Game", LaunchMode: mode, Target: "123456"},
		},
	}, 0, 0)
	return s
}

func TestLegacyStopNormalizesThenRoutesPipeline(t *testing.T) {
	s := newLegacyServer(t, "SteamAppId")
	writeLegacyClaim(t, "oldgame", s.configDir, 0, "")

	raw, structured := callTool(t, s, "games.stop", map[string]interface{}{"gameId": "oldgame"})
	// No PID, no name, no hooks: the pipeline refuses — proving the stop
	// went through normalization + the design/06 path, not the legacy one.
	if structured["code"] != "stop_unsupported" {
		t.Fatalf("legacy claim stop must route through the pipeline after normalization: %s", raw)
	}

	claim, err := process.LoadRuntimeState("oldgame", s.configDir)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if claim.SchemaVersion != process.RuntimeSchemaVersion || !claim.NormalizedFromLegacy {
		t.Fatalf("the first lifecycle touch must fully normalize: %+v", claim)
	}
	if claim.Phase != process.PhaseActive || claim.LaunchID == "" || claim.PIDRole != process.PIDRoleHelper {
		t.Fatalf("normalization contract violated: %+v", claim)
	}
}

func TestGamesConnectMigratesLegacyEndpointExactlyOnce(t *testing.T) {
	s := newLegacyServer(t, "DirectPath")
	writeLegacyClaim(t, "oldgame", s.configDir, 0, "")

	addr, _ := fakeGABPServer(t, "oldgame")
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	if _, err := config.WriteBridgeJSONWithEndpoint("oldgame", s.configDir, port, "legacy-token"); err != nil {
		t.Fatal(err)
	}

	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "oldgame", "timeout": 5})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("legacy migration connect failed: %s", raw)
	}
	t.Cleanup(func() { s.CleanupGABPConnection("oldgame") })

	claim, err := process.LoadRuntimeState("oldgame", s.configDir)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if !claim.NormalizedFromLegacy || claim.SchemaVersion != process.RuntimeSchemaVersion {
		t.Fatalf("connect must normalize the legacy claim: %+v", claim)
	}
	if claim.Endpoint == nil || claim.Endpoint.Port != port || claim.Endpoint.Token != "legacy-token" {
		t.Fatalf("the validated endpoint must migrate into the claim: %+v", claim.Endpoint)
	}
	if claim.Attachment == nil {
		t.Fatalf("the migrated connection must publish its attachment record: %+v", claim)
	}

	// Exactly once: corrupt bridge.json; the second connect must succeed
	// through the claim's migrated endpoint without re-reading the file.
	if _, err := config.WriteBridgeJSONWithEndpoint("oldgame", s.configDir, 1, "bogus"); err != nil {
		t.Fatal(err)
	}
	s.CleanupGABPConnection("oldgame")

	raw, _ = callTool(t, s, "games.connect", map[string]interface{}{"gameId": "oldgame", "timeout": 5})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("reconnect must use the migrated claim endpoint, not bridge.json: %s", raw)
	}
	claim, _ = process.LoadRuntimeState("oldgame", s.configDir)
	if claim.Endpoint == nil || claim.Endpoint.Port != port || claim.Endpoint.Token != "legacy-token" {
		t.Fatalf("the migrated endpoint must be stable: %+v", claim.Endpoint)
	}
}

func TestLegacyMigrationDoesNotRetryAfterFailedValidation(t *testing.T) {
	s := newLegacyServer(t, "DirectPath")
	writeLegacyClaim(t, "oldgame", s.configDir, 0, "")

	// The candidate captured on the first touch points nowhere.
	deadPort := unusedLocalPort(t)
	if _, err := config.WriteBridgeJSONWithEndpoint("oldgame", s.configDir, deadPort, "legacy-token"); err != nil {
		t.Fatal(err)
	}

	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "oldgame", "timeout": 1})
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("validation against a dead endpoint must fail: %s", raw)
	}
	claim, err := process.LoadRuntimeState("oldgame", s.configDir)
	if err != nil || claim == nil || !claim.NormalizedFromLegacy || claim.Endpoint != nil {
		t.Fatalf("the failed validation leaves a normalized claim without an endpoint: %+v %v", claim, err)
	}

	// The migration window is the marker-absent first touch, exactly once:
	// even a now-live bridge.json is never reread (design/07, T-RT).
	addr, _ := fakeGABPServer(t, "oldgame")
	parts := strings.Split(addr, ":")
	livePort := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &livePort)
	if _, err := config.WriteBridgeJSONWithEndpoint("oldgame", s.configDir, livePort, "legacy-token"); err != nil {
		t.Fatal(err)
	}

	raw, _ = callTool(t, s, "games.connect", map[string]interface{}{"gameId": "oldgame", "timeout": 2})
	if !strings.Contains(raw, `"isError":true`) || !strings.Contains(raw, "no attachable endpoint") {
		t.Fatalf("a marker-stamped claim must never reenter the file path: %s", raw)
	}
	if s.hasLiveGABPClient("oldgame") {
		t.Fatal("no connection may be established through the diagnostic file after the migration window")
	}
}

func TestFreshPreEndpointClaimNeverReadsBridgeFile(t *testing.T) {
	s := newLegacyServer(t, "DirectPath")

	addr, _ := fakeGABPServer(t, "oldgame")
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	if _, err := config.WriteBridgeJSONWithEndpoint("oldgame", s.configDir, port, "live-token"); err != nil {
		t.Fatal(err)
	}

	// A marker-stamped claim between creation and endpoint allocation.
	spec := process.LaunchSpec{GameId: "oldgame", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusStarting)
	st.OwnerInstanceID = s.instanceID
	if err := process.ClaimRuntimeState("oldgame", s.configDir, st); err != nil {
		t.Fatal(err)
	}

	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "oldgame", "timeout": 2})
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("a fresh pre-endpoint claim must never enter the bridge.json migration path: %s", raw)
	}
	if !strings.Contains(raw, "no attachable endpoint") {
		t.Fatalf("the refusal must explain the missing endpoint: %s", raw)
	}
	if s.hasLiveGABPClient("oldgame") {
		t.Fatal("no connection may be established through the diagnostic file")
	}
}

func TestExternalSnapshotConnectReportsAttachmentUnavailable(t *testing.T) {
	s := newLegacyServer(t, "DirectPath")

	addr, _ := fakeGABPServer(t, "oldgame")
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	if _, err := config.WriteBridgeJSONWithEndpoint("oldgame", s.configDir, port, "live-token"); err != nil {
		t.Fatal(err)
	}

	spec := process.LaunchSpec{GameId: "oldgame", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.OwnerPID = 0
	st.Phase = process.PhaseActive
	st.Source = process.SourceExternal
	st.SpawnState = ""
	st.ObservedProfile = "unknown"
	st.AppliedInputsState = process.AppliedInputsStateUnavailable
	st.StopProcessName = "external-workload"
	if err := process.ClaimRuntimeState("oldgame", s.configDir, st); err != nil {
		t.Fatal(err)
	}
	restore := process.SetFindProcessesByNameForTesting(func(name string) ([]int, error) {
		if name == "external-workload" {
			return []int{os.Getpid()}, nil // the external instance is alive
		}
		return nil, nil
	})
	defer restore()

	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "oldgame", "timeout": 2})
	if !strings.Contains(raw, `"isError":true`) || !strings.Contains(raw, "attachment unavailable") {
		t.Fatalf("external snapshots report attachment unavailable (design/07): %s", raw)
	}
	claim, _ := process.LoadRuntimeState("oldgame", s.configDir)
	if claim == nil || claim.Source != process.SourceExternal || claim.Endpoint != nil {
		t.Fatalf("the external snapshot must be untouched: %+v", claim)
	}
}

func TestDegradedLegacyStatusBeforeNormalization(t *testing.T) {
	s := newLegacyServer(t, "DirectPath")
	writeLegacyClaim(t, "oldgame", s.configDir, os.Getpid(), "")

	raw, structured := callTool(t, s, "games.status", map[string]interface{}{"gameId": "oldgame"})
	if _, has := structured["phase"]; has {
		t.Fatalf("degraded legacy status must not render newer-schema-only fields: %s", raw)
	}
	if _, has := structured["operation"]; has {
		t.Fatalf("degraded legacy status must not render operations: %s", raw)
	}
	if _, has := structured["activeConfigRevision"]; has {
		t.Fatalf("legacy claims carry no config revision: %s", raw)
	}
	claim, _ := process.LoadRuntimeState("oldgame", s.configDir)
	if claim == nil || claim.SchemaVersion != 0 || claim.NormalizedFromLegacy {
		t.Fatalf("read-only status must never normalize (design/07): %+v", claim)
	}
}
