package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestGameStopFix validates the fix for the Steam/Epic game stopping issue
func TestGameStopFix(t *testing.T) {
	// Create a temporary config for testing
	tempDir, err := os.MkdirTemp("", "gabs_stop_fix_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")

	// Create config with both direct and Steam games
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"direct-game": {
				ID:         "direct-game",
				Name:       "Direct Path Game",
				LaunchMode: "DirectPath",
				Target:     "sleep",
				Args:       []string{"0.1"}, // Very short sleep for testing
				GabpMode:   "local",
			},
			"steam-game": {
				ID:         "steam-game",
				Name:       "Steam Game",
				LaunchMode: "SteamAppId",
				Target:     "123456",
				GabpMode:   "local",
			},
		},
	}

	err = config.SaveGamesConfigToPath(gamesConfig, configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Load config and create server
	loadedConfig, err := config.LoadGamesConfigFromPath(configPath)
	if err != nil {
		t.Fatal(err)
	}

	logger := util.NewLogger("info")
	server := NewServer(logger)
	server.RegisterGameManagementTools(loadedConfig, 0, 0)

	t.Run("DirectGameStopWorksCorrectly", func(t *testing.T) {
		// Start direct game
		startMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"start-direct"`),
			Params: map[string]interface{}{
				"name": "games.start",
				"arguments": map[string]interface{}{
					"gameId": "direct-game",
				},
			},
		}

		response := server.HandleMessage(startMsg)
		if response == nil {
			t.Fatal("Expected response from games.start")
		}

		// Stop direct game
		stopMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"stop-direct"`),
			Params: map[string]interface{}{
				"name": "games.stop",
				"arguments": map[string]interface{}{
					"gameId": "direct-game",
				},
			},
		}

		response = server.HandleMessage(stopMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Direct game stop result: %s", responseStr)

		// Direct games should stop successfully
		if strings.Contains(responseStr, "stopped successfully") {
			t.Log("✓ Direct game stops correctly")
		} else if strings.Contains(responseStr, "isError") && strings.Contains(responseStr, "true") {
			t.Error("Direct game should stop without error")
		}
	})

	t.Run("SteamGameStopShowsProperWarning", func(t *testing.T) {
		// Start Steam game
		startMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"start-steam"`),
			Params: map[string]interface{}{
				"name": "games.start",
				"arguments": map[string]interface{}{
					"gameId": "steam-game",
				},
			},
		}

		response := server.HandleMessage(startMsg)
		if response == nil {
			t.Fatal("Expected response from games.start")
		}

		// Stop Steam game - this is where the fix applies
		stopMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"stop-steam"`),
			Params: map[string]interface{}{
				"name": "games.stop",
				"arguments": map[string]interface{}{
					"gameId": "steam-game",
				},
			},
		}

		response = server.HandleMessage(stopMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Steam game stop result: %s", responseStr)

		// Should not be marked as error but should show warning
		if strings.Contains(responseStr, "isError") && strings.Contains(responseStr, "true") {
			t.Error("Steam game stop should not be marked as error with fix")
		}

		// Should contain the warning about limitation
		if !strings.Contains(responseStr, "launcher process stopped") {
			t.Error("Should warn that only launcher process was stopped")
		}

		if !strings.Contains(responseStr, "may still be running independently") {
			t.Error("Should warn that actual game may still be running")
		}

		if !strings.Contains(responseStr, "SteamAppId") {
			t.Error("Should mention SteamAppId specifically")
		}

		t.Log("✓ Steam game stop shows proper warning about limitations")
	})

	t.Run("SteamGameStatusIsInformative", func(t *testing.T) {
		// Start Steam game
		startMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"start-steam-status"`),
			Params: map[string]interface{}{
				"name": "games.start",
				"arguments": map[string]interface{}{
					"gameId": "steam-game",
				},
			},
		}

		server.HandleMessage(startMsg)

		// Check status
		statusMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"status-steam"`),
			Params: map[string]interface{}{
				"name": "games.status",
				"arguments": map[string]interface{}{
					"gameId": "steam-game",
				},
			},
		}

		response := server.HandleMessage(statusMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Steam game status: %s", responseStr)

		// Should provide informative status with clear explanation
		if !strings.Contains(responseStr, "launched via SteamAppId") {
			t.Error("Should indicate game was launched via SteamAppId")
		}

		if !strings.Contains(responseStr, "cannot track the game process") {
			t.Error("Should explain GABS limitation for tracking")
		}

		if !strings.Contains(responseStr, "Check Steam") {
			t.Error("Should suggest checking Steam for actual status")
		}

		t.Log("✓ Steam game status provides clear explanation of limitations")
	})

	t.Run("GamesListShowsLimitations", func(t *testing.T) {
		// List all games
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-games"`),
			Params: map[string]interface{}{
				"name": "games.list",
				"arguments": map[string]interface{}{},
			},
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Games list: %s", responseStr)

		// Should mention limitation for Steam games
		if !strings.Contains(responseStr, "cannot directly stop SteamAppId games") {
			t.Error("Should warn about SteamAppId stop limitations in games list")
		}

		t.Log("✓ Games list clearly indicates Steam/Epic limitations")
	})
}

// TestImprovedStatusReporting verifies the enhanced status descriptions
func TestImprovedStatusReporting(t *testing.T) {
	logger := util.NewLogger("info")
	server := NewServer(logger)
	
	// Test the status description logic by checking actual behavior
	// rather than trying to mock internal state
	
	t.Run("DirectGameStatusDescriptions", func(t *testing.T) {
		// Create a temporary config for testing
		tempDir, err := os.MkdirTemp("", "gabs_status_test")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(tempDir)

		configPath := filepath.Join(tempDir, "config.json")
		gamesConfig := &config.GamesConfig{
			Version: "1.0",
			Games: map[string]config.GameConfig{
				"direct-test": {
					ID:         "direct-test",
					Name:       "Direct Test Game",
					LaunchMode: "DirectPath",
					Target:     "sleep",
					Args:       []string{"0.01"},
					GabpMode:   "local",
				},
				"steam-test": {
					ID:         "steam-test",
					Name:       "Steam Test Game",
					LaunchMode: "SteamAppId",
					Target:     "999999",
					GabpMode:   "local",
				},
			},
		}

		err = config.SaveGamesConfigToPath(gamesConfig, configPath)
		if err != nil {
			t.Fatal(err)
		}

		loadedConfig, err := config.LoadGamesConfigFromPath(configPath)
		if err != nil {
			t.Fatal(err)
		}

		server.RegisterGameManagementTools(loadedConfig, 0, 0)

		// Test stopped status
		directGameConfig := gamesConfig.Games["direct-test"]
		desc := server.getStatusDescription("direct-test", &directGameConfig)
		if desc != "stopped" {
			t.Errorf("Expected 'stopped' for non-running direct game, got: %s", desc)
		}

		// Test Steam game status descriptions are more informative
		steamGameConfig := gamesConfig.Games["steam-test"]
		steamDesc := server.getStatusDescription("steam-test", &steamGameConfig)
		if steamDesc != "stopped" {
			t.Errorf("Expected 'stopped' for non-running Steam game, got: %s", steamDesc)
		}

		t.Log("✓ Status descriptions work correctly for stopped games")
	})
}