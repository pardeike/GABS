package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBridgeJSON(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	tests := []struct {
		name   string
		gameID string
	}{
		{
			name:   "default config",
			gameID: "testgame",
		},
		{
			name:   "local config (same as default)",
			gameID: "minecraft",
		},
		{
			name:   "another local config",
			gameID: "rimworld",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create game-specific directory (matching the behavior when configDir is specified)
			gameDir := filepath.Join(tempDir, tt.gameID)
			if err := os.MkdirAll(gameDir, 0755); err != nil {
				t.Fatalf("Failed to create game dir: %v", err)
			}

			// Write bridge.json with game-specific directory
			port, token, cfgPath, err := WriteBridgeJSON(tt.gameID, gameDir)
			if err != nil {
				t.Fatalf("WriteBridgeJSON failed: %v", err)
			}

			// Verify return values - now using fallback ranges, so broader validation
			if port <= 0 || port > 65535 {
				t.Errorf("Port %d out of valid range [1, 65535]", port)
			}
			if len(token) != 64 {
				t.Errorf("Token length %d, expected 64", len(token))
			}

			// Config path should be bridge.json in the game's directory
			if !strings.Contains(cfgPath, gameDir) {
				t.Errorf("Config path %s should contain game dir %s", cfgPath, gameDir)
			}
			if filepath.Base(cfgPath) != "bridge.json" {
				t.Errorf("Config path %s should be bridge.json", cfgPath)
			}

			// Read and verify the bridge file contents
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatalf("Failed to read bridge file: %v", err)
			}

			var bridge BridgeJSON
			if err := json.Unmarshal(data, &bridge); err != nil {
				t.Fatalf("Failed to parse bridge file: %v", err)
			}

			// Verify configuration was applied correctly
			if bridge.Port != port {
				t.Errorf("Port mismatch: bridge.json has %d, expected %d", bridge.Port, port)
			}
			if bridge.Token != token {
				t.Errorf("Token mismatch")
			}
			if bridge.GameId != tt.gameID {
				t.Errorf("GameId %s, expected %s", bridge.GameId, tt.gameID)
			}
		})
	}
}

func TestReadBridgeJSON(t *testing.T) {
	tempDir := t.TempDir()
	gameDir := filepath.Join(tempDir, "testread")

	// First create a bridge.json file (always local for GABS)
	port, token, _, err := WriteBridgeJSON("testread", gameDir)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Now read it back
	readHost, readPort, readToken, err := ReadBridgeJSON("testread", gameDir)
	if err != nil {
		t.Fatalf("ReadBridgeJSON failed: %v", err)
	}

	// Verify values match (always local for GABS)
	if readHost != "127.0.0.1" {
		t.Errorf("Read host %s, expected 127.0.0.1", readHost)
	}
	if readPort != port {
		t.Errorf("Read port %d, expected %d", readPort, port)
	}
	if readToken != token {
		t.Errorf("Read token mismatch")
	}

	// Test reading non-existent file
	nonExistentDir := filepath.Join(tempDir, "nonexistent")
	_, _, _, err = ReadBridgeJSON("nonexistent", nonExistentDir)
	if err == nil {
		t.Error("Expected error reading non-existent bridge.json")
	}
}

// TestBridgeConfigWithCustomDirectory tests that bridge configs respect custom config directories
func TestBridgeConfigWithCustomDirectory(t *testing.T) {
	// Create a temporary custom config directory
	tmpDir, err := os.MkdirTemp("", "gabs-test-custom-config-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	gameID := "testgame"
	
	// Test WriteBridgeJSON with custom config directory
	port, token, configPath, err := WriteBridgeJSON(gameID, tmpDir)
	if err != nil {
		t.Fatalf("Failed to write bridge config: %v", err)
	}

	// Verify the file was created in the custom directory
	expectedPath := filepath.Join(tmpDir, gameID, "bridge.json")
	if configPath != expectedPath {
		t.Errorf("Expected config path %s, got %s", expectedPath, configPath)
	}

	// Verify the file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("Bridge config file was not created at %s", configPath)
	}

	// Test ReadBridgeJSON with custom config directory
	host, readPort, readToken, err := ReadBridgeJSON(gameID, tmpDir)
	if err != nil {
		t.Fatalf("Failed to read bridge config: %v", err)
	}

	// Verify the values match
	if host != "127.0.0.1" {
		t.Errorf("Expected host 127.0.0.1, got %s", host)
	}
	if readPort != port {
		t.Errorf("Expected port %d, got %d", port, readPort)
	}
	if readToken != token {
		t.Errorf("Expected token %s, got %s", token, readToken)
	}
}

func TestBackwardCompatibility(t *testing.T) {
	tempDir := t.TempDir()

	// Create old-style bridge.json without extra fields
	oldBridge := BridgeJSON{
		Port:   12345,
		Token:  "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		GameId: "oldgame",
	}

	gameDir := filepath.Join(tempDir, "oldgame")
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	data, _ := json.MarshalIndent(oldBridge, "", "  ")
	cfgPath := filepath.Join(gameDir, "bridge.json")
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatalf("Failed to write old bridge.json: %v", err)
	}

	// Read it back and verify values are correct
	host, port, token, err := ReadBridgeJSON("oldgame", tempDir)
	if err != nil {
		t.Fatalf("ReadBridgeJSON failed: %v", err)
	}

	if host != "127.0.0.1" {
		t.Errorf("Expected default host 127.0.0.1, got %s", host)
	}
	if port != 12345 {
		t.Errorf("Expected port 12345, got %d", port)
	}
	if token != oldBridge.Token {
		t.Errorf("Token mismatch")
	}
}

// TestPortFallbackFunctionality tests the new port allocation with fallback ranges
func TestPortFallbackFunctionality(t *testing.T) {
	tests := []struct {
		name         string
		gamesConfig  *GamesConfig
		expectError  bool
	}{
		{
			name:         "default fallback ranges",
			gamesConfig:  nil,
			expectError:  false,
		},
		{
			name: "custom port range",
			gamesConfig: &GamesConfig{
				PortRanges: &PortRangeConfig{
					CustomRanges: []PortRange{{Min: 8000, Max: 8999}},
				},
			},
			expectError: false,
		},
		{
			name: "multiple custom ranges",
			gamesConfig: &GamesConfig{
				PortRanges: &PortRangeConfig{
					CustomRanges: []PortRange{{Min: 8000, Max: 8099}, {Min: 9000, Max: 9099}},
				},
			},
			expectError: false,
		},
		{
			name: "empty custom ranges falls back to defaults",
			gamesConfig: &GamesConfig{
				PortRanges: &PortRangeConfig{
					CustomRanges: []PortRange{},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, err := findAvailablePortWithConfig(tt.gamesConfig)
			
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if err == nil {
				if port <= 0 || port > 65535 {
					t.Errorf("Port %d out of valid range [1, 65535]", port)
				}
			}
		})
	}
}

// TestConfigPortRanges tests the configuration-based port range functionality
func TestConfigPortRanges(t *testing.T) {
	tests := []struct {
		name           string
		gamesConfig    *GamesConfig
		expectedRanges int
		firstRange     *PortRange
	}{
		{
			name:           "nil config",
			gamesConfig:    nil,
			expectedRanges: 0,
		},
		{
			name: "single range",
			gamesConfig: &GamesConfig{
				PortRanges: &PortRangeConfig{
					CustomRanges: []PortRange{{Min: 8000, Max: 8999}},
				},
			},
			expectedRanges: 1,
			firstRange:     &PortRange{Min: 8000, Max: 8999},
		},
		{
			name: "multiple ranges",
			gamesConfig: &GamesConfig{
				PortRanges: &PortRangeConfig{
					CustomRanges: []PortRange{{Min: 8000, Max: 8999}, {Min: 9000, Max: 9999}},
				},
			},
			expectedRanges: 2,
			firstRange:     &PortRange{Min: 8000, Max: 8999},
		},
		{
			name: "empty custom ranges",
			gamesConfig: &GamesConfig{
				PortRanges: &PortRangeConfig{
					CustomRanges: []PortRange{},
				},
			},
			expectedRanges: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ranges []PortRange
			if tt.gamesConfig != nil && tt.gamesConfig.PortRanges != nil {
				ranges = tt.gamesConfig.PortRanges.CustomRanges
			}
			
			if len(ranges) != tt.expectedRanges {
				t.Errorf("Expected %d ranges, got %d", tt.expectedRanges, len(ranges))
			}
			
			if tt.expectedRanges > 0 && len(ranges) > 0 && tt.firstRange != nil {
				if ranges[0].Min != tt.firstRange.Min || ranges[0].Max != tt.firstRange.Max {
					t.Errorf("Expected first range %v, got %v", tt.firstRange, ranges[0])
				}
			}
		})
	}
}

// TestWriteBridgeJSONWithConfig tests the new config-based bridge creation
func TestWriteBridgeJSONWithConfig(t *testing.T) {
tempDir := t.TempDir()

tests := []struct {
name        string
gameID      string
gamesConfig *GamesConfig
}{
{
name:        "with nil config (uses defaults)",
gameID:      "testgame1",
gamesConfig: nil,
},
{
name:   "with custom port ranges",
gameID: "testgame2",
gamesConfig: &GamesConfig{
PortRanges: &PortRangeConfig{
CustomRanges: []PortRange{{Min: 8000, Max: 8999}},
},
},
},
{
name:   "with empty port ranges (uses defaults)",
gameID: "testgame3",
gamesConfig: &GamesConfig{
PortRanges: &PortRangeConfig{
CustomRanges: []PortRange{},
},
},
},
}

for _, tt := range tests {
t.Run(tt.name, func(t *testing.T) {
// Create game-specific directory
gameDir := filepath.Join(tempDir, tt.gameID)
if err := os.MkdirAll(gameDir, 0755); err != nil {
t.Fatalf("Failed to create game dir: %v", err)
}

// Write bridge.json with games config
port, token, cfgPath, err := WriteBridgeJSONWithConfig(tt.gameID, gameDir, tt.gamesConfig)
if err != nil {
t.Fatalf("WriteBridgeJSONWithConfig failed: %v", err)
}

// Verify return values
if port <= 0 || port > 65535 {
t.Errorf("Port %d out of valid range [1, 65535]", port)
}
if len(token) != 64 {
t.Errorf("Token length %d, expected 64", len(token))
}

// Config path should be bridge.json in the game's directory
if !strings.Contains(cfgPath, gameDir) {
t.Errorf("Config path %s should contain game dir %s", cfgPath, gameDir)
}
if filepath.Base(cfgPath) != "bridge.json" {
t.Errorf("Config path %s should be bridge.json", cfgPath)
}

// Read and verify the bridge file contents
data, err := os.ReadFile(cfgPath)
if err != nil {
t.Fatalf("Failed to read bridge file: %v", err)
}

var bridge BridgeJSON
if err := json.Unmarshal(data, &bridge); err != nil {
t.Fatalf("Failed to parse bridge file: %v", err)
}

// Verify configuration was applied correctly
if bridge.Port != port {
t.Errorf("Port mismatch: bridge.json has %d, expected %d", bridge.Port, port)
}
if bridge.Token != token {
t.Errorf("Token mismatch")
}
if bridge.GameId != tt.gameID {
t.Errorf("GameId %s, expected %s", bridge.GameId, tt.gameID)
}
})
}
}
