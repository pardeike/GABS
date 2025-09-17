package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestDoubleCheckGameStatusFix validates that the games.status tool no longer
// calls checkGameStatus twice, which was causing a potential deadlock
func TestDoubleCheckGameStatusFix(t *testing.T) {
	// Create temp config directory
	tmpDir, err := os.MkdirTemp("", "gabs-double-status-fix")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create RimWorld config - the specific game that was hanging
	rimworldGame := config.GameConfig{
		ID:              "rimworld",
		Name:            "RimWorld", 
		LaunchMode:      "SteamAppId",
		Target:          "294100",
		StopProcessName: "RimWorldWin64.exe",
	}
	
	gamesConfig := &config.GamesConfig{
		Games: make(map[string]config.GameConfig),
	}
	gamesConfig.AddGame(rimworldGame)

	// Create MCP server
	log := util.NewLogger("info")
	server := NewServerForTesting(log)
	server.SetConfigDir(tmpDir)

	// Register game management tools
	backoffMin := 100 * time.Millisecond
	backoffMax := 1 * time.Second
	server.RegisterGameManagementTools(gamesConfig, backoffMin, backoffMax)

	t.Run("games.status should not hang on rimworld", func(t *testing.T) {
		// This tests the exact problem reported: gabs.games_status({"gameId":"rimworld"}) hangs
		statusMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"rimworld-status"`),
			Params: map[string]interface{}{
				"name": "games.status",
				"arguments": map[string]interface{}{
					"gameId": "rimworld",
				},
			},
		}

		// Use timeout to detect hanging - the original issue was a complete hang
		done := make(chan *Message, 1)
		go func() {
			response := server.HandleMessage(statusMsg)
			done <- response
		}()

		select {
		case response := <-done:
			respBytes, _ := json.Marshal(response)
			responseStr := string(respBytes)
			t.Logf("✅ games.status completed successfully: %s", responseStr)
			
			// Verify it returns proper response
			if strings.Contains(responseStr, `"error"`) {
				t.Errorf("games.status returned error: %s", responseStr)
			}
			
			// Should contain RimWorld info
			if !strings.Contains(responseStr, "rimworld") || !strings.Contains(responseStr, "RimWorld") {
				t.Errorf("Expected rimworld game info in response: %s", responseStr)
			}
			
		case <-time.After(2 * time.Second):
			t.Fatal("❌ games.status call timed out - the deadlock issue still exists!")
		}
	})

	t.Run("games.status should work for Steam games after starting", func(t *testing.T) {
		// Start the game to set up the controller state
		startMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"start-rimworld"`),
			Params: map[string]interface{}{
				"name": "games.start",
				"arguments": map[string]interface{}{
					"gameId": "rimworld",
				},
			},
		}

		server.HandleMessage(startMsg)
		time.Sleep(50 * time.Millisecond) // Brief pause for any async setup

		// Now test status - this was the problematic scenario
		statusMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"rimworld-status-after-start"`),
			Params: map[string]interface{}{
				"name": "games.status",
				"arguments": map[string]interface{}{
					"gameId": "rimworld",
				},
			},
		}

		done := make(chan *Message, 1)
		go func() {
			response := server.HandleMessage(statusMsg)
			done <- response
		}()

		select {
		case response := <-done:
			respBytes, _ := json.Marshal(response)
			responseStr := string(respBytes)
			t.Logf("✅ games.status after start completed: %s", responseStr)
			
		case <-time.After(2 * time.Second):
			t.Fatal("❌ games.status after start timed out - deadlock still exists!")
		}
	})
}