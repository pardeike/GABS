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
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// Server runs MCP over stdio.
type Server struct {
	log       util.Logger
	tools     map[string]*ToolHandler
	resources map[string]*ResourceHandler
	games     map[string]*process.Controller // Track running games
	mu        sync.RWMutex
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
		log:       log,
		tools:     make(map[string]*ToolHandler),
		resources: make(map[string]*ResourceHandler),
		games:     make(map[string]*process.Controller),
	}
}

// RegisterTool registers a tool with its handler
func (s *Server) RegisterTool(tool Tool, handler func(args map[string]interface{}) (*ToolResult, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[tool.Name] = &ToolHandler{
		Tool:    tool,
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

// RegisterGameManagementTools registers the game management tools for the new architecture
func (s *Server) RegisterGameManagementTools(gamesConfig *config.GamesConfig, backoffMin, backoffMax time.Duration) {
	// games_list tool
	s.RegisterTool(Tool{
		Name:        "games_list",
		Description: "List all configured games and their current status",
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
			content.WriteString(fmt.Sprintf("Configured Games (%d):\n\n", len(games)))
			for _, game := range games {
				status := s.checkGameStatus(game.ID)
				content.WriteString(fmt.Sprintf("• **%s** - %s (%s)\n", game.ID, game.Name, status))
				content.WriteString(fmt.Sprintf("  Use gameId: '%s' (or target: '%s')\n", game.ID, game.Target))
				content.WriteString(fmt.Sprintf("  Launch: %s\n", game.LaunchMode))
				if game.Description != "" {
					content.WriteString(fmt.Sprintf("  %s\n", game.Description))
				}
				content.WriteString("\n")
			}
		}
		
		return &ToolResult{
			Content: []Content{{Type: "text", Text: content.String()}},
		}, nil
	})

	// games_status tool
	s.RegisterTool(Tool{
		Name:        "games_status",
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
			
			status := s.checkGameStatus(game.ID)
			content.WriteString(fmt.Sprintf("**%s** (%s): %s\n", game.ID, game.Name, status))
		} else {
			// Check all games
			games := gamesConfig.ListGames()
			content.WriteString("Game Status Summary:\n\n")
			for _, game := range games {
				status := s.checkGameStatus(game.ID)
				content.WriteString(fmt.Sprintf("• **%s**: %s\n", game.ID, status))
			}
		}
		
		return &ToolResult{
			Content: []Content{{Type: "text", Text: content.String()}},
		}, nil
	})

	// games_start tool
	s.RegisterTool(Tool{
		Name:        "games_start",
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
	})

	// games_stop tool
	s.RegisterTool(Tool{
		Name:        "games_stop",
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
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to stop %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) stopped successfully", game.ID, game.Name)}},
		}, nil
	})

	// games_kill tool
	s.RegisterTool(Tool{
		Name:        "games_kill",
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
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to kill %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) terminated successfully", game.ID, game.Name)}},
		}, nil
	})

	// games_tools tool - List tools available for specific games
	s.RegisterTool(Tool{
		Name:        "games_tools", 
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
			
			content.WriteString("\nNote: Tools are prefixed with game ID (e.g., 'minecraft_inventory_get') to avoid conflicts between games.\n")
		}
		
		return &ToolResult{
			Content: []Content{{Type: "text", Text: content.String()}},
		}, nil
	})
}

// RegisterBridgeTools registers the legacy bridge management tools (for compatibility)
func (s *Server) RegisterBridgeTools(ctrl interface{}, client interface{}) {
	// Legacy bridge tools - kept for compatibility but not used in new architecture
	// In the new architecture, game management is done through games.* tools
}

// Game process management methods

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
	prefix := gameID + "_"
	
	for toolName, handler := range s.tools {
		if strings.HasPrefix(toolName, prefix) {
			gameTools = append(gameTools, handler.Tool)
		}
	}
	
	return gameTools
}

// checkGameStatus returns the current status of a game
func (s *Server) checkGameStatus(gameID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	controller, exists := s.games[gameID]
	if !exists {
		return "stopped"
	}

	launchMode := controller.GetLaunchMode()
	
	// For Steam/Epic launcher games, we can't easily track the actual game process
	// So we use a different status model
	if launchMode == "SteamAppId" || launchMode == "EpicAppId" {
		// For launcher-based games, assume they're "launched" (not "running")
		// because we don't track the actual game process
		return "launched" // Special status indicating we triggered the launcher
	}

	// For direct processes, check if the process is actually running
	if controller != nil && controller.IsRunning() {
		return "running"
	}

	// Process is dead, clean up
	delete(s.games, gameID)
	// TODO: Also cleanup any GABP connections and mirrored tools for this game
	s.log.Debugw("cleaned up dead game process", "gameId", gameID)
	
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

	// Create GABP bridge configuration
	var bridgeConfig config.BridgeConfig
	if game.GabpHost != "" {
		bridgeConfig.Host = game.GabpHost
	}
	if game.GabpMode != "" {
		bridgeConfig.Mode = game.GabpMode
	}

	port, token, bridgePath, err := config.WriteBridgeJSONWithConfig(game.ID, "", bridgeConfig)
	if err != nil {
		return fmt.Errorf("failed to create bridge config: %w", err)
	}

	s.log.Infow("created GABP bridge configuration", "gameId", game.ID, "port", port, "token", token[:8]+"...", "host", bridgeConfig.Host, "mode", bridgeConfig.Mode, "configPath", bridgePath)

	// Convert GameConfig to LaunchSpec
	launchSpec := process.LaunchSpec{
		GameId:     game.ID,
		Mode:       game.LaunchMode,
		PathOrId:   game.Target,
		Args:       game.Args,
		WorkingDir: game.WorkingDir,
	}

	// Create and configure controller
	controller := &process.Controller{}
	if err := controller.Configure(launchSpec); err != nil {
		return fmt.Errorf("failed to configure game launcher: %w", err)
	}

	// Start the game
	if err := controller.Start(); err != nil {
		return fmt.Errorf("failed to start game: %w", err)
	}

	// Track the running game
	s.games[game.ID] = controller
	
	s.log.Infow("game started with GABP bridge", "gameId", game.ID, "mode", game.LaunchMode, "pid", controller.GetPID(), "gabpPort", port)
	
	// TODO: In a future enhancement, we could start monitoring for GABP connections
	// and automatically set up mirroring when the game mod connects
	
	return nil
}

// stopGame stops a game process gracefully or by force
func (s *Server) stopGame(game config.GameConfig, force bool) error {
	s.mu.Lock()
	controller, exists := s.games[game.ID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("game %s is not running", game.ID)
	}

	// Remove from tracking immediately to prevent double-stops
	delete(s.games, game.ID)
	s.mu.Unlock()

	// Stop the process
	var err error
	if force {
		err = controller.Kill()
		s.log.Infow("game killed", "gameId", game.ID, "pid", controller.GetPID())
	} else {
		// Use default grace period of 3 seconds
		err = controller.Stop(3 * time.Second)
		s.log.Infow("game stopped", "gameId", game.ID, "pid", controller.GetPID())
	}

	// TODO: In future enhancement, also cleanup GABP connections and mirrored tools
	// This would involve:
	// 1. Disconnecting any active GABP client for this game
	// 2. Unregistering all game-specific tools (gameId.* tools)
	// 3. Cleaning up bridge configuration files

	return err
}

func (s *Server) ServeStdio(ctx context.Context) error {
	return s.Serve(os.Stdin, os.Stdout)
}

func (s *Server) Serve(r io.Reader, w io.Writer) error {
	// Implement newline-delimited JSON-RPC over stdio per MCP stdio transport
	reader := util.NewNewlineFrameReader(r)
	writer := util.NewNewlineFrameWriter(w)

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
			Version: "0.1.0",
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
