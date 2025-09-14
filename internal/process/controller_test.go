package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBridgePathGeneration(t *testing.T) {
	controller := &Controller{}

	// Configure with test parameters
	spec := LaunchSpec{
		GameId:   "test-game",
		Mode:     "DirectPath",
		PathOrId: "echo",
		Args:     []string{"test"},
	}

	err := controller.Configure(spec)
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// Test getBridgePath method
	bridgePath := controller.getBridgePath()

	// Verify the path contains the expected components
	if !strings.Contains(bridgePath, ".gabs") {
		t.Errorf("Bridge path should contain '.gabs', got: %s", bridgePath)
	}

	if !strings.Contains(bridgePath, "test-game") {
		t.Errorf("Bridge path should contain game ID 'test-game', got: %s", bridgePath)
	}

	if !strings.HasSuffix(bridgePath, "bridge.json") {
		t.Errorf("Bridge path should end with 'bridge.json', got: %s", bridgePath)
	}

	// Test the expected path structure
	homeDir, err := os.UserHomeDir()
	if err == nil {
		expectedPath := filepath.Join(homeDir, ".gabs", "test-game", "bridge.json")
		if bridgePath != expectedPath {
			t.Errorf("Bridge path mismatch. Expected: %s, Got: %s", expectedPath, bridgePath)
		}
	}
}

func TestEnvironmentVariables(t *testing.T) {
	// Test that environment variables would be set correctly
	// Note: We can't actually start a process in tests without side effects,
	// but we can verify the logic would work correctly

	controller := &Controller{}

	spec := LaunchSpec{
		GameId:   "minecraft",
		Mode:     "DirectPath",
		PathOrId: "echo",
		Args:     []string{"test"},
	}

	err := controller.Configure(spec)
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// Set bridge info for testing
	controller.SetBridgeInfo(12345, "test-token-1234567890abcdef")

	// Verify the bridge path generation
	bridgePath := controller.getBridgePath()
	expectedGameId := "minecraft"

	if !strings.Contains(bridgePath, expectedGameId) {
		t.Errorf("Bridge path should contain game ID '%s', got: %s", expectedGameId, bridgePath)
	}

	// Test that bridge info is set correctly
	if controller.bridgeInfo == nil {
		t.Fatal("Bridge info should be set")
	}

	// Host is always 127.0.0.1 for GABS - no need to store it in bridgeInfo
	if controller.bridgeInfo.Port != 12345 {
		t.Errorf("Expected port 12345, got %d", controller.bridgeInfo.Port)
	}

	if controller.bridgeInfo.Token != "test-token-1234567890abcdef" {
		t.Errorf("Expected token test-token-1234567890abcdef, got %s", controller.bridgeInfo.Token)
	}

	// The environment variables that would be set are:
	// GABS_GAME_ID=minecraft
	// GABS_BRIDGE_PATH=<bridgePath>
	// GABP_SERVER_PORT=12345
	// GABP_TOKEN=test-token-1234567890abcdef

	t.Logf("GABS_GAME_ID would be set to: %s", expectedGameId)
	t.Logf("GABS_BRIDGE_PATH would be set to: %s", bridgePath)
	t.Logf("GABP_SERVER_PORT would be set to: %d", controller.bridgeInfo.Port)
	t.Logf("GABP_TOKEN would be set to: %s", controller.bridgeInfo.Token)
}
