package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

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
