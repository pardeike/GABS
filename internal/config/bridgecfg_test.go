package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBridgeJSONWithConfig(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	tests := []struct {
		name       string
		gameID     string
		config     BridgeConfig
		expectHost string
		expectMode string
	}{
		{
			name:       "default config",
			gameID:     "testgame",
			config:     BridgeConfig{},
			expectHost: "127.0.0.1",
			expectMode: "local",
		},
		{
			name:       "local config (same as default)",
			gameID:     "minecraft",
			config:     BridgeConfig{},
			expectHost: "127.0.0.1",
			expectMode: "local",
		},
		{
			name:       "another local config",
			gameID:     "rimworld",
			config:     BridgeConfig{},
			expectHost: "127.0.0.1",
			expectMode: "local",
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
			port, token, cfgPath, err := WriteBridgeJSONWithConfig(tt.gameID, gameDir, tt.config)
			if err != nil {
				t.Fatalf("WriteBridgeJSONWithConfig failed: %v", err)
			}

			// Verify return values
			if port < 49152 || port > 65535 {
				t.Errorf("Port %d out of expected range [49152, 65535]", port)
			}
			if len(token) != 64 {
				t.Errorf("Token length %d, expected 64", len(token))
			}

			// Config path should now be unique (bridge-<timestamp>.json)
			if !strings.Contains(cfgPath, gameDir) {
				t.Errorf("Config path %s should contain game dir %s", cfgPath, gameDir)
			}
			if !strings.HasPrefix(filepath.Base(cfgPath), "bridge-") || !strings.HasSuffix(cfgPath, ".json") {
				t.Errorf("Config path %s should be bridge-<timestamp>.json format", cfgPath)
			}

			// Verify both unique and standard bridge files exist
			standardPath := filepath.Join(gameDir, "bridge.json")
			if _, err := os.Stat(standardPath); err != nil {
				t.Errorf("Standard bridge.json should also exist at %s", standardPath)
			}

			// Read and verify the unique bridge file contents
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatalf("Failed to read unique bridge file: %v", err)
			}

			var bridge BridgeJSON
			if err := json.Unmarshal(data, &bridge); err != nil {
				t.Fatalf("Failed to parse unique bridge file: %v", err)
			}

			// Verify configuration was applied correctly
			if bridge.Host != tt.expectHost {
				t.Errorf("Host %s, expected %s", bridge.Host, tt.expectHost)
			}
			if bridge.Mode != tt.expectMode {
				t.Errorf("Mode %s, expected %s", bridge.Mode, tt.expectMode)
			}
			if bridge.Port != port {
				t.Errorf("Port mismatch: bridge.json has %d, expected %d", bridge.Port, port)
			}
			if bridge.Token != token {
				t.Errorf("Token mismatch")
			}
			if bridge.GameId != tt.gameID {
				t.Errorf("GameId %s, expected %s", bridge.GameId, tt.gameID)
			}

			// Also verify the standard bridge.json has the same content
			// standardPath already declared above, so reuse it
			standardData, readErr := os.ReadFile(standardPath)
			if readErr != nil {
				t.Fatalf("Failed to read standard bridge.json: %v", readErr)
			}

			var standardBridge BridgeJSON
			parseErr := json.Unmarshal(standardData, &standardBridge)
			if parseErr != nil {
				t.Fatalf("Failed to parse standard bridge.json: %v", parseErr)
			}

			// Standard bridge.json should have identical content
			if standardBridge.Port != bridge.Port || standardBridge.Token != bridge.Token {
				t.Errorf("Standard bridge.json content doesn't match unique bridge file")
			}
		})
	}
}

func TestReadBridgeJSON(t *testing.T) {
	tempDir := t.TempDir()
	gameDir := filepath.Join(tempDir, "testread")

	// First create a bridge.json file (always local for GABS)
	config := BridgeConfig{}
	port, token, _, err := WriteBridgeJSONWithConfig("testread", gameDir, config)
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

func TestBackwardCompatibility(t *testing.T) {
	tempDir := t.TempDir()

	// Create old-style bridge.json without host/mode fields
	oldBridge := BridgeJSON{
		Port:   12345,
		Token:  "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		GameId: "oldgame",
		Agent:  "gabs-v0.1.0",
		// No Host or Mode fields
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

	// Read it back and verify defaults are applied
	host, port, token, err := ReadBridgeJSON("oldgame", gameDir)
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
