package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
	"github.com/pardeike/gabs/internal/version"
)

// Server runs MCP over stdio.
type Server struct {
	log         util.Logger
	tools       map[string]*ToolHandler
	resources   map[string]*ResourceHandler
	games       map[string]*process.Controller // Track running games
	configDir   string                        // Config directory for bridge files
	apiKey      string                        // API key for HTTP authentication
	mu          sync.RWMutex
	writers     []util.FrameWriter           // Track client connections for notifications
	writersMu   sync.RWMutex                // Protect writers slice
	gameTools   map[string][]string         // Track which tools belong to which games
	gameResources map[string][]string       // Track which resources belong to which games
	gabpClients map[string]*gabp.Client     // Track GABP connections per game
}

// ToolHandler represents a tool handler function
type ToolHandler struct {
	Tool    Tool
	Handler func(args map[string]interface{}) (*ToolResult, error)
}

// ResourceHandler represents a resource handler function
type ResourceHandler struct {
	Resource Resource
	Handler  func() ([]Content, error)
}

func NewServer(log util.Logger) *Server {
	return &Server{
		log:           log,
		tools:         make(map[string]*ToolHandler),
		resources:     make(map[string]*ResourceHandler),
		games:         make(map[string]*process.Controller),
		configDir:     "", // Will be set by SetConfigDir
		writers:       make([]util.FrameWriter, 0),
		gameTools:     make(map[string][]string),
		gameResources: make(map[string][]string),
		gabpClients:   make(map[string]*gabp.Client),
	}
}

// RegisterTool registers a tool with its handler, applying normalization if configured
func (s *Server) RegisterTool(tool Tool, handler func(args map[string]interface{}) (*ToolResult, error)) {
	s.RegisterToolWithConfig(tool, handler, nil)
}

// RegisterToolWithConfig registers a tool with its handler, applying normalization based on config
func (s *Server) RegisterToolWithConfig(tool Tool, handler func(args map[string]interface{}) (*ToolResult, error), normalizationConfig *config.ToolNormalizationConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Apply normalization if configured
	registeredTool := tool
	if normalizationConfig != nil && normalizationConfig.EnableOpenAINormalization {
		normalizedResult := util.NormalizeToolNameForOpenAI(tool.Name, normalizationConfig.MaxToolNameLength)
		
		if normalizedResult.WasNormalized {
			// Store original name in metadata
			if registeredTool.Meta == nil {
				registeredTool.Meta = make(map[string]interface{})
			}
			registeredTool.Meta["originalName"] = normalizedResult.OriginalName
			
			// Update the tool name to the normalized version
			registeredTool.Name = normalizedResult.NormalizedName
			
			// Optionally preserve original name in description
			if normalizationConfig.PreserveOriginalName && registeredTool.Description != "" {
				registeredTool.Description = fmt.Sprintf("%s (Original: %s)", registeredTool.Description, normalizedResult.OriginalName)
			}
			
			s.log.Debugw("normalized tool name for OpenAI compatibility", 
				"original", normalizedResult.OriginalName, 
				"normalized", normalizedResult.NormalizedName)
		}
	}

	s.tools[registeredTool.Name] = &ToolHandler{
		Tool:    registeredTool,
		Handler: handler,
	}
}

// RegisterResource registers a resource with its handler
func (s *Server) RegisterResource(resource Resource, handler func() ([]Content, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[resource.URI] = &ResourceHandler{
		Resource: resource,
		Handler:  handler,
	}
}

// SetConfigDir sets the configuration directory for bridge files
func (s *Server) SetConfigDir(configDir string) {
	s.configDir = configDir
}

// SetAPIKey sets the API key for HTTP authentication
func (s *Server) SetAPIKey(apiKey string) {
	s.apiKey = apiKey
}

// RegisterGameManagementTools registers the game management tools for the new architecture
func (s *Server) RegisterGameManagementTools(gamesConfig *config.GamesConfig, backoffMin, backoffMax time.Duration) {
	normalizationConfig := gamesConfig.GetToolNormalization()

	// games.list tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.list",
		Description: "List all configured game IDs",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		games := gamesConfig.ListGames()

		var content strings.Builder
		if len(games) == 0 {
			content.WriteString("No games configured. Use the CLI to add games: gabs games add <id>")
		} else {
			for i, game := range games {
				if i > 0 {
					content.WriteString("\n")
				}
				content.WriteString(game.ID)
			}
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: content.String()}},
		}, nil
	}, normalizationConfig)

	// games.show tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.show",
		Description: "Show detailed configuration and validation status for a specific game",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to show details for",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, exists := s.resolveGameId(gamesConfig, gameIdOrTarget)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameIdOrTarget)}},
				IsError: true,
			}, nil
		}

		var content strings.Builder
		content.WriteString(fmt.Sprintf("Game Configuration: %s\n\n", game.ID))
		content.WriteString(fmt.Sprintf("  ID: %s (%s)\n", game.ID, game.Name))
		content.WriteString(fmt.Sprintf("  Use gameId: '%s' (or target: '%s')\n", game.ID, game.Target))
		content.WriteString(fmt.Sprintf("  Launch: %s\n", game.LaunchMode))
		
		if game.WorkingDir != "" {
			content.WriteString(fmt.Sprintf("  Working Directory: %s\n", game.WorkingDir))
		}
		if len(game.Args) > 0 {
			content.WriteString(fmt.Sprintf("  Arguments: %s\n", strings.Join(game.Args, " ")))
		}
		
		// Validation status for launcher-based games
		if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
			content.WriteString("\nGame Termination Configuration:\n")
			if game.StopProcessName != "" {
				content.WriteString(fmt.Sprintf("  ✓ Configured for proper game termination (process: %s)\n", game.StopProcessName))
			} else {
				content.WriteString(fmt.Sprintf("  ⚠️  Missing stopProcessName - GABS can start but cannot properly stop %s games\n", game.LaunchMode))
				content.WriteString(fmt.Sprintf("     Add stopProcessName to your game configuration for proper termination.\n"))
			}
		} else if game.StopProcessName != "" {
			content.WriteString(fmt.Sprintf("  Stop Process Name: %s\n", game.StopProcessName))
		}
		
		if game.Description != "" {
			content.WriteString(fmt.Sprintf("\nDescription: %s\n", game.Description))
		}
		
		return &ToolResult{
			Content: []Content{{Type: "text", Text: content.String()}},
		}, nil
	}, normalizationConfig)

	// games.status tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.status",
		Description: "Check the status of one or more games using game ID or launch target",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to check (optional, checks all if not provided)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameIdOrTarget, hasGameID := args["gameId"].(string)

		var content strings.Builder
		if hasGameID {
			// Check specific game
			game, exists := s.resolveGameId(gamesConfig, gameIdOrTarget)
			if !exists {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameIdOrTarget)}},
					IsError: true,
				}, nil
			}

			statusDesc := s.getStatusDescription(game.ID, game)
			content.WriteString(fmt.Sprintf("**%s** (%s): %s\n", game.ID, game.Name, statusDesc))

			// Add helpful info for launcher games ONLY when we cannot track them
			if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
				status := s.checkGameStatus(game.ID)
				if status == "launcher-triggered" {
					// Only show the warning if we don't have stopProcessName configured
					if game.StopProcessName == "" {
						content.WriteString(fmt.Sprintf("\nNote: %s game was launched, but GABS cannot track whether it's still running because no 'stopProcessName' is configured.\nCheck Steam/Epic or your system processes to verify the actual game status.\n", game.LaunchMode))
					}
				}
			}
		} else {
			// Check all games
			games := gamesConfig.ListGames()
			content.WriteString("Game Status Summary:\n\n")
			for _, game := range games {
				statusDesc := s.getStatusDescription(game.ID, &game)
				content.WriteString(fmt.Sprintf("• **%s**: %s\n", game.ID, statusDesc))
			}
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: content.String()}},
		}, nil
	}, normalizationConfig)

	// games.start tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.start",
		Description: "Start a configured game using game ID or launch target (e.g., Steam App ID)",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target (Steam App ID, path, etc.)",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, exists := s.resolveGameId(gamesConfig, gameIdOrTarget)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameIdOrTarget)}},
				IsError: true,
			}, nil
		}

		err := s.startGame(*game, backoffMin, backoffMax)
		if err != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to start %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) started successfully", game.ID, game.Name)}},
		}, nil
	}, normalizationConfig)

	// games.stop tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.stop",
		Description: "Gracefully stop a running game using game ID or launch target",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to stop",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, exists := s.resolveGameId(gamesConfig, gameIdOrTarget)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameIdOrTarget)}},
				IsError: true,
			}, nil
		}

		err := s.stopGame(*game, false)
		if err != nil {
			// Check if this is a launcher-specific configuration issue
			if strings.Contains(err.Error(), "Configure 'stopProcessName'") {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("⚠️ %s\n\nTo fix this, update your game configuration to include a 'stopProcessName'. Use: gabs games show %s", err.Error(), game.ID)}},
					IsError: true,
				}, nil
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to stop %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) stopped successfully", game.ID, game.Name)}},
		}, nil
	}, normalizationConfig)

	// games.kill tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.kill",
		Description: "Force terminate a running game using game ID or launch target",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to force terminate",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, exists := s.resolveGameId(gamesConfig, gameIdOrTarget)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameIdOrTarget)}},
				IsError: true,
			}, nil
		}

		err := s.stopGame(*game, true)
		if err != nil {
			// Check if this is a launcher-specific configuration issue
			if strings.Contains(err.Error(), "Configure 'stopProcessName'") {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("⚠️ %s\n\nTo fix this, update your game configuration to include a 'stopProcessName'. Use: gabs games show %s", err.Error(), game.ID)}},
					IsError: true,
				}, nil
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to kill %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) terminated successfully", game.ID, game.Name)}},
		}, nil
	}, normalizationConfig)

	// games.tools tool - List tools available for specific games
	s.RegisterToolWithConfig(Tool{
		Name:        "games.tools",
		Description: "List game-specific tools available from running games with GABP connections",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to list tools for (optional, lists all if not provided)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameId, hasGameID := args["gameId"].(string)

		var content strings.Builder

		if hasGameID {
			// List tools for specific game
			game, exists := s.resolveGameId(gamesConfig, gameId)
			if !exists {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameId)}},
					IsError: true,
				}, nil
			}

			content.WriteString(fmt.Sprintf("Tools for game '%s':\n\n", game.ID))
			// Get tools that start with this game's prefix
			gameTools := s.getGameSpecificTools(game.ID)
			if len(gameTools) == 0 {
				content.WriteString(fmt.Sprintf("No GABP tools available for this game.\n"))
				status := s.checkGameStatus(game.ID)
				if status != "running" {
					content.WriteString(fmt.Sprintf("Game is currently '%s'. Start it with games.start to enable GABP tools.\n", status))
				}
			} else {
				for _, tool := range gameTools {
					content.WriteString(fmt.Sprintf("• **%s** - %s\n", tool.Name, tool.Description))
				}
			}
		} else {
			// List tools for all games
			content.WriteString("Game-Specific Tools Available:\n\n")
			games := gamesConfig.ListGames()

			hasAnyTools := false
			for _, game := range games {
				gameTools := s.getGameSpecificTools(game.ID)
				if len(gameTools) > 0 {
					hasAnyTools = true
					status := s.checkGameStatus(game.ID)
					content.WriteString(fmt.Sprintf("**%s** (%s, %d tools):\n", game.ID, status, len(gameTools)))
					for _, tool := range gameTools {
						content.WriteString(fmt.Sprintf("  • %s - %s\n", tool.Name, tool.Description))
					}
					content.WriteString("\n")
				}
			}

			if !hasAnyTools {
				content.WriteString("No game-specific tools available.\n")
				content.WriteString("Start games with GABP-compliant mods to see their tools.\n")
			}

			content.WriteString("\nNote: Tools are prefixed with game ID (e.g., 'minecraft.inventory.get') to avoid conflicts between games.\n")
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: content.String()}},
		}, nil
	}, normalizationConfig)
}

// RegisterBridgeTools registers the legacy bridge management tools (for compatibility)
func (s *Server) RegisterBridgeTools(ctrl interface{}, client interface{}) {
	// Legacy bridge tools - kept for compatibility but not used in new architecture
	// In the new architecture, game management is done through games.* tools
}

// getGameFromController extracts game config from controller - helper for status checking
func (s *Server) getGameFromController(controller *process.Controller) *config.GameConfig {
	// This is a temporary helper. In a proper refactor, we'd store the game config 
	// alongside the controller, but for minimal changes, we'll work with what we have.
	// We can check the controller's spec to get the StopProcessName
	if controller == nil {
		return nil
	}
	
	// Create a minimal game config with the info we need
	return &config.GameConfig{
		StopProcessName: controller.GetStopProcessName(),
	}
}

// resolveGameId tries to find a game by ID or by target (for better UX)
// Returns the actual game config and whether it was found
func (s *Server) resolveGameId(gamesConfig *config.GamesConfig, gameIdOrTarget string) (*config.GameConfig, bool) {
	// First try direct lookup by game ID
	if game, exists := gamesConfig.GetGame(gameIdOrTarget); exists {
		return game, true
	}

	// If not found, try to find by target (Steam App ID, path, etc.)
	for _, game := range gamesConfig.ListGames() {
		if game.Target == gameIdOrTarget {
			return &game, true
		}
	}

	return nil, false
}

// getGameSpecificTools returns tools that belong to a specific game
func (s *Server) getGameSpecificTools(gameID string) []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var gameTools []Tool
	prefix := gameID + "."

	for toolName, handler := range s.tools {
		if strings.HasPrefix(toolName, prefix) {
			gameTools = append(gameTools, handler.Tool)
		}
	}

	return gameTools
}

// checkGameStatus returns the current status of a game
// getStatusDescription provides a user-friendly description of the game status
func (s *Server) getStatusDescription(gameID string, gameConfig *config.GameConfig) string {
	status := s.checkGameStatus(gameID)

	switch status {
	case "running":
		// Check if this is a launcher-based game with process tracking
		if gameConfig.LaunchMode == "SteamAppId" || gameConfig.LaunchMode == "EpicAppId" {
			if gameConfig.StopProcessName != "" {
				return "running (GABS is tracking the game process)"
			}
		}
		return "running (GABS controls the process)"
	case "stopped":
		return "stopped"
	case "launcher-running":
		return fmt.Sprintf("launcher active (game may be starting via %s)", gameConfig.LaunchMode)
	case "launcher-triggered":
		return fmt.Sprintf("launched via %s (GABS cannot track the game process - no stopProcessName configured)", gameConfig.LaunchMode)
	default:
		return status
	}
}

func (s *Server) checkGameStatus(gameID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	controller, exists := s.games[gameID]
	if !exists {
		return "stopped"
	}

	launchMode := controller.GetLaunchMode()

	// For Steam/Epic launcher games, we use different status reporting
	if launchMode == "SteamAppId" || launchMode == "EpicAppId" {
		// Check if we can track the actual game process
		if controller.IsRunning() {
			return "running" // We can track it and it's running
		} else {
			// Check if the launcher process is still active (shouldn't normally happen)
			if controller.IsLauncherProcessRunning() {
				return "launcher-running" // Launcher process is still active
			}
			
			// Launcher has exited (normal) - determine if we have tracking capability
			game := s.getGameFromController(controller)
			if game != nil && game.StopProcessName != "" {
				// We have tracking capability but game is not running
				return "stopped"
			} else {
				// We don't have tracking capability, so we can't know the real status
				return "launcher-triggered" // We started the launcher, but can't track the game
			}
		}
	}

	// For direct processes, check if the process is actually running
	if controller != nil && controller.IsRunning() {
		return "running"
	}

	// Process is dead, clean up
	delete(s.games, gameID)
	// Cleanup GABP connections and mirrored tools for this game
	// This involves:
	// 1. Disconnecting any active GABP client for this game
	// 2. Unregistering all game-specific tools (gameId.* tools)
	// 3. Cleaning up bridge configuration files
	s.CleanupGABPConnection(gameID)
	s.CleanupGameResources(gameID)
	s.CleanupBridgeConfig(gameID)
	s.log.Debugw("cleaned up dead game process and resources", "gameId", gameID)

	return "stopped"
}

// startGame starts a game process using the process controller and sets up GABP bridge
func (s *Server) startGame(game config.GameConfig, backoffMin, backoffMax time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already running
	if controller, exists := s.games[game.ID]; exists && controller != nil && controller.IsRunning() {
		return fmt.Errorf("game %s is already running", game.ID)
	}

	// Clean up any stale controller reference
	delete(s.games, game.ID)

	// Create GABP bridge configuration (always local for GABS)
	port, token, bridgePath, err := config.WriteBridgeJSON(game.ID, s.configDir)
	if err != nil {
		return fmt.Errorf("failed to create bridge config: %w", err)
	}

	s.log.Infow("created GABP bridge configuration", "gameId", game.ID, "port", port, "token", token[:8]+"...", "host", "127.0.0.1", "configPath", bridgePath)

	// Convert GameConfig to LaunchSpec
	launchSpec := process.LaunchSpec{
		GameId:          game.ID,
		Mode:            game.LaunchMode,
		PathOrId:        game.Target,
		Args:            game.Args,
		WorkingDir:      game.WorkingDir,
		StopProcessName: game.StopProcessName,
	}

	// Create and configure controller
	controller := &process.Controller{}
	if err := controller.Configure(launchSpec); err != nil {
		return fmt.Errorf("failed to configure game launcher: %w", err)
	}

	// Set bridge connection info for environment variables
	controller.SetBridgeInfo(port, token)

	// Start the game
	if err := controller.Start(); err != nil {
		return fmt.Errorf("failed to start game: %w", err)
	}

	// Track the running game
	s.games[game.ID] = controller

	s.log.Infow("game started with GABP bridge", "gameId", game.ID, "mode", game.LaunchMode, "pid", controller.GetPID(), "gabpPort", port)

	// Start GABP connection attempt in background with retry logic
	// This ensures AI agents are notified when tool sets expand dynamically
	go s.establishGABPConnection(game.ID, port, token, backoffMin, backoffMax)

	return nil
}

// establishGABPConnection attempts to connect to the game's GABP server with retry logic
// This runs in the background and implements the Future Enhancement workflow:
// 1. Game starts with bridge config (already done in startGame)
// 2. GABP client connects to game mod's server (implemented here)
// 3. Mirror system syncs tools and sends tools/list_changed notification (implemented here)
// 4. AI agents automatically discover new capabilities via games.tools (enabled by notification)
func (s *Server) establishGABPConnection(gameID string, port int, token string, backoffMin, backoffMax time.Duration) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	s.log.Debugw("attempting GABP connection for game", "gameId", gameID, "addr", addr)

	// Create GABP client
	client := gabp.NewClient(s.log)

	// Store client reference for cleanup
	s.mu.Lock()
	s.gabpClients[gameID] = client
	s.mu.Unlock()

	// Attempt connection with retry logic (handles game mod startup delays)
	err := client.Connect(addr, token, backoffMin, backoffMax)
	if err != nil {
		s.log.Warnw("failed to establish GABP connection - game may not support GABP", 
			"gameId", gameID, "addr", addr, "error", err)
		
		// Clean up client reference on failure
		s.mu.Lock()
		delete(s.gabpClients, gameID)
		s.mu.Unlock()
		return
	}

	s.log.Infow("GABP connection established successfully", "gameId", gameID, "addr", addr)

	// Sync tools from GABP to MCP (inline mirroring logic)
	if err := s.syncGABPTools(client, gameID); err != nil {
		s.log.Warnw("failed to sync GABP tools", "gameId", gameID, "error", err)
	} else {
		s.log.Infow("GABP tools synchronized successfully", "gameId", gameID)
	}

	// Expose GABP resources as MCP resources (inline mirroring logic)
	if err := s.exposeGABPResources(client, gameID); err != nil {
		s.log.Warnw("failed to expose GABP resources", "gameId", gameID, "error", err)
	} else {
		s.log.Infow("GABP resources exposed successfully", "gameId", gameID)
	}

	s.log.Infow("GABP mirroring setup complete for game", "gameId", gameID)
}

// syncGABPTools mirrors GABP tools to MCP tools with game-specific naming
func (s *Server) syncGABPTools(client *gabp.Client, gameID string) error {
	// Get tools from GABP client
	gabpTools, err := client.ListTools()
	if err != nil {
		return fmt.Errorf("failed to list GABP tools: %w", err)
	}

	// Register each GABP tool as an MCP tool with game-specific naming
	for _, tool := range gabpTools {
		// Create game-prefixed tool name for multi-game clarity
		// Apply basic normalization first (convert slashes to dots)
		sanitizedToolName := util.NormalizeToolNameBasic(tool.Name)
		gameSpecificName := fmt.Sprintf("%s.%s", gameID, sanitizedToolName)

		mcpTool := Tool{
			Name:        gameSpecificName,
			Description: fmt.Sprintf("%s (Game: %s)", tool.Description, gameID),
			InputSchema: tool.InputSchema,
		}

		// Create handler that forwards to GABP with original tool name
		originalToolName := tool.Name // Capture original name for GABP call
		handler := func(toolName string) func(args map[string]interface{}) (*ToolResult, error) {
			return func(args map[string]interface{}) (*ToolResult, error) {
				// Call GABP with original tool name (without game prefix)
				result, isError, err := client.CallTool(toolName, args)
				if err != nil {
					return &ToolResult{
						Content: []Content{{Type: "text", Text: err.Error()}},
						IsError: true,
					}, nil
				}

				if isError {
					return &ToolResult{
						Content:           []Content{{Type: "text", Text: fmt.Sprintf("Tool error: %v", result)}},
						StructuredContent: result,
						IsError:           true,
					}, nil
				}

				// Convert result to MCP format
				content := []Content{}
				if resultText, ok := result["text"].(string); ok {
					content = append(content, Content{Type: "text", Text: resultText})
				} else {
					// Serialize non-text tool results as JSON instead of using %v
					if jsonData, err := json.Marshal(result); err != nil {
						// Fallback to string representation if JSON marshaling fails
						content = append(content, Content{Type: "text", Text: fmt.Sprintf("Tool result (JSON marshal failed): %v", result)})
					} else {
						content = append(content, Content{Type: "text", Text: string(jsonData)})
					}
				}

				return &ToolResult{
					Content:           content,
					StructuredContent: result,
					IsError:           false,
				}, nil
			}
		}(originalToolName)

		// Register the tool using the existing game tool registration method
		// Use empty normalization config for now
		normalizationConfig := &config.ToolNormalizationConfig{}
		s.RegisterGameTool(gameID, mcpTool, handler, normalizationConfig)
		s.log.Debugw("registered GABP tool as game-specific MCP tool", "gameId", gameID, "originalName", tool.Name, "mcpName", gameSpecificName)
	}

	s.log.Infow("synced GABP tools to MCP with game namespacing", "gameId", gameID, "count", len(gabpTools))

	// Send tools/list_changed notification to AI agents
	// This automatically alerts AI agents that new tools are available without
	// them needing to poll. AI agents can then use games.tools to discover the
	// new capabilities.
	s.SendToolsListChangedNotification()

	return nil
}

// exposeGABPResources creates MCP resources that expose GABP game information
func (s *Server) exposeGABPResources(client *gabp.Client, gameID string) error {
	// Game state resource for exposing current game information
	stateResource := Resource{
		URI:         fmt.Sprintf("gab://%s/state", gameID),
		Name:        fmt.Sprintf("%s Game State", gameID),
		Description: fmt.Sprintf("Current state and capabilities of game: %s", gameID),
		MimeType:    "application/json",
	}

	stateHandler := func() ([]Content, error) {
		// Get current tools to show game capabilities
		tools, err := client.ListTools()
		if err != nil {
			return []Content{
				{Type: "text", Text: fmt.Sprintf("Error retrieving game state: %v", err)},
			}, nil
		}

		stateData := map[string]interface{}{
			"gameId":       gameID,
			"connected":    true,
			"toolCount":    len(tools),
			"capabilities": client.GetCapabilities(),
			"availableTools": func() []string {
				var toolNames []string
				for _, tool := range tools {
					toolNames = append(toolNames, tool.Name)
				}
				return toolNames
			}(),
			"lastUpdate": fmt.Sprintf("%d", time.Now().Unix()),
		}

		stateJson, err := json.Marshal(stateData)
		if err != nil {
			return []Content{
				{Type: "text", Text: fmt.Sprintf("Error marshaling state data: %v", err)},
			}, err
		}

		return []Content{
			{Type: "text", Text: string(stateJson)},
		}, nil
	}

	// Register the resource using the existing game resource registration method
	s.RegisterGameResource(gameID, stateResource, stateHandler)

	s.log.Infow("exposed GABP resources as game-specific MCP resources", "gameId", gameID, "resources", []string{"state"})

	// Send resources/list_changed notification to alert AI agents
	s.SendResourcesListChangedNotification()

	return nil
}

// stopGame stops a game process gracefully or by force
func (s *Server) stopGame(game config.GameConfig, force bool) error {
	s.mu.Lock()
	controller, exists := s.games[game.ID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("game %s is not running (no process tracked)", game.ID)
	}

	launchMode := controller.GetLaunchMode()

	// Remove from tracking immediately to prevent double-stops
	delete(s.games, game.ID)
	s.mu.Unlock()

	// Handle different launch modes differently
	if launchMode == "SteamAppId" || launchMode == "EpicAppId" {
		// For Steam/Epic games, try to use stopProcessName first if available
		if game.StopProcessName != "" {
			// Try to stop by process name first
			if err := controller.Stop(3 * time.Second); err == nil {
				s.log.Infow("game stopped via process name", "gameId", game.ID, "processName", game.StopProcessName)
				return nil
			}
		}
		
		// Fall back to stopping the launcher process
		var err error
		if force {
			err = controller.Kill()
		} else {
			err = controller.Stop(3 * time.Second)
		}

		if err != nil {
			s.log.Infow("launcher process stop failed (may have already exited)", "gameId", game.ID, "mode", launchMode, "error", err)
		} else {
			s.log.Infow("launcher process stopped", "gameId", game.ID, "mode", launchMode, "pid", controller.GetPID())
		}
		
		// If we have stopProcessName configured, we should have been able to stop the game properly
		if game.StopProcessName != "" {
			return nil // Process was handled by stopProcessName logic above
		}
		
		// Only show the confusing message if stopProcessName is not configured
		return fmt.Errorf("launcher process stopped, but the actual %s game may still be running independently. Configure 'stopProcessName' in the game configuration to enable proper game termination", launchMode)
	}

	// For direct processes, stop normally
	var err error
	if force {
		err = controller.Kill()
		s.log.Infow("game killed", "gameId", game.ID, "pid", controller.GetPID())
	} else {
		// Use default grace period of 3 seconds
		err = controller.Stop(3 * time.Second)
		s.log.Infow("game stopped", "gameId", game.ID, "pid", controller.GetPID())
	}

	// Cleanup GABP connections and mirrored tools when game stops
	// This involves:
	// 1. Disconnecting any active GABP client for this game
	// 2. Unregistering all game-specific tools (gameId.* tools)
	// 3. Cleaning up bridge configuration files
	s.CleanupGABPConnection(game.ID)
	s.CleanupGameResources(game.ID)
	s.CleanupBridgeConfig(game.ID)

	return err
}

func (s *Server) ServeStdio(ctx context.Context) error {
	return s.Serve(os.Stdin, os.Stdout)
}

// SendNotification sends a notification to all connected clients
func (s *Server) SendNotification(method string, params interface{}) {
	notification := NewNotification(method, params)
	
	s.writersMu.RLock()
	defer s.writersMu.RUnlock()
	
	for _, writer := range s.writers {
		if err := writer.WriteJSON(notification); err != nil {
			s.log.Warnw("failed to send notification", "method", method, "error", err)
		}
	}
}

// SendToolsListChangedNotification notifies clients that the tool list has changed
func (s *Server) SendToolsListChangedNotification() {
	s.SendNotification("notifications/tools/list_changed", map[string]interface{}{})
	s.log.Debugw("sent tools/list_changed notification")
}

// SendResourcesListChangedNotification notifies clients that the resource list has changed
func (s *Server) SendResourcesListChangedNotification() {
	s.SendNotification("notifications/resources/list_changed", map[string]interface{}{})
	s.log.Debugw("sent resources/list_changed notification")
}

// RegisterGameTool registers a tool for a specific game and tracks it for cleanup
func (s *Server) RegisterGameTool(gameId string, tool Tool, handler func(args map[string]interface{}) (*ToolResult, error), normalizationConfig *config.ToolNormalizationConfig) {
	s.RegisterToolWithConfig(tool, handler, normalizationConfig)
	
	// Track which game this tool belongs to
	s.mu.Lock()
	s.gameTools[gameId] = append(s.gameTools[gameId], tool.Name)
	s.mu.Unlock()
}

// RegisterGameResource registers a resource for a specific game and tracks it for cleanup
func (s *Server) RegisterGameResource(gameId string, resource Resource, handler func() ([]Content, error)) {
	s.RegisterResource(resource, handler)
	
	// Track which game this resource belongs to
	s.mu.Lock()
	s.gameResources[gameId] = append(s.gameResources[gameId], resource.URI)
	s.mu.Unlock()
}

// CleanupGameResources removes all tools and resources for a specific game
func (s *Server) CleanupGameResources(gameId string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	toolsRemoved := 0
	resourcesRemoved := 0
	
	// Remove game-specific tools
	if toolNames, exists := s.gameTools[gameId]; exists {
		for _, toolName := range toolNames {
			if _, exists := s.tools[toolName]; exists {
				delete(s.tools, toolName)
				toolsRemoved++
			}
		}
		delete(s.gameTools, gameId)
	}
	
	// Remove game-specific resources
	if resourceURIs, exists := s.gameResources[gameId]; exists {
		for _, resourceURI := range resourceURIs {
			if _, exists := s.resources[resourceURI]; exists {
				delete(s.resources, resourceURI)
				resourcesRemoved++
			}
		}
		delete(s.gameResources, gameId)
	}
	
	if toolsRemoved > 0 || resourcesRemoved > 0 {
		s.log.Infow("cleaned up game resources", "gameId", gameId, "toolsRemoved", toolsRemoved, "resourcesRemoved", resourcesRemoved)
		
		// Notify clients about changes
		if toolsRemoved > 0 {
			s.SendToolsListChangedNotification()
		}
		if resourcesRemoved > 0 {
			s.SendResourcesListChangedNotification()
		}
	}
}

// CleanupGABPConnection closes the GABP connection for a game
func (s *Server) CleanupGABPConnection(gameId string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clean up GABP client connection
	if client, exists := s.gabpClients[gameId]; exists {
		if err := client.Close(); err != nil {
			s.log.Warnw("error closing GABP client", "gameId", gameId, "error", err)
		}
		delete(s.gabpClients, gameId)
		s.log.Debugw("cleaned up GABP client connection", "gameId", gameId)
	}
}

// CleanupBridgeConfig removes the bridge configuration file for a game
func (s *Server) CleanupBridgeConfig(gameId string) {
	cp, err := config.NewConfigPaths(s.configDir)
	if err != nil {
		s.log.Warnw("failed to create config paths for cleanup", "gameId", gameId, "error", err)
		return
	}
	
	bridgePath := cp.GetBridgeConfigPath(gameId)
	
	if err := os.Remove(bridgePath); err != nil {
		// Don't log as error since file might not exist
		s.log.Debugw("bridge config cleanup", "gameId", gameId, "path", bridgePath, "result", err.Error())
	} else {
		s.log.Debugw("cleaned up bridge config", "gameId", gameId, "path", bridgePath)
	}
}

func (s *Server) Serve(r io.Reader, w io.Writer) error {
	// Implement newline-delimited JSON-RPC over stdio per MCP stdio transport
	reader := util.NewNewlineFrameReader(r)
	writer := util.NewNewlineFrameWriter(w)

	// Track this writer for notifications
	s.writersMu.Lock()
	s.writers = append(s.writers, writer)
	s.writersMu.Unlock()

	// Clean up writer on exit
	defer func() {
		s.writersMu.Lock()
		// Find and remove writer from slice (safer than using index)
		for i, w := range s.writers {
			if w == writer {
				s.writers = append(s.writers[:i], s.writers[i+1:]...)
				break
			}
		}
		s.writersMu.Unlock()
	}()

	for {
		var msg Message
		if err := reader.ReadJSON(&msg); err != nil {
			if err == io.EOF {
				break
			}
			s.log.Errorw("failed to read message", "error", err)
			continue
		}

		s.log.Debugw("received message", "method", msg.Method, "id", msg.ID)

		response := s.handleMessage(&msg)
		if response != nil {
			if err := writer.WriteJSON(response); err != nil {
				s.log.Errorw("failed to write response", "error", err)
				return err
			}
		}
	}

	return nil
}

// HandleMessage is a public method for testing tool calls
func (s *Server) HandleMessage(msg *Message) *Message {
	return s.handleMessage(msg)
}

func (s *Server) handleMessage(msg *Message) *Message {
	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg)
	case "tools/list":
		return s.handleToolsList(msg)
	case "tools/call":
		return s.handleToolsCall(msg)
	case "resources/list":
		return s.handleResourcesList(msg)
	case "resources/read":
		return s.handleResourcesRead(msg)
	default:
		return NewError(msg.ID, -32601, "Method not found", nil)
	}
}

func (s *Server) handleInitialize(msg *Message) *Message {
	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{
				ListChanged: true,
			},
			Resources: &ResourcesCapability{
				Subscribe:   false,
				ListChanged: true,
			},
		},
		ServerInfo: ServerInfo{
			Name:    "gabs",
			Version: version.Get(),
		},
	}
	return NewResponse(msg.ID, result)
}

func (s *Server) handleToolsList(msg *Message) *Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]Tool, 0, len(s.tools))
	for _, handler := range s.tools {
		tools = append(tools, handler.Tool)
	}

	result := ToolsListResult{Tools: tools}
	return NewResponse(msg.ID, result)
}

func (s *Server) handleToolsCall(msg *Message) *Message {
	var params ToolCallParams
	paramsBytes, err := json.Marshal(msg.Params)
	if err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	s.mu.RLock()
	handler, exists := s.tools[params.Name]
	s.mu.RUnlock()

	if !exists {
		return NewError(msg.ID, -32601, "Tool not found", params.Name)
	}

	result, err := handler.Handler(params.Arguments)
	if err != nil {
		return NewError(msg.ID, -32603, "Tool execution failed", err.Error())
	}

	return NewResponse(msg.ID, result)
}

func (s *Server) handleResourcesList(msg *Message) *Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resources := make([]Resource, 0, len(s.resources))
	for _, handler := range s.resources {
		resources = append(resources, handler.Resource)
	}

	result := ResourcesListResult{Resources: resources}
	return NewResponse(msg.ID, result)
}

func (s *Server) handleResourcesRead(msg *Message) *Message {
	var params ResourcesReadParams
	paramsBytes, err := json.Marshal(msg.Params)
	if err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	s.mu.RLock()
	handler, exists := s.resources[params.URI]
	s.mu.RUnlock()

	if !exists {
		return NewError(msg.ID, -32601, "Resource not found", params.URI)
	}

	contents, err := handler.Handler()
	if err != nil {
		return NewError(msg.ID, -32603, "Resource read failed", err.Error())
	}

	result := ResourcesReadResult{Contents: contents}
	return NewResponse(msg.ID, result)
}
