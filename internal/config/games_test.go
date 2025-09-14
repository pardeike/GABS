package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGamesConfig(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	t.Run("LoadEmptyConfig", func(t *testing.T) {
		config, err := LoadGamesConfigFromPath(configPath)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if config.Version != "1.0" {
			t.Errorf("Expected version 1.0, got %s", config.Version)
		}

		if len(config.Games) != 0 {
			t.Errorf("Expected empty games map, got %d games", len(config.Games))
		}
	})

	t.Run("AddAndRetrieveGame", func(t *testing.T) {
		config, _ := LoadGamesConfigFromPath(configPath)

		game := GameConfig{
			ID:          "minecraft",
			Name:        "Minecraft",
			LaunchMode:  "DirectPath",
			Target:      "/path/to/minecraft",
			Description: "A test game",
		}
		
		if err := config.AddGame(game); err != nil {
			t.Fatalf("Failed to add valid game: %v", err)
		}
		
		retrieved, exists := config.GetGame("minecraft")
		if !exists {
			t.Error("Expected game to exist after adding")
		}

		if retrieved.Name != "Minecraft" {
			t.Errorf("Expected name 'Minecraft', got '%s'", retrieved.Name)
		}

		if retrieved.LaunchMode != "DirectPath" {
			t.Errorf("Expected launch mode 'DirectPath', got '%s'", retrieved.LaunchMode)
		}
	})

	t.Run("SaveAndLoadConfig", func(t *testing.T) {
		config := &GamesConfig{
			Version: "1.0",
			Games: map[string]GameConfig{
				"rimworld": {
					ID:         "rimworld",
					Name:       "RimWorld",
					LaunchMode: "SteamAppId",
					Target:     "294100",
				},
			},
		}

		err := SaveGamesConfigToPath(config, configPath)
		if err != nil {
			t.Fatalf("Failed to save config: %v", err)
		}

		loadedConfig, err := LoadGamesConfigFromPath(configPath)
		if err != nil {
			t.Fatalf("Failed to load config: %v", err)
		}

		if len(loadedConfig.Games) != 1 {
			t.Errorf("Expected 1 game, got %d", len(loadedConfig.Games))
		}

		game, exists := loadedConfig.GetGame("rimworld")
		if !exists {
			t.Error("Expected rimworld game to exist")
		}

		if game.Name != "RimWorld" {
			t.Errorf("Expected name 'RimWorld', got '%s'", game.Name)
		}

		if game.Target != "294100" {
			t.Errorf("Expected target '294100', got '%s'", game.Target)
		}
	})

	t.Run("RemoveGame", func(t *testing.T) {
		config, _ := LoadGamesConfigFromPath(configPath)

		// Add a game
		game := GameConfig{ID: "testgame", Name: "Test Game", LaunchMode: "DirectPath", Target: "/path/to/game"}
		if err := config.AddGame(game); err != nil {
			t.Fatalf("Failed to add valid game: %v", err)
		}
		
		// Verify it exists
		_, exists := config.GetGame("testgame")
		if !exists {
			t.Error("Expected game to exist before removal")
		}

		// Remove it
		removed := config.RemoveGame("testgame")
		if !removed {
			t.Error("Expected RemoveGame to return true")
		}

		// Verify it's gone
		_, exists = config.GetGame("testgame")
		if exists {
			t.Error("Expected game to not exist after removal")
		}

		// Try to remove non-existent game
		removed = config.RemoveGame("nonexistent")
		if removed {
			t.Error("Expected RemoveGame to return false for non-existent game")
		}
	})

	t.Run("ListGames", func(t *testing.T) {
		config := &GamesConfig{
			Version: "1.0",
			Games: map[string]GameConfig{
				"game1": {ID: "game1", Name: "Game 1"},
				"game2": {ID: "game2", Name: "Game 2"},
				"game3": {ID: "game3", Name: "Game 3"},
			},
		}

		games := config.ListGames()
		if len(games) != 3 {
			t.Errorf("Expected 3 games, got %d", len(games))
		}

		// Verify all games are present (order doesn't matter)
		gameIds := make(map[string]bool)
		for _, game := range games {
			gameIds[game.ID] = true
		}

		for _, expectedId := range []string{"game1", "game2", "game3"} {
			if !gameIds[expectedId] {
				t.Errorf("Expected to find game %s in list", expectedId)
			}
		}
	})
}

func TestNewGabsDirectoryStructure(t *testing.T) {
	t.Run("ConfigPathUsesHomeGabsDirectory", func(t *testing.T) {
		configPath, err := getGamesConfigPath()
		if err != nil {
			t.Fatalf("Failed to get config path: %v", err)
		}

		// Verify the path ends with .gabs/config.json
		if !strings.Contains(configPath, ".gabs") {
			t.Errorf("Expected config path to contain '.gabs', got: %s", configPath)
		}

		if !strings.HasSuffix(configPath, "config.json") {
			t.Errorf("Expected config path to end with 'config.json', got: %s", configPath)
		}

		// Get home directory to verify the full path structure
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("Failed to get home directory: %v", err)
		}

		expectedPath := filepath.Join(homeDir, ".gabs", "config.json")
		if configPath != expectedPath {
			t.Errorf("Expected config path '%s', got '%s'", expectedPath, configPath)
		}
	})

	t.Run("BridgeConfigUsesGabsDirectory", func(t *testing.T) {
		gameID := "test-game"
		configDir, err := getConfigDir(gameID, "")
		if err != nil {
			t.Fatalf("Failed to get config directory: %v", err)
		}

		// Verify the path contains .gabs
		if !strings.Contains(configDir, ".gabs") {
			t.Errorf("Expected config directory to contain '.gabs', got: %s", configDir)
		}

		// Get home directory to verify the full path structure
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("Failed to get home directory: %v", err)
		}

		expectedPath := filepath.Join(homeDir, ".gabs", gameID)
		if configDir != expectedPath {
			t.Errorf("Expected config directory '%s', got '%s'", expectedPath, configDir)
		}
	})
}

func TestStopProcessNameConfiguration(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	t.Run("GameConfigWithStopProcessName", func(t *testing.T) {
		// Create a game config with stop process name
		config := &GamesConfig{
			Version: "1.0",
			Games: map[string]GameConfig{
				"testgame": {
					ID:              "testgame",
					Name:            "Test Game",
					LaunchMode:      "SteamAppId",
					Target:          "123456",
					StopProcessName: "testgame.exe",
					Description:     "Test game with stop process name",
				},
			},
		}

		// Save the config
		err := SaveGamesConfigToPath(config, configPath)
		if err != nil {
			t.Fatalf("Failed to save config: %v", err)
		}

		// Load and verify
		loadedConfig, err := LoadGamesConfigFromPath(configPath)
		if err != nil {
			t.Fatalf("Failed to load config: %v", err)
		}

		game, exists := loadedConfig.GetGame("testgame")
		if !exists {
			t.Fatal("Game not found after loading")
		}

		if game.StopProcessName != "testgame.exe" {
			t.Errorf("Expected StopProcessName 'testgame.exe', got '%s'", game.StopProcessName)
		}
	})

	t.Run("GameConfigWithoutStopProcessName", func(t *testing.T) {
		// Create a game config without stop process name
		config := &GamesConfig{
			Version: "1.0",
			Games: map[string]GameConfig{
				"simplegame": {
					ID:         "simplegame",
					Name:       "Simple Game",
					LaunchMode: "DirectPath",
					Target:     "/path/to/game",
				},
			},
		}

		// Save the config
		configPath2 := filepath.Join(tempDir, "config2.json")
		err := SaveGamesConfigToPath(config, configPath2)
		if err != nil {
			t.Fatalf("Failed to save config: %v", err)
		}

		// Load and verify
		loadedConfig, err := LoadGamesConfigFromPath(configPath2)
		if err != nil {
			t.Fatalf("Failed to load config: %v", err)
		}

		game, exists := loadedConfig.GetGame("simplegame")
		if !exists {
			t.Fatal("Game not found after loading")
		}

		if game.StopProcessName != "" {
			t.Errorf("Expected empty StopProcessName, got '%s'", game.StopProcessName)
		}
	})

	t.Run("JSONSerializationWithStopProcessName", func(t *testing.T) {
		game := GameConfig{
			ID:              "testgame",
			Name:            "Test Game",
			LaunchMode:      "SteamAppId",
			Target:          "123456",
			StopProcessName: "TestGame.exe",
			Description:     "A test game",
		}

		// Marshal to JSON
		jsonData, err := json.MarshalIndent(game, "", "  ")
		if err != nil {
			t.Fatalf("Failed to marshal game config: %v", err)
		}

		// Verify JSON contains stopProcessName
		jsonStr := string(jsonData)
		if !strings.Contains(jsonStr, "stopProcessName") {
			t.Error("JSON should contain stopProcessName field")
		}
		if !strings.Contains(jsonStr, "TestGame.exe") {
			t.Error("JSON should contain the stopProcessName value")
		}

		// Unmarshal and verify
		var unmarshaled GameConfig
		err = json.Unmarshal(jsonData, &unmarshaled)
		if err != nil {
			t.Fatalf("Failed to unmarshal game config: %v", err)
		}

		if unmarshaled.StopProcessName != "TestGame.exe" {
			t.Errorf("Expected StopProcessName 'TestGame.exe', got '%s'", unmarshaled.StopProcessName)
		}
	})
}

func TestGameConfigValidation(t *testing.T) {
	t.Run("ValidDirectPathGame", func(t *testing.T) {
		game := GameConfig{
			ID:         "test",
			Name:       "Test Game",
			LaunchMode: "DirectPath",
			Target:     "/path/to/game",
		}
		
		if err := game.Validate(); err != nil {
			t.Errorf("Expected valid game to pass validation, got: %v", err)
		}
	})

	t.Run("ValidSteamGameWithStopProcess", func(t *testing.T) {
		game := GameConfig{
			ID:              "rimworld",
			Name:            "RimWorld",
			LaunchMode:      "SteamAppId",
			Target:          "294100",
			StopProcessName: "RimWorldWin64.exe",
		}
		
		if err := game.Validate(); err != nil {
			t.Errorf("Expected valid Steam game to pass validation, got: %v", err)
		}
	})

	t.Run("InvalidSteamGameWithoutStopProcess", func(t *testing.T) {
		game := GameConfig{
			ID:         "rimworld",
			Name:       "RimWorld",
			LaunchMode: "SteamAppId",
			Target:     "294100",
			// Missing StopProcessName
		}
		
		err := game.Validate()
		if err == nil {
			t.Error("Expected Steam game without stopProcessName to fail validation")
		}
		if !strings.Contains(err.Error(), "stopProcessName is required") {
			t.Errorf("Expected error about required stopProcessName, got: %v", err)
		}
	})

	t.Run("InvalidEpicGameWithoutStopProcess", func(t *testing.T) {
		game := GameConfig{
			ID:         "testgame",
			Name:       "Test Game",
			LaunchMode: "EpicAppId",
			Target:     "epic-id",
			// Missing StopProcessName
		}
		
		err := game.Validate()
		if err == nil {
			t.Error("Expected Epic game without stopProcessName to fail validation")
		}
		if !strings.Contains(err.Error(), "stopProcessName is required") {
			t.Errorf("Expected error about required stopProcessName, got: %v", err)
		}
	})

	t.Run("MissingRequiredFields", func(t *testing.T) {
		tests := []struct {
			name        string
			game        GameConfig
			expectedErr string
		}{
			{
				name:        "Missing ID",
				game:        GameConfig{Name: "Test", LaunchMode: "DirectPath", Target: "/path"},
				expectedErr: "game ID is required",
			},
			{
				name:        "Missing Name",
				game:        GameConfig{ID: "test", LaunchMode: "DirectPath", Target: "/path"},
				expectedErr: "game name is required",
			},
			{
				name:        "Missing LaunchMode",
				game:        GameConfig{ID: "test", Name: "Test", Target: "/path"},
				expectedErr: "launch mode is required",
			},
			{
				name:        "Missing Target for SteamAppId",
				game:        GameConfig{ID: "test", Name: "Test", LaunchMode: "SteamAppId"},
				expectedErr: "target is required for SteamAppId launch mode",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := tt.game.Validate()
				if err == nil {
					t.Errorf("Expected validation to fail for %s", tt.name)
				}
				if !strings.Contains(err.Error(), tt.expectedErr) {
					t.Errorf("Expected error containing '%s', got: %v", tt.expectedErr, err)
				}
			})
		}
	})

	t.Run("InvalidLaunchMode", func(t *testing.T) {
		game := GameConfig{
			ID:         "test",
			Name:       "Test",
			LaunchMode: "InvalidMode",
			Target:     "/path",
		}
		
		err := game.Validate()
		if err == nil {
			t.Error("Expected validation to fail for invalid launch mode")
		}
		if !strings.Contains(err.Error(), "invalid launch mode") {
			t.Errorf("Expected error about invalid launch mode, got: %v", err)
		}
	})

	t.Run("AllowEmptyTargetForDirectPath", func(t *testing.T) {
		game := GameConfig{
			ID:         "test",
			Name:       "Test",
			LaunchMode: "DirectPath",
			Target:     "", // Empty target should be allowed for DirectPath
		}
		
		err := game.Validate()
		if err != nil {
			t.Errorf("Expected validation to pass for DirectPath with empty target, got: %v", err)
		}
	})
}
