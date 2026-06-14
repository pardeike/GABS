package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestMultiGameToolMirroring demonstrates the confusion AI faces with multiple games
func TestMultiGameToolMirroring(t *testing.T) {
	// This test simulates what happens when you have 2 games running:
	// - FactorySim with inventory/get, world/place_block tools
	// - AdventureGame with inventory/get, crafting/build tools
	//
	// Current behavior: Tool name conflicts, no way to specify game context
	// Expected after fix: Clear game-specific tool names or context

	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	// Simulate what happens when 2 games connect via GABP
	t.Run("SimulateMultipleGameToolRegistration", func(t *testing.T) {
		// Simulate registering tools from different games
		// In reality, Mirror instances would do this

		// Register factory tools
		factoryTools := []Tool{
			{Name: "factory.inventory.get", Description: "Get player inventory in FactorySim (Game: factory)"},
			{Name: "factory.world.place_block", Description: "Place a block in FactorySim world (Game: factory)"},
			{Name: "factory.player.teleport", Description: "Teleport player in FactorySim (Game: factory)"},
		}

		adventureTools := []Tool{
			{Name: "adventure.inventory.get", Description: "Get unit inventory in AdventureGame (Game: adventure)"},
			{Name: "adventure.crafting.build", Description: "Build items in AdventureGame (Game: adventure)"},
			{Name: "adventure.player.teleport", Description: "Move unit in AdventureGame (Game: adventure)"},
		}

		// Register tools as Mirror would do
		for _, tool := range factoryTools {
			server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("FactorySim tool %s executed", tool.Name)}},
				}, nil
			})
		}

		for _, tool := range adventureTools {
			server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("AdventureGame tool %s executed", tool.Name)}},
				}, nil
			})
		}

		// Test that AI can now see distinct tools
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"list-tools"`),
		}

		response := server.HandleMessage(listMsg)
		if response == nil {
			t.Fatal("Expected response from tools/list")
		}

		respBytes, _ := json.Marshal(response)
		t.Logf("Fixed tools list (clear for AI): %s", string(respBytes))

		// Verify AI can call specific game tools
		t.Run("CallFactorySimInventory", func(t *testing.T) {
			callMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"call-factory-inventory"`),
				Params: map[string]interface{}{
					"name": "factory.inventory.get",
					"arguments": map[string]interface{}{
						"playerId": "steve",
					},
				},
			}

			callResponse := server.HandleMessage(callMsg)
			if callResponse == nil {
				t.Fatal("Expected response from factory.inventory.get")
			}

			callBytes, _ := json.Marshal(callResponse)
			t.Logf("FactorySim inventory call result: %s", string(callBytes))
		})

		t.Run("CallAdventureGameInventory", func(t *testing.T) {
			callMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"call-adventure-inventory"`),
				Params: map[string]interface{}{
					"name": "adventure.inventory.get",
					"arguments": map[string]interface{}{
						"playerId": "unit1",
					},
				},
			}

			callResponse := server.HandleMessage(callMsg)
			if callResponse == nil {
				t.Fatal("Expected response from adventure.inventory.get")
			}

			callBytes, _ := json.Marshal(callResponse)
			t.Logf("AdventureGame inventory call result: %s", string(callBytes))
		})
	})
}

// TestGamesToolsCommand tests the new games.tools command
func TestGamesToolsCommand(t *testing.T) {
	// Create a temporary config for testing
	tempDir, err := os.MkdirTemp("", "gabs_tools_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")

	// Create config with multiple games
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"factory": {
				ID:         "factory",
				Name:       "FactorySim",
				LaunchMode: "DirectPath",
				Target:     "/opt/factory/start.sh",
			},
			"adventure": {
				ID:         "adventure",
				Name:       "AdventureGame",
				LaunchMode: "SteamAppId",
				Target:     "123456",
			},
		},
	}

	err = config.SaveGamesConfigToPath(gamesConfig, configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Load config and create server with game management tools
	loadedConfig, err := config.LoadGamesConfigFromPath(configPath)
	if err != nil {
		t.Fatal(err)
	}

	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)
	server.RegisterGameManagementTools(loadedConfig, 0, 0)

	// Simulate having some game-specific tools registered (as Mirror would do)
	factoryTools := []Tool{
		{
			Name:        "factory.inventory.get",
			Description: "Get player inventory in FactorySim (Game: factory)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"playerId": map[string]interface{}{
						"type":        "string",
						"description": "Player ID to inspect",
					},
					"includeArmor": map[string]interface{}{
						"type":        "boolean",
						"description": "Include equipped armor",
						"default":     false,
					},
				},
				"required": []interface{}{"playerId"},
			},
			OutputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"items": map[string]interface{}{
						"type":        "array",
						"description": "Inventory items",
					},
					"armor": map[string]interface{}{
						"type":        []interface{}{"array", "null"},
						"description": "Equipped armor, when requested",
					},
				},
			},
		},
		{
			Name:        "factory.world.place_block",
			Description: "Place a block in FactorySim world (Game: factory)",
		},
	}

	adventureTools := []Tool{
		{
			Name:        "adventure.inventory.get",
			Description: "Get unit inventory in AdventureGame (Game: adventure)",
		},
		{
			Name:        "adventure.crafting.build",
			Description: "Build items in AdventureGame (Game: adventure)",
		},
	}

	// Register tools
	for _, tool := range factoryTools {
		server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Content: []Content{{Type: "text", Text: "Tool executed"}}}, nil
		})
	}

	for _, tool := range adventureTools {
		server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Content: []Content{{Type: "text", Text: "Tool executed"}}}, nil
		})
	}

	t.Run("ListAllGameTools", func(t *testing.T) {
		// Test games.tools without gameId - should list all
		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-game-tools"`),
			Params: map[string]interface{}{
				"name":      "games.tools",
				"arguments": map[string]interface{}{},
			},
		}

		response := server.HandleMessage(toolsMsg)
		if response == nil {
			t.Fatal("Expected response from games.tools")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tools (all games): %s", responseStr)

		// Should mention both factory and adventure tools
		if !strings.Contains(responseStr, "factory.inventory.get") {
			t.Error("Expected to see factory tools")
		}
		if !strings.Contains(responseStr, "adventure.inventory.get") {
			t.Error("Expected to see adventure tools")
		}
	})

	t.Run("ListFactorySimToolsOnly", func(t *testing.T) {
		// Test games.tools with specific gameId
		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-factory-tools"`),
			Params: map[string]interface{}{
				"name": "games.tools",
				"arguments": map[string]interface{}{
					"gameId": "factory",
				},
			},
		}

		response := server.HandleMessage(toolsMsg)
		if response == nil {
			t.Fatal("Expected response from games.tools")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tools (factory only): %s", responseStr)

		// Should mention factory tools but not adventure
		if !strings.Contains(responseStr, "factory.inventory.get") {
			t.Error("Expected to see factory tools")
		}
		if strings.Contains(responseStr, "adventure.inventory.get") {
			t.Error("Should not see adventure tools when filtering for factory")
		}
		if !strings.Contains(responseStr, "\"structuredContent\"") {
			t.Error("Expected games.tools to include structured content")
		}
		if !strings.Contains(responseStr, "\"availableTotal\":2") {
			t.Error("Expected games.tools structured content to include availableTotal")
		}
		if !strings.Contains(responseStr, "\"inputSchema\"") {
			t.Error("Expected games.tools structured content to include schemas")
		}
	})

	t.Run("ListToolNamesOnly", func(t *testing.T) {
		namesMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-tool-names"`),
			Params: map[string]interface{}{
				"name": "games.tool_names",
				"arguments": map[string]interface{}{
					"gameId": "factory",
				},
			},
		}

		response := server.HandleMessage(namesMsg)
		if response == nil {
			t.Fatal("Expected response from games.tool_names")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tool_names (factory): %s", responseStr)

		if !strings.Contains(responseStr, "factory.inventory.get") {
			t.Error("Expected compact tool list to include factory.inventory.get")
		}
		if !strings.Contains(responseStr, "games.tool_detail") {
			t.Error("Expected guidance to use games.tool_detail")
		}
		if strings.Contains(responseStr, "Parameters") {
			t.Error("games.tool_names should not include full parameter details")
		}
	})

	t.Run("ListToolNamesWithBriefStructuredSummaries", func(t *testing.T) {
		namesMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-tool-names-brief"`),
			Params: map[string]interface{}{
				"name": "games.tool_names",
				"arguments": map[string]interface{}{
					"gameId": "factory",
					"brief":  true,
				},
			},
		}

		response := server.HandleMessage(namesMsg)
		if response == nil {
			t.Fatal("Expected response from brief games.tool_names")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tool_names (brief): %s", responseStr)

		if !strings.Contains(responseStr, "\"brief\":true") {
			t.Error("Expected structured content to note brief mode")
		}
		if !strings.Contains(responseStr, "\"summary\":\"Get player inventory in FactorySim (Game: factory)\"") {
			t.Error("Expected structured content to include one-line summaries")
		}
		if strings.Contains(responseStr, "Include equipped armor") {
			t.Error("Brief names response should not inline full parameter detail in text output")
		}
	})

	t.Run("ToolDetailIncludesSchemas", func(t *testing.T) {
		detailMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"tool-detail"`),
			Params: map[string]interface{}{
				"name": "games.tool_detail",
				"arguments": map[string]interface{}{
					"gameId": "factory",
					"tool":   "factory.inventory.get",
				},
			},
		}

		response := server.HandleMessage(detailMsg)
		if response == nil {
			t.Fatal("Expected response from games.tool_detail")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tool_detail: %s", responseStr)

		if !strings.Contains(responseStr, "playerId") {
			t.Error("Expected detailed schema output to include playerId")
		}
		if !strings.Contains(responseStr, "includeArmor") {
			t.Error("Expected detailed schema output to include includeArmor")
		}
		if !strings.Contains(responseStr, "\"outputSchema\"") {
			t.Error("Expected structured content to include outputSchema")
		}
		if !strings.Contains(responseStr, "default: false") {
			t.Error("Expected default values to be surfaced in detail output")
		}
	})

	t.Run("ToolDetailCanInferGameFromQualifiedName", func(t *testing.T) {
		detailMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"tool-detail-no-game"`),
			Params: map[string]interface{}{
				"name": "games.tool_detail",
				"arguments": map[string]interface{}{
					"tool": "factory.inventory.get",
				},
			},
		}

		response := server.HandleMessage(detailMsg)
		if response == nil {
			t.Fatal("Expected response from games.tool_detail without gameId")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tool_detail (qualified without gameId): %s", responseStr)

		if strings.Contains(responseStr, "\"isError\":true") {
			t.Error("Expected fully qualified tool_detail lookup without gameId to succeed")
		}
		if !strings.Contains(responseStr, "\"gameId\":\"factory\"") {
			t.Error("Expected games.tool_detail to resolve the game from the qualified tool name")
		}
	})

	t.Run("ToolDetailRejectsAmbiguousLocalNameWithoutGameId", func(t *testing.T) {
		detailMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"tool-detail-ambiguous"`),
			Params: map[string]interface{}{
				"name": "games.tool_detail",
				"arguments": map[string]interface{}{
					"tool": "inventory.get",
				},
			},
		}

		response := server.HandleMessage(detailMsg)
		if response == nil {
			t.Fatal("Expected response from ambiguous games.tool_detail lookup")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tool_detail (ambiguous without gameId): %s", responseStr)

		if !strings.Contains(responseStr, "matched multiple games") {
			t.Error("Expected ambiguous local names without gameId to be rejected clearly")
		}
	})

	t.Run("ListToolNamesWithPagination", func(t *testing.T) {
		namesMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-tool-names-paged"`),
			Params: map[string]interface{}{
				"name": "games.tool_names",
				"arguments": map[string]interface{}{
					"limit":  2,
					"cursor": "0",
				},
			},
		}

		response := server.HandleMessage(namesMsg)
		if response == nil {
			t.Fatal("Expected response from paged games.tool_names")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tool_names (paged): %s", responseStr)

		if !strings.Contains(responseStr, "\"nextCursor\":\"2\"") {
			t.Error("Expected paged tool names to return a next cursor")
		}
	})

	t.Run("ListDetailedToolsWithQuery", func(t *testing.T) {
		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-detailed-tools-filtered"`),
			Params: map[string]interface{}{
				"name": "games.tools",
				"arguments": map[string]interface{}{
					"query": "craft",
				},
			},
		}

		response := server.HandleMessage(toolsMsg)
		if response == nil {
			t.Fatal("Expected response from filtered games.tools")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tools (filtered): %s", responseStr)

		if !strings.Contains(responseStr, "adventure.crafting.build") {
			t.Error("Expected filtered games.tools to include crafting tool")
		}
		if strings.Contains(responseStr, "• factory.inventory.get") {
			t.Error("Filtered games.tools should not include unrelated tools")
		}
	})

	t.Run("FilteredDetailedToolsWithNoMatchesExplainsWhy", func(t *testing.T) {
		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-detailed-tools-no-match"`),
			Params: map[string]interface{}{
				"name": "games.tools",
				"arguments": map[string]interface{}{
					"gameId": "adventure",
					"prefix": "adventure.core",
				},
			},
		}

		response := server.HandleMessage(toolsMsg)
		if response == nil {
			t.Fatal("Expected response from filtered games.tools with no matches")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tools (no filtered matches): %s", responseStr)

		if !strings.Contains(responseStr, "No matching game-specific tools for 'adventure'.") {
			t.Error("Expected games.tools to clearly say the filter matched nothing")
		}
		if !strings.Contains(responseStr, "2 game-specific tools are currently connected for this game") {
			t.Error("Expected games.tools to mention connected tools still exist")
		}
		if !strings.Contains(responseStr, "none matched prefix \\\"adventure.core\\\"") {
			t.Error("Expected games.tools to echo the unmatched prefix")
		}
		if strings.Contains(responseStr, "no GABP tools are currently connected") {
			t.Error("games.tools should not claim tools are disconnected when the filter matched nothing")
		}
	})

	t.Run("FilteredToolNamesWithNoMatchesExplainsWhy", func(t *testing.T) {
		namesMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-tool-names-no-match"`),
			Params: map[string]interface{}{
				"name": "games.tool_names",
				"arguments": map[string]interface{}{
					"gameId": "adventure",
					"query":  "teleport",
				},
			},
		}

		response := server.HandleMessage(namesMsg)
		if response == nil {
			t.Fatal("Expected response from filtered games.tool_names with no matches")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tool_names (no filtered matches): %s", responseStr)

		if !strings.Contains(responseStr, "No matching game-specific tool names for 'adventure'.") {
			t.Error("Expected games.tool_names to clearly say the filter matched nothing")
		}
		if !strings.Contains(responseStr, "none matched query \\\"teleport\\\"") {
			t.Error("Expected games.tool_names to echo the unmatched query")
		}
		if !strings.Contains(responseStr, "\"availableTotal\":2") {
			t.Error("Expected structured content to include the pre-filter available count")
		}
	})
}

func TestGameToolNamesDefaultLimit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "gabs_tool_names_limit_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"factory": {
				ID:         "factory",
				Name:       "FactorySim",
				LaunchMode: "DirectPath",
				Target:     "/opt/factory/start.sh",
			},
		},
	}

	if err := config.SaveGamesConfigToPath(gamesConfig, configPath); err != nil {
		t.Fatal(err)
	}

	loadedConfig, err := config.LoadGamesConfigFromPath(configPath)
	if err != nil {
		t.Fatal(err)
	}

	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)
	server.RegisterGameManagementTools(loadedConfig, 0, 0)

	for i := 0; i < 55; i++ {
		tool := Tool{
			Name:        fmt.Sprintf("factory.bulk.tool_%02d", i),
			Description: fmt.Sprintf("Bulk tool %02d", i),
		}
		server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Content: []Content{{Type: "text", Text: "Tool executed"}}}, nil
		})
	}

	namesMsg := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"tool-names-default-limit"`),
		Params: map[string]interface{}{
			"name": "games.tool_names",
			"arguments": map[string]interface{}{
				"gameId": "factory",
			},
		},
	}

	response := server.HandleMessage(namesMsg)
	if response == nil {
		t.Fatal("Expected response from games.tool_names default limit test")
	}

	respBytes, _ := json.Marshal(response)
	responseStr := string(respBytes)
	t.Logf("games.tool_names (default limit): %s", responseStr)

	if !strings.Contains(responseStr, "\"returned\":50") {
		t.Error("Expected games.tool_names to default to returning 50 names")
	}
	if !strings.Contains(responseStr, "\"nextCursor\":\"50\"") {
		t.Error("Expected games.tool_names default page to expose nextCursor 50")
	}
}

// TestProposedSolution demonstrates how the fix should work
func TestProposedSolution(t *testing.T) {
	// This test shows how we can fix the multi-game mirroring issue

	t.Run("GamePrefixedTools", func(t *testing.T) {
		// Strategy 1: Prefix all tools with game ID
		// inventory/get becomes:
		// - factory.inventory.get
		// - adventure.inventory.get

		// This makes it clear to AI which game each tool belongs to
		// AI can then explicitly choose: "use factory.inventory.get to get Steve's inventory"
		t.Log("Strategy 1: Game-prefixed tool names")
		t.Log("  factory.inventory.get - Get player inventory in FactorySim")
		t.Log("  adventure.inventory.get - Get unit inventory in AdventureGame")
		t.Log("  AI can clearly specify which game to target")
	})

	t.Run("GameContextParameter", func(t *testing.T) {
		// Strategy 2: Add gameId parameter to all mirrored tools
		// inventory/get with parameters: {gameId: "factory", playerId: "steve"}
		// This allows keeping original tool names while adding game context

		t.Log("Strategy 2: Game context parameter")
		t.Log("  inventory/get with {gameId: 'factory', playerId: 'steve'}")
		t.Log("  inventory/get with {gameId: 'adventure', playerId: 'unit1'}")
		t.Log("  AI adds gameId parameter to specify target game")
	})

	t.Run("GameSpecificToolSets", func(t *testing.T) {
		// Strategy 3: Only register tools for the currently "active" game
		// games.switch tool to change active game context
		// This is simpler but requires switching context

		t.Log("Strategy 3: Single active game context")
		t.Log("  games.switch {'gameId': 'factory'}")
		t.Log("  Now all tools (inventory_get, world_place_block) apply to FactorySim")
		t.Log("  games.switch {'gameId': 'adventure'}")
		t.Log("  Now all tools (inventory_get, crafting_build) apply to AdventureGame")
	})

	// I think Strategy 1 (game-prefixed tools) is clearest for AI
	// It's explicit, no hidden context, and AI can see all available options
}
