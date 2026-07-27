package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestInProcessAlreadyActiveCarriesStableCode pins the exhaustive-outcome
// contract on the in-process fast path: when the persisted claim is lost
// while this server still tracks a live controller, the duplicate start must
// render the stable already_running refusal — code, causeClass, and track
// record — not an uncodable informational blob.
func TestInProcessAlreadyActiveCarriesStableCode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GABSTEST_HELPER_PROCESS", "1")

	game := helperProcessGameConfig(t, "inproc-active")
	// Two declared profiles let the duplicate request ask for a DIFFERENT
	// profile than the one that launched: the republished snapshot must not
	// stamp the new request's context onto the old process.
	game.DefaultProfile = "a"
	game.Profiles = map[string]config.ProfileConfig{"a": {}, "b": {}}
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games:   map[string]config.GameConfig{game.ID: game},
	}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 0, 0)
	t.Cleanup(func() {
		server.HandleMessage(toolCallMessage("cleanup", "games.kill", game.ID))
	})

	first := marshalMessage(t, server.HandleMessage(toolCallMessage("s1", "games.start", game.ID)))
	if strings.Contains(first, `"isError":true`) {
		t.Fatalf("first start must succeed: %s", first)
	}

	// Lose the claim while the in-process registry still tracks the live
	// controller: the duplicate start (for the OTHER profile) now takes the
	// fast path.
	if err := process.RemoveRuntimeState(game.ID, tmpDir); err != nil {
		t.Fatal(err)
	}

	second := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"s2"`),
		Params: map[string]interface{}{
			"name":      "games.start",
			"arguments": map[string]interface{}{"gameId": game.ID, "profile": "b"},
		},
	}))
	if !strings.Contains(second, `"code":"already_running"`) {
		t.Fatalf("the in-process fast path must carry the stable already_running code: %s", second)
	}
	if !strings.Contains(second, "causeClass") {
		t.Fatalf("the refusal must carry attribution like every stable start outcome: %s", second)
	}
	if strings.Contains(second, `"isError":true`) {
		t.Fatalf("already_running stays informational: %s", second)
	}

	// Cross-process coordination must be restored, not discarded — and the
	// snapshot must be explicitly UNATTRIBUTED: the second request's profile,
	// hooks, and history identity describe the request, not the old process.
	claim, err := process.LoadRuntimeState(game.ID, tmpDir)
	if err != nil || claim == nil {
		t.Fatalf("already_running must leave a claim behind, got claim=%v err=%v", claim, err)
	}
	if claim.Phase != process.PhaseActive || claim.GamePID <= 0 {
		t.Fatalf("the republished claim must describe the live controller: %+v", claim)
	}
	if claim.Profile != "" || claim.ObservedProfile != process.ObservedProfileUnknown {
		t.Fatalf("the snapshot must not attribute a profile (requested %q must not leak): %+v", "b", claim)
	}
	if claim.Lifecycle != nil || claim.HistoryContextHash != "" {
		t.Fatalf("the snapshot must not carry the new request's hooks or history identity: %+v", claim)
	}
	if claim.Source != process.SourceExternal {
		t.Fatalf("the snapshot must be explicitly unattributed (external-source semantics): %+v", claim)
	}
}

// TestInProcessRepublishFailureBlocks pins the failure side: when the
// truthful snapshot cannot be durably published, the response must be a
// blocking unknown-state refusal — an informational already_running over a
// missing claim would leave the workload invisible to other GABS processes.
func TestInProcessRepublishFailureBlocks(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GABSTEST_HELPER_PROCESS", "1")

	game := helperProcessGameConfig(t, "inproc-outage")
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games:   map[string]config.GameConfig{game.ID: game},
	}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 0, 0)
	t.Cleanup(func() {
		server.HandleMessage(toolCallMessage("cleanup", "games.kill", game.ID))
	})

	first := marshalMessage(t, server.HandleMessage(toolCallMessage("s1", "games.start", game.ID)))
	if strings.Contains(first, `"isError":true`) {
		t.Fatalf("first start must succeed: %s", first)
	}
	if err := process.RemoveRuntimeState(game.ID, tmpDir); err != nil {
		t.Fatal(err)
	}

	// The duplicate start writes the claim twice: the gate's post-probe
	// re-stamp, then the fast path's snapshot publish. Fail the second write
	// to simulate an outage exactly at the republish.
	calls := 0
	restore := process.SetSaveRuntimeStateFailHookForTesting(func() error {
		calls++
		if calls >= 2 {
			return errors.New("simulated republish outage")
		}
		return nil
	})
	defer restore()

	second := marshalMessage(t, server.HandleMessage(toolCallMessage("s2", "games.start", game.ID)))
	if strings.Contains(second, `"code":"already_running"`) {
		t.Fatalf("a failed republish must not report an unqualified already_running: %s", second)
	}
	if !strings.Contains(second, "blocked_unknown_state") {
		t.Fatalf("a failed republish must block on unknown state: %s", second)
	}
}
