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
	// - Minecraft with inventory/get, world/place_block tools
	// - RimWorld with inventory/get, crafting/build tools
	//
	// Current behavior: Tool name conflicts, no way to specify game context
	// Expected after fix: Clear game-specific tool names or context

	logger := util.NewLogger("info")
	server := NewServerForTesting(logger)

	// Simulate what happens when 2 games connect via GABP
	t.Run("SimulateMultipleGameToolRegistration", func(t *testing.T) {
		// Simulate registering tools from different games
		// In reality, Mirror instances would do this

		// Register minecraft tools
		minecraftTools := []Tool{
			{Name: "minecraft.inventory.get", Description: "Get player inventory in Minecraft (Game: minecraft)"},
			{Name: "minecraft.world.place_block", Description: "Place a block in Minecraft world (Game: minecraft)"},
			{Name: "minecraft.player.teleport", Description: "Teleport player in Minecraft (Game: minecraft)"},
		}

		rimworldTools := []Tool{
			{Name: "rimworld.inventory.get", Description: "Get colonist inventory in RimWorld (Game: rimworld)"},
			{Name: "rimworld.crafting.build", Description: "Build items in RimWorld (Game: rimworld)"},
			{Name: "rimworld.player.teleport", Description: "Move colonist in RimWorld (Game: rimworld)"},
		}

		// Register tools as Mirror would do
		for _, tool := range minecraftTools {
			server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Minecraft tool %s executed", tool.Name)}},
				}, nil
			})
		}

		for _, tool := range rimworldTools {
			server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("RimWorld tool %s executed", tool.Name)}},
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
		t.Run("CallMinecraftInventory", func(t *testing.T) {
			callMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"call-minecraft-inventory"`),
				Params: map[string]interface{}{
					"name": "minecraft.inventory.get",
					"arguments": map[string]interface{}{
						"playerId": "steve",
					},
				},
			}

			callResponse := server.HandleMessage(callMsg)
			if callResponse == nil {
				t.Fatal("Expected response from minecraft.inventory.get")
			}

			callBytes, _ := json.Marshal(callResponse)
			t.Logf("Minecraft inventory call result: %s", string(callBytes))
		})

		t.Run("CallRimWorldInventory", func(t *testing.T) {
			callMsg := &Message{
				JSONRPC: "2.0",
				Method:  "tools/call",
				ID:      json.RawMessage(`"call-rimworld-inventory"`),
				Params: map[string]interface{}{
					"name": "rimworld.inventory.get",
					"arguments": map[string]interface{}{
						"playerId": "colonist1",
					},
				},
			}

			callResponse := server.HandleMessage(callMsg)
			if callResponse == nil {
				t.Fatal("Expected response from rimworld.inventory.get")
			}

			callBytes, _ := json.Marshal(callResponse)
			t.Logf("RimWorld inventory call result: %s", string(callBytes))
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
			"minecraft": {
				ID:         "minecraft",
				Name:       "Minecraft",
				LaunchMode: "DirectPath",
				Target:     "/opt/minecraft/start.sh",
			},
			"rimworld": {
				ID:         "rimworld",
				Name:       "RimWorld",
				LaunchMode: "SteamAppId",
				Target:     "294100",
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
	minecraftTools := []Tool{
		{Name: "minecraft.inventory.get", Description: "Get player inventory in Minecraft (Game: minecraft)"},
		{Name: "minecraft.world.place_block", Description: "Place a block in Minecraft world (Game: minecraft)"},
	}

	rimworldTools := []Tool{
		{Name: "rimworld.inventory.get", Description: "Get colonist inventory in RimWorld (Game: rimworld)"},
		{Name: "rimworld.crafting.build", Description: "Build items in RimWorld (Game: rimworld)"},
	}

	// Register tools
	for _, tool := range minecraftTools {
		server.RegisterTool(tool, func(args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Content: []Content{{Type: "text", Text: "Tool executed"}}}, nil
		})
	}

	for _, tool := range rimworldTools {
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

		// Should mention both minecraft and rimworld tools
		if !strings.Contains(responseStr, "minecraft.inventory.get") {
			t.Error("Expected to see minecraft tools")
		}
		if !strings.Contains(responseStr, "rimworld.inventory.get") {
			t.Error("Expected to see rimworld tools")
		}
	})

	t.Run("ListMinecraftToolsOnly", func(t *testing.T) {
		// Test games.tools with specific gameId
		toolsMsg := &Message{
			JSONRPC: "2.0",
			Method:  "tools/call",
			ID:      json.RawMessage(`"list-minecraft-tools"`),
			Params: map[string]interface{}{
				"name": "games.tools",
				"arguments": map[string]interface{}{
					"gameId": "minecraft",
				},
			},
		}

		response := server.HandleMessage(toolsMsg)
		if response == nil {
			t.Fatal("Expected response from games.tools")
		}

		respBytes, _ := json.Marshal(response)
		responseStr := string(respBytes)
		t.Logf("games.tools (minecraft only): %s", responseStr)

		// Should mention minecraft tools but not rimworld
		if !strings.Contains(responseStr, "minecraft.inventory.get") {
			t.Error("Expected to see minecraft tools")
		}
		if strings.Contains(responseStr, "rimworld.inventory.get") {
			t.Error("Should not see rimworld tools when filtering for minecraft")
		}
	})
}

// TestProposedSolution demonstrates how the fix should work
func TestProposedSolution(t *testing.T) {
	// This test shows how we can fix the multi-game mirroring issue

	t.Run("GamePrefixedTools", func(t *testing.T) {
		// Strategy 1: Prefix all tools with game ID
		// inventory/get becomes:
		// - minecraft.inventory.get
		// - rimworld.inventory.get

		// This makes it clear to AI which game each tool belongs to
		// AI can then explicitly choose: "use minecraft.inventory.get to get Steve's inventory"
		t.Log("Strategy 1: Game-prefixed tool names")
		t.Log("  minecraft.inventory.get - Get player inventory in Minecraft")
		t.Log("  rimworld.inventory.get - Get colonist inventory in RimWorld")
		t.Log("  AI can clearly specify which game to target")
	})

	t.Run("GameContextParameter", func(t *testing.T) {
		// Strategy 2: Add gameId parameter to all mirrored tools
		// inventory/get with parameters: {gameId: "minecraft", playerId: "steve"}
		// This allows keeping original tool names while adding game context

		t.Log("Strategy 2: Game context parameter")
		t.Log("  inventory/get with {gameId: 'minecraft', playerId: 'steve'}")
		t.Log("  inventory/get with {gameId: 'rimworld', playerId: 'colonist1'}")
		t.Log("  AI adds gameId parameter to specify target game")
	})

	t.Run("GameSpecificToolSets", func(t *testing.T) {
		// Strategy 3: Only register tools for the currently "active" game
		// games.switch tool to change active game context
		// This is simpler but requires switching context

		t.Log("Strategy 3: Single active game context")
		t.Log("  games.switch {'gameId': 'minecraft'}")
		t.Log("  Now all tools (inventory_get, world_place_block) apply to Minecraft")
		t.Log("  games.switch {'gameId': 'rimworld'}")
		t.Log("  Now all tools (inventory_get, crafting_build) apply to RimWorld")
	})

	// I think Strategy 1 (game-prefixed tools) is clearest for AI
	// It's explicit, no hidden context, and AI can see all available options
}
