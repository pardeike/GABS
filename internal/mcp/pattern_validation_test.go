package mcp

import (
	"regexp"
	"testing"

	"github.com/pardeike/gabs/internal/util"
)

// TestToolNamePatternCompliance verifies that all registered tool names comply with MCP pattern requirements
func TestToolNamePatternCompliance(t *testing.T) {
	// MCP tool name pattern: allows letters, numbers, underscores, hyphens, and dots
	mcpPattern := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

	logger := util.NewLogger("info")
	server := NewServer(logger)

	// Register built-in tools (simulates what happens during server startup)
	server.RegisterTool(Tool{
		Name:        "games.list",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games.start",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games.stop",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games.kill",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games.status",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	server.RegisterTool(Tool{
		Name:        "games.tools",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	})

	// Register some game-specific tools (simulates what Mirror would register)
	gameSpecificTools := []Tool{
		{Name: "minecraft.inventory.get", Description: "Test tool"},
		{Name: "minecraft.world.place_block", Description: "Test tool"},
		{Name: "rimworld.inventory.get", Description: "Test tool"},
		{Name: "rimworld.crafting.build", Description: "Test tool"},
		{Name: "mymod.complex.tool.name.123", Description: "Test tool"},
		{Name: "game-with-hyphens.tool-name", Description: "Test tool"},
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
		name              string
		originalName      string
		expectedSanitized string
	}{
		{
			name:              "SlashesToDots",
			originalName:      "inventory/get",
			expectedSanitized: "inventory.get",
		},
		{
			name:              "MultipleSlashesToDots",
			originalName:      "world/blocks/place",
			expectedSanitized: "world.blocks.place",
		},
		{
			name:              "MixedSeparators",
			originalName:      "player.stats/get",
			expectedSanitized: "player.stats.get",
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
			mcpPattern := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
			if !mcpPattern.MatchString(sanitized) {
				t.Errorf("Sanitized name '%s' is still not MCP compliant", sanitized)
			}

			t.Logf("✓ '%s' -> '%s' (MCP compliant)", tc.originalName, sanitized)
		})
	}
}

// Helper function to test the sanitization logic
func sanitizeToolName(toolName string) string {
	// This matches the logic in mirror.go using the util function
	return util.NormalizeToolNameBasic(toolName)
}
