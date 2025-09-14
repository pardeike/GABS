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
			},
			"steam-game": {
				ID:         "steam-game",
				Name:       "Steam Game",
				LaunchMode: "SteamAppId",
				Target:     "123456",
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

		// Should be marked as error because stopProcessName is missing
		if !strings.Contains(responseStr, "isError") || !strings.Contains(responseStr, "true") {
			t.Error("Steam game stop should be marked as error when stopProcessName is missing")
		}

		// Should contain the warning about missing configuration
		if !strings.Contains(responseStr, "Configure 'stopProcessName'") {
			t.Error("Should warn about missing stopProcessName configuration")
		}

		if !strings.Contains(responseStr, "SteamAppId") {
			t.Error("Should mention SteamAppId specifically")
		}

		t.Log("✓ Steam game stop shows proper warning about missing configuration")
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

	t.Run("GamesListIsSimplified", func(t *testing.T) {
		// List all games
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-games"`),
			Params: map[string]interface{}{
				"name":      "games.list",
				"arguments": map[string]interface{}{},
			},
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Games list: %s", responseStr)

		// Should NOT mention validation details - that's now in games.show
		if strings.Contains(responseStr, "Missing stopProcessName") {
			t.Error("games.list should be simplified - validation warnings should be in games.show")
		}
		
		// Should NOT contain limitation warnings - that's for games.status
		if strings.Contains(responseStr, "cannot directly stop SteamAppId games") {
			t.Error("games.list should be simplified and not show stop limitations")
		}
		if strings.Contains(responseStr, "Note:") {
			t.Error("games.list should not contain verbose notes - should be simplified")
		}
		
		// Should contain game IDs only
		if !strings.Contains(responseStr, "direct-game") {
			t.Error("Expected to see game ID 'direct-game' in simplified output")
		}
		if !strings.Contains(responseStr, "steam-game") {
			t.Error("Expected to see game ID 'steam-game' in simplified output")
		}

		t.Log("✓ Games list is now simplified to just game IDs")
	})

	t.Run("GamesShowValidation", func(t *testing.T) {
		// Show details for Steam game to check validation
		showMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"show-steam-game"`),
			Params: map[string]interface{}{
				"name": "games.show",
				"arguments": map[string]interface{}{
					"gameId": "steam-game",
				},
			},
		}

		response := server.HandleMessage(showMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("Games show steam-game: %s", responseStr)

		// Should mention missing stopProcessName for Steam games in detailed view
		if !strings.Contains(responseStr, "Missing stopProcessName") {
			t.Error("games.show should warn about SteamAppId stop limitations")
		}

		// Should show detailed configuration
		if !strings.Contains(responseStr, "steam-game") {
			t.Error("Expected to see game ID in detailed view")
		}
		if !strings.Contains(responseStr, "SteamAppId") {
			t.Error("Expected to see launch mode in detailed view")
		}

		t.Log("✓ Games show provides detailed validation information")
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
				},
				"steam-test": {
					ID:         "steam-test",
					Name:       "Steam Test Game",
					LaunchMode: "SteamAppId",
					Target:     "999999",
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
