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

// Test to reproduce the stopping issue with Steam games
func main() {
	// Create a temporary config with a Steam game for testing
	tempDir, err := os.MkdirTemp("", "gabs_steam_stop_test")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")

	// Create config with a Steam game (this is where the issue likely is)
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"rimworld": {
				ID:         "rimworld",
				Name:       "RimWorld",
				LaunchMode: "SteamAppId",
				Target:     "294100",
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

	// Test the Steam stop issue
	fmt.Println("=== Testing Steam Game Stop Issue ===")

	// 1. Start the Steam game
	fmt.Println("1. Starting rimworld (Steam)...")
	startMsg := &mcp.Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-steam"`),
		Params: map[string]interface{}{
			"name": "games_start",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
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
		ID:      json.RawMessage(`"status-steam"`),
		Params: map[string]interface{}{
			"name": "games_status",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}

	response = server.HandleMessage(statusMsg)
	respBytes, _ = json.Marshal(response)
	statusResult := string(respBytes)
	fmt.Printf("Status result: %s\n", statusResult)

	if strings.Contains(statusResult, "launched") {
		fmt.Println("✓ Steam game shows as 'launched' (launcher triggered)")
	} else if strings.Contains(statusResult, "running") {
		fmt.Println("✓ Steam game shows as 'running'")
	} else {
		fmt.Println("? Unexpected status for Steam game")
	}

	// 3. Try to stop the Steam game - this is where the issue occurs
	fmt.Println("\n3. Attempting to stop rimworld...")
	stopMsg := &mcp.Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"stop-steam"`),
		Params: map[string]interface{}{
			"name": "games_stop",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}

	response = server.HandleMessage(stopMsg)
	respBytes, _ = json.Marshal(response)
	stopResult := string(respBytes)
	fmt.Printf("Stop result: %s\n", stopResult)

	// For Steam games, this will likely fail because GABS doesn't track the actual game process
	if strings.Contains(stopResult, "stopped successfully") {
		fmt.Println("✓ Stop command executed")
	} else if strings.Contains(stopResult, "not running") {
		fmt.Println("⚠ ISSUE: GABS thinks Steam game is not running (launcher process exited)")
	} else if strings.Contains(stopResult, "isError") {
		fmt.Println("✗ Stop command failed")
	}

	// 4. Check status after attempted stop
	fmt.Println("\n4. Checking status after stop attempt...")
	time.Sleep(1 * time.Second)

	response = server.HandleMessage(statusMsg)
	respBytes, _ = json.Marshal(response)
	statusAfterStop := string(respBytes)
	fmt.Printf("Status after stop: %s\n", statusAfterStop)

	if strings.Contains(statusAfterStop, "stopped") {
		fmt.Println("⚠ GABS reports game as stopped, but actual Steam game might still be running")
	} else if strings.Contains(statusAfterStop, "launched") || strings.Contains(statusAfterStop, "running") {
		fmt.Println("✗ Game still shows as running - stop didn't work")
	}

	fmt.Println("\n=== Analysis ===")
	fmt.Println("For Steam games, GABS has limited control:")
	fmt.Println("- Start: Triggers Steam launcher (works)")
	fmt.Println("- Status: Can only check launcher process, not actual game")
	fmt.Println("- Stop: Can only stop launcher process, not actual game")
	fmt.Println("\nThis is the core issue mentioned in the problem statement:")
	fmt.Println("AI thinks game is stopped, but actual game process continues running.")
}