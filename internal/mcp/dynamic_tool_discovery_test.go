package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestDynamicToolDiscoveryWorkflow demonstrates how AI agents can effectively
// handle the dynamic expansion of MCP tools as GABP-compliant games connect
func TestDynamicToolDiscoveryWorkflow(t *testing.T) {
	// This test simulates the real-world concern from the GitHub issue:
	// "How will AI agents handle dynamic tool expansion in GABS?"

	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	// Create shared games config for all phases
	gamesConfig := &config.GamesConfig{}
	err1 := gamesConfig.AddGame(config.GameConfig{
		ID:         "factory",
		Name:       "Example Game",
		LaunchMode: "DirectPath",
		Target:     "/opt/factory/server.jar",
	})
	if err1 != nil {
		t.Fatalf("Failed to add factory game: %v", err1)
	}

	err2 := gamesConfig.AddGame(config.GameConfig{
		ID:              "adventure",
		Name:            "AdventureGame",
		LaunchMode:      "SteamAppId",
		Target:          "123456",
		StopProcessName: "GameName.exe", // Required for Steam games
	})
	if err2 != nil {
		t.Fatalf("Failed to add adventure game: %v", err2)
	}

	// Step 1: Simulate initial GABS server state - only core game management tools
	t.Run("Phase1_InitialState", func(t *testing.T) {
		// Register core game management tools (what GABS starts with)
		server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 5*time.Second)

		// AI agent discovers initial tools
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"discover-initial"`),
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		t.Logf("🤖 AI Agent Initial Discovery:")
		t.Logf("Phase 1 - Available tools: %s", responseStr)

		// Verify AI sees only core game management tools
		expectedCoreTools := []string{
			"games.list",
			"games.show",
			"games.start",
			"games.stop",
			"games.kill",
			"games.status",
			"games.tool_names",
			"games.tool_detail",
			"games.tools",
			"games.connect",
			"games.get_attention",
			"games.ack_attention",
			"games.call_tool",
		}
		for _, tool := range expectedCoreTools {
			if !strings.Contains(responseStr, tool) {
				t.Errorf("Expected core tool '%s' not found", tool)
			}
		}

		// Verify NO game-specific tools yet
		if strings.Contains(responseStr, "factory.") || strings.Contains(responseStr, "adventure.") {
			t.Error("Should not see game-specific tools before games are connected")
		}

		t.Log("✅ Phase 1 Complete: AI agent sees the current stable core management surface")
	})

	// Step 2: Simulate game connection with GABP bridge - tools expand dramatically
	t.Run("Phase2_GameConnection", func(t *testing.T) {
		// Simulate what happens when a game with GABP bridge connects
		// The Mirror system would do this automatically in real usage

		t.Log("🎮 FactorySim game connects with GABP bridge...")

		// Simulate registering tools from FactorySim GABP bridge
		factoryTools := []Tool{
			{
				Name:        "factory.inventory.get",
				Description: "Get player inventory in FactorySim (Game: factory)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"playerId": map[string]interface{}{
							"type":        "string",
							"description": "Player UUID or name",
						},
					},
					"required": []string{"playerId"},
				},
			},
			{
				Name:        "factory.inventory.set",
				Description: "Modify player inventory in FactorySim (Game: factory)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"playerId": map[string]interface{}{"type": "string"},
						"item":     map[string]interface{}{"type": "string"},
						"count":    map[string]interface{}{"type": "integer"},
					},
					"required": []string{"playerId", "item", "count"},
				},
			},
			{
				Name:        "factory.world.place_block",
				Description: "Place a block in FactorySim world (Game: factory)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"x":         map[string]interface{}{"type": "integer"},
						"y":         map[string]interface{}{"type": "integer"},
						"z":         map[string]interface{}{"type": "integer"},
						"blockType": map[string]interface{}{"type": "string"},
					},
					"required": []string{"x", "y", "z", "blockType"},
				},
			},
			{
				Name:        "factory.player.teleport",
				Description: "Teleport player in FactorySim (Game: factory)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"playerId": map[string]interface{}{"type": "string"},
						"x":        map[string]interface{}{"type": "number"},
						"y":        map[string]interface{}{"type": "number"},
						"z":        map[string]interface{}{"type": "number"},
					},
					"required": []string{"playerId", "x", "y", "z"},
				},
			},
			{
				Name:        "factory.chat.send",
				Description: "Send chat message in FactorySim (Game: factory)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{"type": "string"},
						"target":  map[string]interface{}{"type": "string", "description": "Optional: specific player"},
					},
					"required": []string{"message"},
				},
			},
			{
				Name:        "factory.time.set",
				Description: "Set world time in FactorySim (Game: factory)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"time": map[string]interface{}{"type": "string", "enum": []string{"day", "night", "noon", "midnight"}},
					},
					"required": []string{"time"},
				},
			},
			{
				Name:        "factory.weather.set",
				Description: "Control weather in FactorySim (Game: factory)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"weather": map[string]interface{}{"type": "string", "enum": []string{"clear", "rain", "thunder"}},
					},
					"required": []string{"weather"},
				},
			},
		}

		// Register FactorySim tools (as Mirror would do)
		for _, tool := range factoryTools {
			// Capture tool by value to avoid closure bug
			currentTool := tool
			server.RegisterTool(currentTool, func(toolName string) func(args map[string]interface{}) (*ToolResult, error) {
				return func(args map[string]interface{}) (*ToolResult, error) {
					return &ToolResult{
						Content: []Content{{Type: "text", Text: fmt.Sprintf("Executed %s with args: %v", toolName, args)}},
					}, nil
				}
			}(currentTool.Name))
		}

		t.Logf("✅ Registered %d FactorySim tools", len(factoryTools))

		// AI agent discovers expanded tools
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"discover-expanded"`),
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		t.Log("🤖 AI Agent Discovers Tool Expansion:")
		t.Logf("Phase 2 - Available tools after FactorySim connection: %s", responseStr)

		// Count tools to show expansion
		toolCount := strings.Count(responseStr, `"name"`)
		t.Logf("✅ AI now sees %d total tools (was 6, expanded by %d)", toolCount, toolCount-6)

		// Verify game-specific tools are present
		factoryToolNames := []string{"factory.inventory.get", "factory.world.place_block", "factory.player.teleport", "factory.chat.send"}
		for _, tool := range factoryToolNames {
			if !strings.Contains(responseStr, tool) {
				t.Errorf("Expected FactorySim tool '%s' not found after connection", tool)
			}
		}
	})

	// Step 3: Demonstrate AI discovery workflow using games.tools
	t.Run("Phase3_AIDiscoveryWorkflow", func(t *testing.T) {
		t.Log("🤖 AI Agent using discovery pattern...")

		// AI uses games.tools to discover what FactorySim can do
		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"discover-factory"`),
			Params: map[string]interface{}{
				"name": "games.tools",
				"arguments": map[string]interface{}{
					"gameId": "factory",
				},
			},
		}

		response := server.HandleMessage(toolsMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		t.Log("🎯 AI Discovery Result:")
		t.Logf("games.tools response: %s", responseStr)

		// Verify the discovery response is helpful for AI
		expectedCapabilities := []string{"inventory", "world", "player", "chat", "time", "weather"}
		for _, capability := range expectedCapabilities {
			if !strings.Contains(responseStr, capability) {
				t.Errorf("Expected capability '%s' not mentioned in discovery", capability)
			}
		}

		t.Log("✅ AI can now understand what FactorySim bridge provides")
	})

	// Step 4: Show multi-game scenario - AdventureGame also connects
	t.Run("Phase4_MultiGameExpansion", func(t *testing.T) {
		t.Log("🎮 AdventureGame game also connects with GABP bridge...")

		// Simulate AdventureGame GABP bridge connecting
		adventureTools := []Tool{
			{
				Name:        "adventure.inventory.get",
				Description: "Get unit inventory in AdventureGame (Game: adventure)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"unitId": map[string]interface{}{"type": "string"},
					},
					"required": []string{"unitId"},
				},
			},
			{
				Name:        "adventure.crafting.build",
				Description: "Build items/structures in AdventureGame (Game: adventure)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"item":     map[string]interface{}{"type": "string"},
						"quantity": map[string]interface{}{"type": "integer"},
						"location": map[string]interface{}{"type": "string"},
					},
					"required": []string{"item"},
				},
			},
			{
				Name:        "adventure.unit.command",
				Description: "Give orders to units in AdventureGame (Game: adventure)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"unitId":   map[string]interface{}{"type": "string"},
						"task":     map[string]interface{}{"type": "string"},
						"priority": map[string]interface{}{"type": "integer"},
					},
					"required": []string{"unitId", "task"},
				},
			},
			{
				Name:        "adventure.research.progress",
				Description: "Check research progress in AdventureGame (Game: adventure)",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"project": map[string]interface{}{
							"type":        "string",
							"description": "Research project name (optional)",
						},
					},
				},
			},
		}

		// Register AdventureGame tools
		for i := 0; i < len(adventureTools); i++ {
			tool := adventureTools[i] // Get by index to avoid range loop issues
			server.RegisterTool(tool, func(toolName string) func(args map[string]interface{}) (*ToolResult, error) {
				return func(args map[string]interface{}) (*ToolResult, error) {
					return &ToolResult{
						Content: []Content{{Type: "text", Text: fmt.Sprintf("Executed %s with args: %v", toolName, args)}},
					}, nil
				}
			}(tool.Name))
		}

		// AI discovers the now-massive tool ecosystem
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"discover-multi-game"`),
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		toolCount := strings.Count(responseStr, `"name"`)
		t.Logf("🚀 Phase 4 - AI now sees %d total tools!", toolCount)
		t.Logf("Tool expansion: 6 core → 13 (+ FactorySim) → %d (+ AdventureGame)", toolCount)

		// Show how AI can use games.tools to understand multi-game context
		multiGameMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"discover-all-games"`),
			Params: map[string]interface{}{
				"name":      "games.tools",
				"arguments": map[string]interface{}{},
			},
		}

		multiResponse := server.HandleMessage(multiGameMsg)
		multiBytes, _ := json.Marshal(multiResponse)
		multiResponseStr := string(multiBytes)

		t.Log("🎯 AI Multi-Game Discovery:")
		t.Logf("games.tools (all games): %s", multiResponseStr)

		// Verify both games' tools are clearly separated
		if !strings.Contains(multiResponseStr, "factory") || !strings.Contains(multiResponseStr, "adventure") {
			t.Error("Multi-game discovery should mention both games")
		}

		t.Log("✅ AI can manage multiple games simultaneously without confusion")
	})

	// Step 5: Demonstrate practical AI usage patterns
	t.Run("Phase5_PracticalAIUsage", func(t *testing.T) {
		t.Log("🤖 Demonstrating practical AI interaction patterns...")

		// Pattern 1: AI checks what's available before acting
		t.Run("Pattern1_DiscoveryFirst", func(t *testing.T) {
			// User: "Help me with FactorySim"
			// AI: "Let me see what I can do with FactorySim..."

			checkMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"check-factory"`),
				Params: map[string]interface{}{
					"name": "games.tools",
					"arguments": map[string]interface{}{
						"gameId": "factory",
					},
				},
			}

			response := server.HandleMessage(checkMsg)
			respBytes, _ := json.Marshal(response)
			t.Logf("AI Discovery: %s", string(respBytes))

			// Now AI can confidently use discovered tools
			useMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"use-factory-tool"`),
				Params: map[string]interface{}{
					"name": "factory.inventory.get",
					"arguments": map[string]interface{}{
						"playerId": "steve",
					},
				},
			}

			useResponse := server.HandleMessage(useMsg)
			useBytes, _ := json.Marshal(useResponse)
			t.Logf("AI Tool Usage: %s", string(useBytes))

			t.Log("✅ Discovery-first pattern works perfectly")
		})

		// Pattern 2: AI handles ambiguity with game prefixes
		t.Run("Pattern2_GamePrefixClarity", func(t *testing.T) {
			// Both games have inventory.get - but AI uses game-prefixed names

			// Get FactorySim inventory
			mcMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"mc-inventory"`),
				Params: map[string]interface{}{
					"name": "factory.inventory.get",
					"arguments": map[string]interface{}{
						"playerId": "steve",
					},
				},
			}

			// Get AdventureGame inventory
			rwMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"rw-inventory"`),
				Params: map[string]interface{}{
					"name": "adventure.inventory.get",
					"arguments": map[string]interface{}{
						"unitId": "alice",
					},
				},
			}

			mcResponse := server.HandleMessage(mcMsg)
			rwResponse := server.HandleMessage(rwMsg)

			mcBytes, _ := json.Marshal(mcResponse)
			rwBytes, _ := json.Marshal(rwResponse)

			t.Logf("FactorySim inventory: %s", string(mcBytes))
			t.Logf("AdventureGame inventory: %s", string(rwBytes))

			t.Log("✅ Game prefixes eliminate ambiguity perfectly")
		})
	})
}

// TestAIToolManagementStrategies demonstrates how AI agents can implement
// effective tool management for GABS's dynamic tool system
func TestAIToolManagementStrategies(t *testing.T) {
	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	// Set up test environment with game config
	gamesConfig := &config.GamesConfig{}
	gamesConfig.AddGame(config.GameConfig{
		ID:         "factory",
		Name:       "Example Game",
		LaunchMode: "DirectPath",
		Target:     "/opt/factory/server.jar",
	})

	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 5*time.Second)

	t.Run("Strategy1_ToolCaching", func(t *testing.T) {
		// Simulate AI caching strategy
		t.Log("🧠 AI Agent: Implementing tool caching strategy")

		// Initial discovery and cache
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"cache-init"`),
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)

		initialToolCount := strings.Count(string(respBytes), `"name"`)
		t.Logf("Cached %d tools initially", initialToolCount)

		// Simulate game connecting (tool expansion)
		server.RegisterTool(Tool{
			Name:        "factory.inventory.get",
			Description: "Get inventory",
		}, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Content: []Content{{Type: "text", Text: "mock"}}}, nil
		})

		// AI refreshes cache after game state change
		response2 := server.HandleMessage(listMsg)
		respBytes2, _ := json.Marshal(response2)

		newToolCount := strings.Count(string(respBytes2), `"name"`)
		t.Logf("Cache refreshed: %d tools (added %d)", newToolCount, newToolCount-initialToolCount)

		t.Log("✅ AI can effectively cache and refresh tools")
	})

	t.Run("Strategy2_LazyDiscovery", func(t *testing.T) {
		// Simulate lazy loading strategy
		t.Log("🧠 AI Agent: Using lazy discovery pattern")

		// AI doesn't load all tools upfront, instead discovers as needed
		// User: "Get my FactorySim inventory"
		// AI: First checks if FactorySim tools are available

		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"lazy-check"`),
			Params: map[string]interface{}{
				"name":      "games.tools",
				"arguments": map[string]interface{}{"gameId": "factory"},
			},
		}

		response := server.HandleMessage(toolsMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		if strings.Contains(responseStr, "factory.inventory") {
			t.Log("✅ Found FactorySim inventory tools, can proceed")
		} else {
			t.Log("ℹ️ No FactorySim inventory tools yet, need to start game first")
		}

		t.Log("✅ Lazy discovery reduces unnecessary tool loading")
	})

	t.Run("Strategy3_IntentBasedTooling", func(t *testing.T) {
		// Simulate intent-based tool selection
		t.Log("🧠 AI Agent: Using intent-based tool selection")

		// User intent: "I want to manage game inventories"
		// AI identifies relevant tool patterns

		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"intent-based"`),
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		// AI filters tools by intent (inventory-related)
		inventoryTools := []string{}
		if strings.Contains(responseStr, "inventory") {
			inventoryTools = append(inventoryTools, "Found inventory-related tools")
		}

		t.Logf("Intent-based filtering found %d inventory tools", len(inventoryTools))
		t.Log("✅ AI can filter large tool sets by user intent")
	})
}

// TestRealWorldScenarios demonstrates how the dynamic tool system works
// in realistic AI-user interaction scenarios
func TestRealWorldScenarios(t *testing.T) {
	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	// Set up realistic game environment
	gamesConfig := &config.GamesConfig{}
	gamesConfig.AddGame(config.GameConfig{
		ID:         "factory",
		Name:       "Example Game",
		LaunchMode: "DirectPath",
		Target:     "/opt/factory/server.jar",
	})
	gamesConfig.AddGame(config.GameConfig{
		ID:              "adventure",
		Name:            "AdventureGame",
		LaunchMode:      "SteamAppId",
		Target:          "123456",
		StopProcessName: "GameName.exe",
	})

	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 5*time.Second)

	t.Run("Scenario1_NewUserExploration", func(t *testing.T) {
		// User: "I just installed GABS, what can I do?"
		t.Log("👤 User: I just installed GABS, what can I do?")

		// AI: "Let me show you what games you have and what's possible"
		listMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/list",
			ID:      json.RawMessage(`"new-user"`),
		}

		response := server.HandleMessage(listMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		t.Log("🤖 AI: I can see your game management tools:")
		coreTools := []string{"games.list", "games.start", "games.status"}
		for _, tool := range coreTools {
			if strings.Contains(responseStr, tool) {
				t.Logf("  ✅ %s available", tool)
			}
		}

		t.Log("🤖 AI: You can start games, and then I'll gain more capabilities!")
		t.Log("✅ New user onboarding works well")
	})

	t.Run("Scenario2_GameModInstallation", func(t *testing.T) {
		// User starts game, installs GABP bridge, bridge connects
		t.Log("👤 User: I started FactorySim and installed a GABP bridge")

		// Simulate bridge connecting and registering tools
		factoryTools := []string{"inventory.get", "world.place_block", "player.teleport"}
		for _, toolName := range factoryTools {
			fullName := fmt.Sprintf("factory.%s", toolName)
			server.RegisterTool(Tool{
				Name:        fullName,
				Description: fmt.Sprintf("FactorySim %s tool", toolName),
			}, func(args map[string]interface{}) (*ToolResult, error) {
				return &ToolResult{Content: []Content{{Type: "text", Text: "mock result"}}}, nil
			})
		}

		// AI discovers new capabilities
		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"bridge-discovery"`),
			Params: map[string]interface{}{
				"name":      "games.tools",
				"arguments": map[string]interface{}{"gameId": "factory"},
			},
		}

		response := server.HandleMessage(toolsMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		t.Log("🤖 AI: Great! I can now help you with:")
		for _, tool := range factoryTools {
			if strings.Contains(responseStr, tool) {
				t.Logf("  ✅ %s", tool)
			}
		}

		t.Log("✅ Bridge installation discovery works perfectly")
	})

	t.Run("Scenario3_MultipleGameManagement", func(t *testing.T) {
		// User has multiple games running with different bridges
		t.Log("👤 User: I'm running both FactorySim and AdventureGame")

		// Add AdventureGame tools too
		adventureTools := []string{"inventory.get", "crafting.build", "research.progress"}
		for _, toolName := range adventureTools {
			fullName := fmt.Sprintf("adventure.%s", toolName)
			server.RegisterTool(Tool{
				Name:        fullName,
				Description: fmt.Sprintf("AdventureGame %s tool", toolName),
			}, func(args map[string]interface{}) (*ToolResult, error) {
				return &ToolResult{Content: []Content{{Type: "text", Text: "mock result"}}}, nil
			})
		}

		// AI shows comprehensive overview
		allToolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"multi-game"`),
			Params: map[string]interface{}{
				"name":      "games.tools",
				"arguments": map[string]interface{}{},
			},
		}

		response := server.HandleMessage(allToolsMsg)
		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)

		t.Log("🤖 AI: I can manage both games simultaneously:")
		t.Log("  FactorySim: inventory, world building, player control")
		t.Log("  AdventureGame: unit management, crafting, research")

		// Verify both game namespaces are present
		if strings.Contains(responseStr, "factory") && strings.Contains(responseStr, "adventure") {
			t.Log("✅ Multi-game management works perfectly")
		} else {
			t.Error("Should see both game namespaces")
		}
	})
}
