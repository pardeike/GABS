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

// TestCurrentGameCommandBehavior demonstrates the current issue
func TestCurrentGameCommandBehavior(t *testing.T) {
	// Create a temporary config with a Steam game
	tempDir, err := os.MkdirTemp("", "gabs_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	
	configPath := filepath.Join(tempDir, "config.json")
	
	// Create config with RimWorld game - this mirrors the README example
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"rimworld": {
				ID:         "rimworld",
				Name:       "RimWorld",
				LaunchMode: "SteamAppId",
				Target:     "294100", // This is what AI sees and tries to use as gameId
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
	
	// Test games.list - see what output AI gets
	t.Run("GamesList", func(t *testing.T) {
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"test-list"`),
			Params: map[string]interface{}{
				"name":      "games_list",
				"arguments": map[string]interface{}{},
			},
		}
		
		response := server.HandleMessage(listMsg)
		if response == nil {
			t.Fatal("Expected response from games.list")
		}
		
		// Check if response contains the confusing format
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.list output: %s", responseStr)
		
		// The output should contain both "rimworld" and "294100"
		// This is what confuses AI
		if !strings.Contains(responseStr, "rimworld") {
			t.Error("Expected to see game ID 'rimworld' in output")
		}
		if !strings.Contains(responseStr, "294100") {
			t.Error("Expected to see Steam App ID '294100' in output")
		}
	})
	
	// Test games.start with correct ID (should work)
	t.Run("GamesStartWithCorrectId", func(t *testing.T) {
		startCorrectMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"test-start-correct"`),
			Params: map[string]interface{}{
				"name": "games_start",
				"arguments": map[string]interface{}{
					"gameId": "rimworld",
				},
			},
		}
		
		response := server.HandleMessage(startCorrectMsg)
		if response == nil {
			t.Fatal("Expected response from games.start")
		}
		
		// Should not be an error (though may fail to actually start due to no Steam)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.start with 'rimworld': %s", responseStr)
		
		// Should find the game (even if start fails)
		if strings.Contains(responseStr, "not found") {
			t.Error("Should find game 'rimworld'")
		}
	})
	
	// Test games.start with Steam App ID (currently fails - this is the bug)
	t.Run("GamesStartWithSteamAppId", func(t *testing.T) {
		startWrongMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"test-start-wrong"`),
			Params: map[string]interface{}{
				"name": "games_start",
				"arguments": map[string]interface{}{
					"gameId": "294100", // AI tries this after seeing it in games.list
				},
			},
		}
		
		response := server.HandleMessage(startWrongMsg)
		if response == nil {
			t.Fatal("Expected response from games.start")
		}
		
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.start with '294100': %s", responseStr)
		
		// After our fix, both the game ID and Steam App ID should work
		// The command should resolve the Steam App ID to the actual game
		if strings.Contains(responseStr, "not found") {
			t.Error("After fix, Steam App ID should be accepted and resolved to game")
		}
		
		// Should either succeed or fail with game-specific error (not "not found")
		if strings.Contains(responseStr, "294100") && !strings.Contains(responseStr, "not found") {
			t.Log("Steam App ID successfully resolved - this is the fix!")
		}
	})
}

// TestGameIdResolution tests the new forgiving resolution logic
func TestGameIdResolution(t *testing.T) {
	// Create a fresh temporary config
	tempDir, err := os.MkdirTemp("", "gabs_resolution_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	
	configPath := filepath.Join(tempDir, "config.json")
	
	// Create config with multiple games to test resolution
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"minecraft": {
				ID:         "minecraft",
				Name:       "Minecraft Server",
				LaunchMode: "DirectPath",
				Target:     "/opt/minecraft/start.sh",
				GabpMode:   "local",
			},
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
	
	testCases := []struct {
		name           string
		gameIdInput    string
		expectedGameId string
		shouldFind     bool
	}{
		{
			name:           "DirectGameId",
			gameIdInput:    "rimworld",
			expectedGameId: "rimworld",
			shouldFind:     true,
		},
		{
			name:           "SteamAppIdResolution",
			gameIdInput:    "294100",
			expectedGameId: "rimworld",
			shouldFind:     true,
		},
		{
			name:           "DirectPathResolution",
			gameIdInput:    "/opt/minecraft/start.sh",
			expectedGameId: "minecraft",
			shouldFind:     true,
		},
		{
			name:        "InvalidId",
			gameIdInput: "nonexistent",
			shouldFind:  false,
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			statusMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"test-resolution"`),
				Params: map[string]interface{}{
					"name": "games_status",
					"arguments": map[string]interface{}{
						"gameId": tc.gameIdInput,
					},
				},
			}
			
			response := server.HandleMessage(statusMsg)
			if response == nil {
				t.Fatal("Expected response from games.status")
			}
			
			respBytes, _ := json.Marshal(response)
			responseStr := string(respBytes)
			t.Logf("Resolution test for '%s': %s", tc.gameIdInput, responseStr)
			
			if tc.shouldFind {
				if strings.Contains(responseStr, "not found") {
					t.Errorf("Expected to find game for input '%s'", tc.gameIdInput)
				}
				if tc.expectedGameId != "" && !strings.Contains(responseStr, tc.expectedGameId) {
					t.Errorf("Expected response to reference game ID '%s'", tc.expectedGameId)
				}
			} else {
				if !strings.Contains(responseStr, "not found") {
					t.Errorf("Expected not to find game for input '%s'", tc.gameIdInput)
				}
			}
		})
	}
}