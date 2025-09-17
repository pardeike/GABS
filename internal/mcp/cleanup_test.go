package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

func TestGameResourceCleanup(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServerForTesting(log)

	gameId := "test-game"

	// Register some game-specific tools
	tool1 := Tool{
		Name:        gameId + ".test_tool1",
		Description: "Test tool 1",
		InputSchema: map[string]interface{}{"type": "object"},
	}
	tool2 := Tool{
		Name:        gameId + ".test_tool2", 
		Description: "Test tool 2",
		InputSchema: map[string]interface{}{"type": "object"},
	}

	handler := func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{Content: []Content{{Type: "text", Text: "test"}}}, nil
	}

	server.RegisterGameTool(gameId, tool1, handler, nil)
	server.RegisterGameTool(gameId, tool2, handler, nil)

	// Register some game-specific resources
	resource1 := Resource{
		URI:      "gab://" + gameId + "/test1",
		Name:     "Test Resource 1",
		MimeType: "application/json",
	}
	resource2 := Resource{
		URI:      "gab://" + gameId + "/test2",
		Name:     "Test Resource 2", 
		MimeType: "application/json",
	}

	resourceHandler := func() ([]Content, error) {
		return []Content{{Type: "text", Text: "test"}}, nil
	}

	server.RegisterGameResource(gameId, resource1, resourceHandler)
	server.RegisterGameResource(gameId, resource2, resourceHandler)

	// Verify tools and resources are registered
	server.mu.RLock()
	if len(server.tools) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(server.tools))
	}
	if len(server.resources) != 2 {
		t.Errorf("Expected 2 resources, got %d", len(server.resources))
	}
	if len(server.gameTools[gameId]) != 2 {
		t.Errorf("Expected 2 game tools tracked, got %d", len(server.gameTools[gameId]))
	}
	if len(server.gameResources[gameId]) != 2 {
		t.Errorf("Expected 2 game resources tracked, got %d", len(server.gameResources[gameId]))
	}
	server.mu.RUnlock()

	// Cleanup the game resources
	server.CleanupGameResources(gameId)

	// Verify everything was cleaned up
	server.mu.RLock()
	if len(server.tools) != 0 {
		t.Errorf("Expected 0 tools after cleanup, got %d", len(server.tools))
	}
	if len(server.resources) != 0 {
		t.Errorf("Expected 0 resources after cleanup, got %d", len(server.resources))
	}
	if len(server.gameTools) != 0 {
		t.Errorf("Expected 0 game tools tracking after cleanup, got %d", len(server.gameTools))
	}
	if len(server.gameResources) != 0 {
		t.Errorf("Expected 0 game resources tracking after cleanup, got %d", len(server.gameResources))
	}
	server.mu.RUnlock()
}

func TestBridgeConfigCleanup(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServerForTesting(log)

	gameId := "cleanup-test-game"

	// Create a bridge config file
	port, token, configPath, err := config.WriteBridgeJSON(gameId, "")
	if err != nil {
		t.Fatalf("Failed to create bridge config: %v", err)
	}

	// Verify the file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatalf("Bridge config file was not created: %s", configPath)
	}

	t.Logf("Created bridge config: port=%d, token=%s..., path=%s", port, token[:8], configPath)

	// Cleanup the bridge config  
	server.CleanupBridgeConfig(gameId)

	// Verify the file is gone
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("Bridge config file still exists after cleanup: %s", configPath)
	}

	// Clean up the directory too
	dir := filepath.Dir(configPath)
	if err := os.RemoveAll(dir); err != nil {
		t.Logf("Warning: failed to clean up test directory %s: %v", dir, err)
	}
}

func TestMixedGameCleanup(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServerForTesting(log)

	game1 := "game1"
	game2 := "game2"

	// Register tools for both games
	tool1 := Tool{Name: game1 + ".tool", Description: "Game 1 tool", InputSchema: map[string]interface{}{"type": "object"}}
	tool2 := Tool{Name: game2 + ".tool", Description: "Game 2 tool", InputSchema: map[string]interface{}{"type": "object"}}

	handler := func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{Content: []Content{{Type: "text", Text: "test"}}}, nil
	}

	server.RegisterGameTool(game1, tool1, handler, nil)
	server.RegisterGameTool(game2, tool2, handler, nil)

	// Verify both games have tools
	server.mu.RLock()
	if len(server.tools) != 2 {
		t.Errorf("Expected 2 tools total, got %d", len(server.tools))
	}
	if len(server.gameTools) != 2 {
		t.Errorf("Expected 2 games tracked, got %d", len(server.gameTools))
	}
	server.mu.RUnlock()

	// Cleanup only game1
	server.CleanupGameResources(game1)

	// Verify only game1 tools were removed
	server.mu.RLock()
	if len(server.tools) != 1 {
		t.Errorf("Expected 1 tool remaining after game1 cleanup, got %d", len(server.tools))
	}
	if len(server.gameTools) != 1 {
		t.Errorf("Expected 1 game tracked after game1 cleanup, got %d", len(server.gameTools))
	}
	if _, exists := server.tools[game1+".tool"]; exists {
		t.Error("Game1 tool should have been removed")
	}
	if _, exists := server.tools[game2+".tool"]; !exists {
		t.Error("Game2 tool should still exist")
	}
	server.mu.RUnlock()

	// Cleanup game2
	server.CleanupGameResources(game2)

	// Verify everything is cleaned up
	server.mu.RLock()
	if len(server.tools) != 0 {
		t.Errorf("Expected 0 tools after complete cleanup, got %d", len(server.tools))
	}
	if len(server.gameTools) != 0 {
		t.Errorf("Expected 0 games tracked after complete cleanup, got %d", len(server.gameTools))
	}
	server.mu.RUnlock()
}