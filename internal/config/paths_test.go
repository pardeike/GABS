package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewConfigPaths(t *testing.T) {
	t.Run("with custom base directory", func(t *testing.T) {
		customDir := "/tmp/custom-gabs"
		cp, err := NewConfigPaths(customDir)
		if err != nil {
			t.Fatalf("Failed to create ConfigPaths: %v", err)
		}
		
		if cp.GetBaseDir() != customDir {
			t.Errorf("Expected base dir %s, got %s", customDir, cp.GetBaseDir())
		}
	})
	
	t.Run("with empty base directory (uses default)", func(t *testing.T) {
		cp, err := NewConfigPaths("")
		if err != nil {
			t.Fatalf("Failed to create ConfigPaths: %v", err)
		}
		
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("Failed to get home directory: %v", err)
		}
		expectedDir := filepath.Join(homeDir, ".gabs")
		
		if cp.GetBaseDir() != expectedDir {
			t.Errorf("Expected base dir %s, got %s", expectedDir, cp.GetBaseDir())
		}
	})
}

func TestConfigPathsMethods(t *testing.T) {
	testBaseDir := "/tmp/test-gabs"
	cp, err := NewConfigPaths(testBaseDir)
	if err != nil {
		t.Fatalf("Failed to create ConfigPaths: %v", err)
	}
	
	t.Run("GetMainConfigPath", func(t *testing.T) {
		expected := filepath.Join(testBaseDir, "config.json")
		actual := cp.GetMainConfigPath()
		if actual != expected {
			t.Errorf("Expected main config path %s, got %s", expected, actual)
		}
	})
	
	t.Run("GetGameDir", func(t *testing.T) {
		gameID := "minecraft"
		expected := filepath.Join(testBaseDir, gameID)
		actual := cp.GetGameDir(gameID)
		if actual != expected {
			t.Errorf("Expected game dir %s, got %s", expected, actual)
		}
	})
	
	t.Run("GetBridgeConfigPath", func(t *testing.T) {
		gameID := "minecraft"
		expected := filepath.Join(testBaseDir, gameID, "bridge.json")
		actual := cp.GetBridgeConfigPath(gameID)
		if actual != expected {
			t.Errorf("Expected bridge config path %s, got %s", expected, actual)
		}
	})
}

func TestConfigPathsDirectoryOperations(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	testBaseDir := filepath.Join(tempDir, "test-gabs")
	
	cp, err := NewConfigPaths(testBaseDir)
	if err != nil {
		t.Fatalf("Failed to create ConfigPaths: %v", err)
	}
	
	t.Run("EnsureBaseDir", func(t *testing.T) {
		if err := cp.EnsureBaseDir(); err != nil {
			t.Fatalf("Failed to ensure base directory: %v", err)
		}
		
		// Verify directory was created
		if _, err := os.Stat(testBaseDir); os.IsNotExist(err) {
			t.Errorf("Base directory was not created: %s", testBaseDir)
		}
	})
	
	t.Run("EnsureGameDir", func(t *testing.T) {
		gameID := "minecraft"
		if err := cp.EnsureGameDir(gameID); err != nil {
			t.Fatalf("Failed to ensure game directory: %v", err)
		}
		
		// Verify directory was created
		expectedGameDir := cp.GetGameDir(gameID)
		if _, err := os.Stat(expectedGameDir); os.IsNotExist(err) {
			t.Errorf("Game directory was not created: %s", expectedGameDir)
		}
	})
}