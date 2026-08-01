package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// A traversal identifier from the MCP surface must resolve to not-found and
// never reach a filesystem op outside the config base (round-19 P1): the
// runtime-only fallback treats an unsafe ID like "no claim".
func TestRuntimeOnlyStatusRejectsTraversalID(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	victimDir := filepath.Join(root, "victim")
	if err := os.MkdirAll(victimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	victimClaim := filepath.Join(victimDir, "runtime.json")
	if err := os.WriteFile(victimClaim, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(base)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("s", "games.status", "../victim")))
	if !strings.Contains(statusText, "not found") {
		t.Fatalf("a traversal ID must resolve to not-found, got: %s", statusText)
	}
	// stop/kill of a traversal ID must not remove the sibling either.
	server.HandleMessage(toolCallMessage("k", "games.stop", "../victim"))
	server.HandleMessage(toolCallMessage("k", "games.kill", "../victim"))
	if _, err := os.Stat(victimClaim); err != nil {
		t.Fatalf("a traversal ID reached a removal outside the config base: %v", err)
	}
}

// seedRuntimeOnlyClaim writes a runtime.json for a game that is NOT in config —
// a launch whose config entry was edited away (design/07).
func seedRuntimeOnlyClaim(t *testing.T, tmpDir, gameID string) {
	t.Helper()
	spec := process.LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/" + gameID}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	if err := process.ClaimRuntimeState(gameID, tmpDir, st); err != nil {
		t.Fatalf("seed runtime-only claim: %v", err)
	}
}

func noArgStatusMessage(id string) *Message {
	return &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"` + id + `"`),
		Params: map[string]interface{}{
			"name":      "games.status",
			"arguments": map[string]interface{}{}, // no gameId → all games
		},
	}
}

// No-argument games_status unions configured entries with persisted runtime
// claims, reporting configured:false and the persisted phase (design/07:63-68,
// design/10:31) so a fresh agent can find a launch whose config entry was
// edited away.
func TestNoArgStatusUnionsRuntimeOnlyClaim(t *testing.T) {
	tmpDir := t.TempDir()
	seedRuntimeOnlyClaim(t, tmpDir, "ghost-game")

	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	statusText := marshalMessage(t, server.HandleMessage(noArgStatusMessage("status-all")))
	if !strings.Contains(statusText, "ghost-game") {
		t.Fatalf("no-arg status must surface the runtime-only claim, got: %s", statusText)
	}
	if !strings.Contains(statusText, `"configured":false`) {
		t.Fatalf("the runtime-only claim must report configured:false, got: %s", statusText)
	}
	if !strings.Contains(statusText, `"phase":"active"`) {
		t.Fatalf("the runtime-only claim must report its persisted phase, got: %s", statusText)
	}
}

// A removed-but-claimed game stays addressable by ID: games_status <id> returns
// the claim's status (configured:false), never a not-found error (design/07:62).
func TestSingleIDStatusAddressesRuntimeOnlyClaim(t *testing.T) {
	tmpDir := t.TempDir()
	seedRuntimeOnlyClaim(t, tmpDir, "ghost-game")

	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("status-ghost", "games.status", "ghost-game")))
	if strings.Contains(statusText, "not found") || strings.Contains(statusText, `"isError":true`) {
		t.Fatalf("a removed-but-claimed game must stay addressable, got: %s", statusText)
	}
	if !strings.Contains(statusText, `"configured":false`) {
		t.Fatalf("single-ID status of a runtime-only claim must report configured:false, got: %s", statusText)
	}
}

// A slash-containing game ID (design-legal) must survive the whole runtime-only
// arc: no-arg discovery, single-ID status, and stop addressability — the storage
// key decodes back to the exact ID (round-19 P1 regression fix).
func TestRuntimeOnlySlashIDLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	seedRuntimeOnlyClaim(t, tmpDir, "factory/old")

	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	statusAll := marshalMessage(t, server.HandleMessage(noArgStatusMessage("all")))
	if !strings.Contains(statusAll, "factory/old") {
		t.Fatalf("no-arg status must discover the slash-ID runtime-only claim: %s", statusAll)
	}
	statusOne := marshalMessage(t, server.HandleMessage(toolCallMessage("one", "games.status", "factory/old")))
	if strings.Contains(statusOne, "not found") || strings.Contains(statusOne, `"isError":true`) {
		t.Fatalf("a slash ID must be addressable for status: %s", statusOne)
	}
	stopText := marshalMessage(t, server.HandleMessage(toolCallMessage("stop", "games.stop", "factory/old")))
	if strings.Contains(stopText, "not found") {
		t.Fatalf("a slash ID must be addressable for stop: %s", stopText)
	}
}

// Runtime-only claims must go through the SAME concurrent status pool as
// configured games (design/10, round-19 P2): three removed claims with slow
// pinned status hooks must total ~one probe interval, not the serial sum.
func TestMultiGameStatusProbesRunConcurrentlyRuntimeOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep binary as a slow status hook")
	}
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	for _, id := range []string{"ghost-a", "ghost-b", "ghost-c"} {
		spec := process.LaunchSpec{GameId: id, Mode: "DirectPath", PathOrId: "/bin/sleep"}
		st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
		st.Phase = process.PhaseActive
		st.SpawnState = process.SpawnStateSpawned
		st.Lifecycle = &launch.ResolvedLifecycle{Status: &launch.ResolvedHook{
			Command: "/bin/sleep", Args: []string{"30"}, TimeoutSeconds: 2,
			RunningExitCodes: []int{0}, StoppedExitCodes: []int{1},
		}}
		if err := process.ClaimRuntimeState(id, dir, st); err != nil {
			t.Fatal(err)
		}
	}
	// EMPTY config: every row is a runtime-only claim.
	s.RegisterGameManagementTools(&config.GamesConfig{Version: "1.0", Games: map[string]config.GameConfig{}}, 0, 0)

	startAt := time.Now()
	raw, _ := callTool(t, s, "games.status", map[string]interface{}{})
	elapsed := time.Since(startAt)
	if elapsed > 4500*time.Millisecond {
		t.Fatalf("runtime-only probes must run concurrently, took %v: %s", elapsed, raw)
	}
	for _, id := range []string{"ghost-a", "ghost-b", "ghost-c"} {
		if !strings.Contains(raw, id) {
			t.Fatalf("runtime-only claim %s must be surfaced: %s", id, raw)
		}
	}
}

// A corrupt runtime-only claim must render as unknown + the repair path, not a
// silent unknown that only offers stop/kill (which return blocked_unknown_state)
// — T-RT "corrupt runtime.json → unknown + repair path".
func TestNoArgStatusSurfacesRepairForCorruptClaim(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := filepath.Join(tmpDir, "corrupt-game")
	if err := os.MkdirAll(gameDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "runtime.json"), []byte("{ not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	statusText := marshalMessage(t, server.HandleMessage(noArgStatusMessage("status-all")))
	if !strings.Contains(statusText, "repair corrupt-game --forget-runtime") {
		t.Fatalf("a corrupt runtime-only claim must surface the repair command, got: %s", statusText)
	}
	if !strings.Contains(statusText, `"status":"unknown"`) {
		t.Fatalf("a corrupt claim must be unknown, got: %s", statusText)
	}
}

// A removed-but-claimed game must be stoppable by ID (design/07:66, "so a fresh
// agent can stop it"): games_stop <id> resolves the claim's pinned lifecycle,
// never a not-found error.
func TestStopAddressesRuntimeOnlyClaim(t *testing.T) {
	tmpDir := t.TempDir()
	seedRuntimeOnlyClaim(t, tmpDir, "ghost-game")

	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	stopText := marshalMessage(t, server.HandleMessage(toolCallMessage("stop-ghost", "games.stop", "ghost-game")))
	if strings.Contains(stopText, "not found") {
		t.Fatalf("a removed-but-claimed game must be addressable for stop, got: %s", stopText)
	}
}
