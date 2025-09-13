package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/util"
)

// Manual test to reproduce the stopping issue
func main() {
	// Create a temporary config with a long-running direct process for testing
	tempDir, err := os.MkdirTemp("", "gabs_stop_test")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")

	// Create config with a long-running process we can test stopping
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"testgame": {
				ID:         "testgame",
				Name:       "Test Long Running Game",
				LaunchMode: "DirectPath",
				Target:     "sleep", // Use sleep command for testing
				Args:       []string{"30"}, // Sleep for 30 seconds
				GabpMode:   "local",
			},
		},
	}

	err = config.SaveGamesConfigToPath(gamesConfig, configPath)
	if err != nil {
		log.Fatal(err)
	}

	// Load config and create server
	loadedConfig, err := config.LoadGamesConfigFromPath(configPath)
	if err != nil {
		log.Fatal(err)
	}

	logger := util.NewLogger("info")
	server := mcp.NewServer(logger)
	server.RegisterGameManagementTools(loadedConfig, 0, 0)

	// Test the stop issue manually
	fmt.Println("=== Testing Game Stop Issue ===")

	// 1. Start the game
	fmt.Println("1. Starting testgame...")
	startMsg := &mcp.Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-test"`),
		Params: map[string]interface{}{
			"name": "games_start",
			"arguments": map[string]interface{}{
				"gameId": "testgame",
			},
		},
	}

	response := server.HandleMessage(startMsg)
	respBytes, _ := json.Marshal(response)
	fmt.Printf("Start result: %s\n", string(respBytes))

	// 2. Check status after start
	fmt.Println("\n2. Checking status after start...")
	statusMsg := &mcp.Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"status-test"`),
		Params: map[string]interface{}{
			"name": "games_status",
			"arguments": map[string]interface{}{
				"gameId": "testgame",
			},
		},
	}

	response = server.HandleMessage(statusMsg)
	respBytes, _ = json.Marshal(response)
	statusResult := string(respBytes)
	fmt.Printf("Status result: %s\n", statusResult)

	if strings.Contains(statusResult, "running") {
		fmt.Println("✓ Game is running as expected")
	} else {
		fmt.Println("✗ Game should be running but isn't reported as such")
	}

	// 3. Stop the game
	fmt.Println("\n3. Stopping testgame...")
	stopMsg := &mcp.Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"stop-test"`),
		Params: map[string]interface{}{
			"name": "games_stop",
			"arguments": map[string]interface{}{
				"gameId": "testgame",
			},
		},
	}

	response = server.HandleMessage(stopMsg)
	respBytes, _ = json.Marshal(response)
	stopResult := string(respBytes)
	fmt.Printf("Stop result: %s\n", stopResult)

	if strings.Contains(stopResult, "stopped successfully") {
		fmt.Println("✓ Stop command executed successfully")
	} else {
		fmt.Println("✗ Stop command failed")
		return
	}

	// 4. Check status after stop - this is where the issue might be
	fmt.Println("\n4. Checking status after stop...")
	time.Sleep(1 * time.Second) // Give it a moment to process

	response = server.HandleMessage(statusMsg)
	respBytes, _ = json.Marshal(response)
	statusAfterStop := string(respBytes)
	fmt.Printf("Status after stop: %s\n", statusAfterStop)

	if strings.Contains(statusAfterStop, "stopped") {
		fmt.Println("✓ Game is correctly reported as stopped")
	} else if strings.Contains(statusAfterStop, "running") {
		fmt.Println("✗ ISSUE FOUND: Game is still reported as running after stop!")
	} else {
		fmt.Println("? Unexpected status result")
	}

	// 5. Verify the actual process is gone (this is what user would check)
	fmt.Println("\n5. Final verification...")
	fmt.Println("In a real scenario, the user would check if the actual game process is still running.")
	fmt.Println("For DirectPath games, GABS should properly terminate the process.")
	fmt.Println("For Steam/Epic games, this is more complex as GABS doesn't track the actual game process.")
}