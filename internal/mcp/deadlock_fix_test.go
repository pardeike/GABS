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

// TestGameStatusNoDeadlock validates the fix for the mutex deadlock issue
// that was causing games.status to hang after starting a game
func TestGameStatusNoDeadlock(t *testing.T) {
	// Create temp config directory
	tmpDir, err := os.MkdirTemp("", "gabs-deadlock-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a minimal games config
	testGame := config.GameConfig{
		ID:         "test-game",
		Name:       "Test Game",
		LaunchMode: "DirectPath",
		Target:     "/bin/echo", // Use echo command as a simple executable
		Args:       []string{"Game started successfully"},
	}
	
	gamesConfig := &config.GamesConfig{
		Games: make(map[string]config.GameConfig),
	}
	gamesConfig.AddGame(testGame)

	// Create MCP server
	log := util.NewLogger("info")
	server := NewServer(log)
	server.SetConfigDir(tmpDir)

	// Register game management tools
	backoffMin := 100 * time.Millisecond
	backoffMax := 1 * time.Second
	server.RegisterGameManagementTools(gamesConfig, backoffMin, backoffMax)

	t.Run("Test games.status before starting game", func(t *testing.T) {
		// Test games.status for a specific game before starting it
		msg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"status-before"`),
			Params: map[string]interface{}{
				"name": "games.status",
				"arguments": map[string]interface{}{
					"gameId": "test-game",
				},
			},
		}

		response := server.HandleMessage(msg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Status before start: %s", responseStr)

		// Should contain "stopped" status
		if !strings.Contains(responseStr, "stopped") {
			t.Error("Expected game to be stopped before starting")
		}
	})

	t.Run("Test games.start doesn't cause deadlock", func(t *testing.T) {
		// Start the game - this used to cause a deadlock
		startMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"start-game"`),
			Params: map[string]interface{}{
				"name": "games.start",
				"arguments": map[string]interface{}{
					"gameId": "test-game",
				},
			},
		}

		response := server.HandleMessage(startMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Start game response: %s", responseStr)

		// Should be successful
		if strings.Contains(responseStr, "error") {
			t.Errorf("Game start failed: %s", responseStr)
		}
	})

	t.Run("Test games.status works after starting game (no deadlock)", func(t *testing.T) {
		// Give the game a moment to start and the goroutine to run
		time.Sleep(200 * time.Millisecond)

		// This call should NOT hang - this was the original issue
		statusMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"status-after"`),
			Params: map[string]interface{}{
				"name": "games.status",
				"arguments": map[string]interface{}{
					"gameId": "test-game",
				},
			},
		}

		// Use a timeout to detect if the call hangs
		done := make(chan *Message, 1)
		go func() {
			response := server.HandleMessage(statusMsg)
			done <- response
		}()

		select {
		case response := <-done:
			respBytes, _ := json.Marshal(response)
			responseStr := string(respBytes)
			t.Logf("Status after start: %s", responseStr)

			// The response should be successful (no hanging)
			if strings.Contains(responseStr, `"error"`) {
				t.Errorf("games.status returned error after start: %s", responseStr)
			}
			
			t.Log("✅ games.status responded successfully after game start - no deadlock!")

		case <-time.After(5 * time.Second):
			t.Fatal("❌ games.status call timed out - deadlock still exists!")
		}
	})

	t.Run("Test multiple status calls work without blocking", func(t *testing.T) {
		// Test multiple rapid calls to ensure no deadlock in concurrent scenarios
		results := make(chan string, 3)
		
		for i := 0; i < 3; i++ {
			go func(callNum int) {
				statusMsg := &Message{
					JSONRPC: "2.0",
					Method:  "tools/call",
					ID:      json.RawMessage(`"concurrent-` + string(rune('0'+callNum)) + `"`),
					Params: map[string]interface{}{
						"name": "games.status",
						"arguments": map[string]interface{}{
							"gameId": "test-game",
						},
					},
				}
				
				response := server.HandleMessage(statusMsg)
				respBytes, _ := json.Marshal(response)
				results <- string(respBytes)
			}(i)
		}

		// Collect all results with timeout
		for i := 0; i < 3; i++ {
			select {
			case result := <-results:
				t.Logf("Concurrent call %d result: %s", i+1, result)
			case <-time.After(3 * time.Second):
				t.Fatalf("❌ Concurrent games.status call %d timed out", i+1)
			}
		}
		
		t.Log("✅ All concurrent games.status calls completed successfully!")
	})
}