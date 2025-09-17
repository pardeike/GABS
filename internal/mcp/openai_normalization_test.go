package mcp

import (
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestOpenAINormalizationIntegration tests the complete integration of OpenAI tool name normalization
func TestOpenAINormalizationIntegration(t *testing.T) {
	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	// Test with OpenAI normalization enabled
	normalizationConfig := &config.ToolNormalizationConfig{
		EnableOpenAINormalization: true,
		MaxToolNameLength:         64,
		PreserveOriginalName:      true,
	}

	testCases := []struct {
		name                 string
		originalToolName     string
		expectedNormalizedName string
		shouldPreserveOriginal bool
	}{
		{
			name:                   "SimpleDotReplacement",
			originalToolName:       "minecraft.inventory.get",
			expectedNormalizedName: "minecraft_inventory_get",
			shouldPreserveOriginal: true,
		},
		{
			name:                   "ComplexGameTool",
			originalToolName:       "rimworld.crafting.build",
			expectedNormalizedName: "rimworld_crafting_build",
			shouldPreserveOriginal: true,
		},
		{
			name:                   "AlreadyOpenAICompliant",
			originalToolName:       "simple_tool_name",
			expectedNormalizedName: "simple_tool_name",
			shouldPreserveOriginal: false,
		},
		{
			name:                   "ComplexWithSpecialChars",
			originalToolName:       "game.mod@special#tool",
			expectedNormalizedName: "game_mod_special_tool",
			shouldPreserveOriginal: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Register tool with normalization
			originalTool := Tool{
				Name:        tc.originalToolName,
				Description: "Test tool for normalization",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{},
				},
			}

			server.RegisterToolWithConfig(originalTool, func(args map[string]interface{}) (*ToolResult, error) {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: "test result"}},
				}, nil
			}, normalizationConfig)

			// Check that the tool was registered with normalized name
			server.mu.RLock()
			registeredTool, exists := server.tools[tc.expectedNormalizedName]
			server.mu.RUnlock()

			if !exists {
				t.Errorf("Expected normalized tool '%s' to be registered, but it wasn't found", tc.expectedNormalizedName)
				// List all registered tools for debugging
				server.mu.RLock()
				t.Logf("Registered tools: %v", getToolNames(server.tools))
				server.mu.RUnlock()
				return
			}

			// Verify the normalized name passes OpenAI validation
			if !util.ValidateOpenAIToolName(registeredTool.Tool.Name) {
				t.Errorf("Normalized tool name '%s' failed OpenAI validation", registeredTool.Tool.Name)
			}

			// Check if original name is preserved in metadata when expected
			if tc.shouldPreserveOriginal {
				if registeredTool.Tool.Meta == nil {
					t.Errorf("Expected metadata to be set for normalized tool '%s'", registeredTool.Tool.Name)
				} else if originalName, exists := registeredTool.Tool.Meta["originalName"]; !exists {
					t.Errorf("Expected originalName to be preserved in metadata for tool '%s'", registeredTool.Tool.Name)
				} else if originalName != tc.originalToolName {
					t.Errorf("Expected originalName '%s', got '%s'", tc.originalToolName, originalName)
				}

				// Check if original name is preserved in description
				if !contains(registeredTool.Tool.Description, tc.originalToolName) {
					t.Errorf("Expected original name '%s' to be preserved in description: %s", tc.originalToolName, registeredTool.Tool.Description)
				}
			}

			t.Logf("✓ '%s' -> '%s' (OpenAI compliant: %v)", tc.originalToolName, registeredTool.Tool.Name, util.ValidateOpenAIToolName(registeredTool.Tool.Name))
		})
	}
}

// TestOpenAINormalizationDisabled tests that normalization is skipped when disabled
func TestOpenAINormalizationDisabled(t *testing.T) {
	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	// Test with OpenAI normalization disabled
	normalizationConfig := &config.ToolNormalizationConfig{
		EnableOpenAINormalization: false,
		MaxToolNameLength:         64,
		PreserveOriginalName:      true,
	}

	originalTool := Tool{
		Name:        "minecraft.inventory.get",
		Description: "Test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{},
		},
	}

	server.RegisterToolWithConfig(originalTool, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{
			Content: []Content{{Type: "text", Text: "test result"}},
		}, nil
	}, normalizationConfig)

	// Check that the tool was registered with original name (not normalized)
	server.mu.RLock()
	_, exists := server.tools["minecraft.inventory.get"]
	server.mu.RUnlock()

	if !exists {
		t.Errorf("Expected original tool name 'minecraft.inventory.get' to be preserved when normalization is disabled")
	}

	// Ensure normalized version is NOT registered
	server.mu.RLock()
	_, normalizedExists := server.tools["minecraft_inventory_get"]
	server.mu.RUnlock()

	if normalizedExists {
		t.Errorf("Did not expect normalized tool name to be registered when normalization is disabled")
	}

	t.Logf("✓ Normalization correctly disabled - original name preserved")
}

// TestRegisterToolBackwardCompatibility tests that RegisterTool still works without normalization
func TestRegisterToolBackwardCompatibility(t *testing.T) {
	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	originalTool := Tool{
		Name:        "minecraft.inventory.get",
		Description: "Test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{},
		},
	}

	// Use the old RegisterTool method (should default to no normalization)
	server.RegisterTool(originalTool, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{
			Content: []Content{{Type: "text", Text: "test result"}},
		}, nil
	})

	// Check that the tool was registered with original name
	server.mu.RLock()
	_, exists := server.tools["minecraft.inventory.get"]
	server.mu.RUnlock()

	if !exists {
		t.Errorf("Expected backward compatibility - original tool name should be preserved with RegisterTool")
	}

	t.Logf("✓ Backward compatibility maintained")
}

// Helper functions

func getToolNames(tools map[string]*ToolHandler) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	return names
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}