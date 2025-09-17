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

// TestRimWorldStatusHang reproduces the specific issue where
// gabs.games_status({"gameId":"rimworld"}) hangs
func TestRimWorldStatusHang(t *testing.T) {
	// Create temp config directory
	tmpDir, err := os.MkdirTemp("", "gabs-rimworld-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create RimWorld config similar to what user might have
	rimworldGame := config.GameConfig{
		ID:              "rimworld",
		Name:            "RimWorld", 
		LaunchMode:      "SteamAppId",
		Target:          "294100",
		StopProcessName: "RimWorldWin64.exe", // This should allow tracking
	}
	
	gamesConfig := &config.GamesConfig{
		Games: make(map[string]config.GameConfig),
	}
	gamesConfig.AddGame(rimworldGame)

	// Create MCP server
	log := util.NewLogger("info")
	server := NewServer(log)
	server.SetConfigDir(tmpDir)

	// Register game management tools
	backoffMin := 100 * time.Millisecond
	backoffMax := 1 * time.Second
	server.RegisterGameManagementTools(gamesConfig, backoffMin, backoffMax)

	t.Run("Test games.status with rimworld should not hang", func(t *testing.T) {
		// This reproduces the specific issue reported
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

		// Use timeout to detect hanging
		done := make(chan *Message, 1)
		go func() {
			response := server.HandleMessage(statusMsg)
			done <- response
		}()

		select {
		case response := <-done:
			respBytes, _ := json.Marshal(response)
			responseStr := string(respBytes)
			t.Logf("✅ Response received: %s", responseStr)
			
			// Should be successful (no error)
			if strings.Contains(responseStr, `"error"`) {
				t.Errorf("games.status returned error: %s", responseStr)
			}
			
			// Should contain stopped status for rimworld
			if !strings.Contains(responseStr, "stopped") {
				t.Errorf("Expected rimworld to be stopped, got: %s", responseStr)
			}
			
		case <-time.After(3 * time.Second):
			t.Fatal("❌ games.status call timed out - deadlock reproduced!")
		}
	})

	// Test the suspected double checkGameStatus call issue
	t.Run("Test games.status with Steam launcher game (potential double mutex)", func(t *testing.T) {
		// Start rimworld first to trigger the GABP connection setup
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

		// Start the game (this sets up internal state)
		server.HandleMessage(startMsg)
		
		// Give a moment for any goroutines to start
		time.Sleep(100 * time.Millisecond)

		// Now test status - this is where the hang happens
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

		// Use timeout to detect hanging
		done := make(chan *Message, 1)
		go func() {
			response := server.HandleMessage(statusMsg)
			done <- response
		}()

		select {
		case response := <-done:
			respBytes, _ := json.Marshal(response)
			responseStr := string(respBytes)
			t.Logf("✅ Response after start: %s", responseStr)
			
		case <-time.After(3 * time.Second):
			t.Fatal("❌ games.status call after start timed out - deadlock reproduced!")
		}
	})
}