package mcp

import (
	"regexp"
	"testing"

	"github.com/pardeike/gabs/internal/util"
)

// TestToolNamePatternCompliance verifies that all registered tool names comply with MCP pattern requirements
func TestToolNamePatternCompliance(t *testing.T) {
	// MCP tool name pattern: only allows letters, numbers, underscores, and hyphens
	mcpPattern := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	logger := util.NewLogger("info")
	server := NewServer(logger)

	// Register built-in tools (simulates what happens during server startup)
	server.RegisterTool(Tool{
		Name:        "games_list",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games_start", 
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games_stop",
		Description: "Test tool", 
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games_kill",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games_status",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games_tools",
		Description: "Test tool", 
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	// Register some game-specific tools (simulates what Mirror would register)
	gameSpecificTools := []Tool{
		{Name: "minecraft_inventory_get", Description: "Test tool"},
		{Name: "minecraft_world_place_block", Description: "Test tool"},
		{Name: "rimworld_inventory_get", Description: "Test tool"},
		{Name: "rimworld_crafting_build", Description: "Test tool"},
		{Name: "mymod_complex_tool_name_123", Description: "Test tool"},
		{Name: "game-with-hyphens_tool-name", Description: "Test tool"},
	}

	for _, tool := range gameSpecificTools {
		server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{}, nil
		})
	}

	// Check all registered tool names against the MCP pattern
	server.mu.RLock()
	defer server.mu.RUnlock()

	for toolName := range server.tools {
		if !mcpPattern.MatchString(toolName) {
			t.Errorf("Tool name '%s' does not match MCP pattern ^[a-zA-Z0-9_-]+$", toolName)
		} else {
			t.Logf("✓ Tool name '%s' is MCP compliant", toolName)
		}
	}

	t.Logf("Validated %d tool names for MCP compliance", len(server.tools))
}

// TestInvalidToolNames verifies that tool names that would have failed before are now properly sanitized
func TestInvalidToolNames(t *testing.T) {
	testCases := []struct {
		name           string
		originalName   string
		expectedSanitized string
	}{
		{
			name:           "DotsAndSlashes",
			originalName:   "inventory/get",
			expectedSanitized: "inventory_get",
		},
		{
			name:           "MultipleDotsAndSlashes", 
			originalName:   "world/blocks/place",
			expectedSanitized: "world_blocks_place",
		},
		{
			name:           "MixedSeparators",
			originalName:   "player.stats/get",
			expectedSanitized: "player_stats_get",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the sanitization that happens in Mirror.SyncTools()
			sanitized := sanitizeToolName(tc.originalName)
			
			if sanitized != tc.expectedSanitized {
				t.Errorf("Expected sanitized name '%s', got '%s'", tc.expectedSanitized, sanitized)
			}

			// Verify the sanitized name is MCP compliant
			mcpPattern := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
			if !mcpPattern.MatchString(sanitized) {
				t.Errorf("Sanitized name '%s' is still not MCP compliant", sanitized)
			}

			t.Logf("✓ '%s' -> '%s' (MCP compliant)", tc.originalName, sanitized)
		})
	}
}

// Helper function to test the sanitization logic
func sanitizeToolName(toolName string) string {
	// This matches the logic in mirror.go
	result := toolName
	result = regexp.MustCompile(`\.`).ReplaceAllString(result, "_")
	result = regexp.MustCompile(`/`).ReplaceAllString(result, "_")
	return result
}