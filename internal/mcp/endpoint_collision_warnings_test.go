package mcp

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestOccupiedEndpointCacheCarriesStartWarnings pins warning preservation on
// the endpoint-cache-collision result: the special-case branch returns before
// the generic endpoint_unavailable rendering, so it must attach the probe
// warnings the accepted attempt already earned — evidence the operator needs
// alongside the collision.
func TestOccupiedEndpointCacheCarriesStartWarnings(t *testing.T) {
	tmpDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve endpoint port: %v", err)
	}
	defer listener.Close()

	// The profile's status hook exits 3 — neither running nor stopped — so
	// the pre-start probe verdict is unknown and Stage 2 records the
	// unprobeable-profile warning before endpoint preparation collides.
	timeout := 5
	game := config.GameConfig{
		ID:             "occupied-cache",
		Name:           "Occupied Cache",
		LaunchMode:     "DirectPath",
		Target:         "/bin/sleep",
		Args:           []string{"30"},
		DefaultProfile: "p",
		Profiles: map[string]config.ProfileConfig{
			"p": {Lifecycle: &config.LifecycleConfig{
				Status: &config.HookConfig{Command: "/bin/sh", Args: []string{"-c", "exit 3"}, TimeoutSeconds: &timeout},
			}},
		},
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}
	if _, err := config.WriteBridgeJSONWithEndpoint(game.ID, tmpDir, listener.Addr().(*net.TCPAddr).Port, "existing-token"); err != nil {
		t.Fatalf("failed to seed endpoint cache: %v", err)
	}

	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	startText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-occupied-with-warnings"`),
		Params: map[string]interface{}{
			"name":      "games.start",
			"arguments": map[string]interface{}{"gameId": game.ID},
		},
	}))

	if !strings.Contains(startText, `"status":"endpoint_cache_in_use"`) {
		t.Fatalf("expected the endpoint-cache collision, got: %s", startText)
	}
	if !strings.Contains(startText, "startWarnings") || !strings.Contains(startText, "could not probe") {
		t.Fatalf("the collision result must carry the probe warnings the attempt earned, got: %s", startText)
	}
	// The warnings must also reach the TEXT content: some MCP clients never
	// surface structured content.
	if !strings.Contains(startText, "Warnings:") {
		t.Fatalf("the probe warnings must appear in the textual content too, got: %s", startText)
	}
}
