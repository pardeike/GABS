package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

func TestStripOutputSchema(t *testing.T) {
	logger := util.NewLogger("warn")

	// Helper to create a server, register tools, add a game tool with outputSchema,
	// and return the tools/list response.
	setup := func(strip bool) *Message {
		server := NewServerForTesting(logger)

		gamesConfig := &config.GamesConfig{
			StripOutputSchema: strip,
		}
		if err := gamesConfig.AddGame(config.GameConfig{
			ID:         "testgame",
			Name:       "Test Game",
			LaunchMode: "DirectPath",
			Target:     "/bin/true",
		}); err != nil {
			t.Fatalf("AddGame: %v", err)
		}

		server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 5*time.Second)

		// Register a game tool that carries outputSchema (like RimBridgeServer does)
		server.RegisterGameTool("testgame", Tool{
			Name:        "testgame.take_screenshot",
			Description: "Take a screenshot",
			InputSchema: map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"fileName": map[string]interface{}{
						"type":        "string",
						"description": "Output file name",
					},
				},
			},
			OutputSchema: map[string]interface{}{
				"description": "The screenshot result with file path and dimensions",
			},
		}, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
		}, &config.ToolNormalizationConfig{})

		// Also register one without outputSchema
		server.RegisterGameTool("testgame", Tool{
			Name:        "testgame.ping",
			Description: "Connectivity check",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		}, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Content: []Content{{Type: "text", Text: "pong"}}}, nil
		}, &config.ToolNormalizationConfig{})

		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"test-list"`),
		}
		return server.HandleMessage(listMsg)
	}

	findTool := func(response *Message, name string) map[string]interface{} {
		respBytes, _ := json.Marshal(response.Result)
		var result struct {
			Tools []map[string]interface{} `json:"tools"`
		}
		json.Unmarshal(respBytes, &result)
		for _, tool := range result.Tools {
			if tool["name"] == name {
				return tool
			}
		}
		return nil
	}

	t.Run("StripOutputSchema_false_preserves_field", func(t *testing.T) {
		response := setup(false)
		tool := findTool(response, "testgame.take_screenshot")
		if tool == nil {
			t.Fatal("tool testgame.take_screenshot not found in tools/list")
		}
		if _, ok := tool["outputSchema"]; !ok {
			t.Error("expected outputSchema to be present when stripOutputSchema is false")
		}
	})

	t.Run("StripOutputSchema_true_removes_field", func(t *testing.T) {
		response := setup(true)
		tool := findTool(response, "testgame.take_screenshot")
		if tool == nil {
			t.Fatal("tool testgame.take_screenshot not found in tools/list")
		}
		if _, ok := tool["outputSchema"]; ok {
			t.Error("expected outputSchema to be stripped when stripOutputSchema is true")
		}
	})

	t.Run("StripOutputSchema_true_preserves_tools_without_it", func(t *testing.T) {
		response := setup(true)
		tool := findTool(response, "testgame.ping")
		if tool == nil {
			t.Fatal("tool testgame.ping not found in tools/list")
		}
		// Should still have name, description, inputSchema
		if tool["name"] != "testgame.ping" {
			t.Errorf("expected name testgame.ping, got %v", tool["name"])
		}
		if tool["description"] != "Connectivity check" {
			t.Errorf("expected description 'Connectivity check', got %v", tool["description"])
		}
	})

	t.Run("StripOutputSchema_true_preserves_inputSchema", func(t *testing.T) {
		response := setup(true)
		tool := findTool(response, "testgame.take_screenshot")
		if tool == nil {
			t.Fatal("tool testgame.take_screenshot not found in tools/list")
		}
		schema, ok := tool["inputSchema"].(map[string]interface{})
		if !ok {
			t.Fatal("inputSchema missing or wrong type")
		}
		props, ok := schema["properties"].(map[string]interface{})
		if !ok {
			t.Fatal("inputSchema.properties missing")
		}
		if _, ok := props["fileName"]; !ok {
			t.Error("inputSchema.properties.fileName should be preserved")
		}
	})
}
