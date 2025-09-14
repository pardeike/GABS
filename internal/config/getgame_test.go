package config

import (
	"testing"
)

func TestGetGameCopyBehavior(t *testing.T) {
	// Test that GetGame returns a safe copy that doesn't affect the original
	gamesConfig := &GamesConfig{
		Version: "1.0",
		Games:   make(map[string]GameConfig),
	}
	
	// Add a game
	game := GameConfig{
		ID:         "test",
		Name:       "Test Game",
		LaunchMode: "DirectPath",
		Target:     "/path/to/game",
	}
	
	err := gamesConfig.AddGame(game)
	if err != nil {
		t.Fatalf("Error adding game: %v", err)
	}
	
	// Get the game
	retrieved, exists := gamesConfig.GetGame("test")
	if !exists {
		t.Fatal("Game not found")
	}
	
	originalName := retrieved.Name
	
	// Try to modify the returned game
	retrieved.Name = "Modified Name"
	
	// Check if the original was affected
	retrieved2, exists := gamesConfig.GetGame("test")
	if !exists {
		t.Fatal("Game not found on second retrieval")
	}
	
	if retrieved2.Name != originalName {
		t.Errorf("Original config was affected by modifying the returned copy. Expected %s, got %s", originalName, retrieved2.Name)
	}
	
	// This behavior is actually correct for safety - modifications to the returned
	// pointer should not affect the original configuration. Updates should go through AddGame.
}