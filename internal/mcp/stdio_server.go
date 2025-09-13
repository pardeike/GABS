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
	// games.list tool
	s.RegisterTool(Tool{
		Name:        "games.list",
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
				content.WriteString(fmt.Sprintf("• **%s** (%s) - %s\n", game.ID, game.Name, status))
				content.WriteString(fmt.Sprintf("  Launch: %s via %s\n", game.LaunchMode, game.Target))
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

	// games.status tool
	s.RegisterTool(Tool{
		Name:        "games.status",
		Description: "Check the status of one or more games",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to check status for (optional, checks all if not provided)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameID, hasGameID := args["gameId"].(string)
		
		var content strings.Builder
		if hasGameID {
			// Check specific game
			game, exists := gamesConfig.GetGame(gameID)
			if !exists {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found", gameID)}},
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

	// games.start tool
	s.RegisterTool(Tool{
		Name:        "games.start",
		Description: "Start a configured game",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to start",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameID, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, exists := gamesConfig.GetGame(gameID)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found", gameID)}},
				IsError: true,
			}, nil
		}

		err := s.startGame(*game, backoffMin, backoffMax)
		if err != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to start %s: %v", gameID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' started successfully", gameID)}},
		}, nil
	})

	// games.stop tool
	s.RegisterTool(Tool{
		Name:        "games.stop",
		Description: "Gracefully stop a running game",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to stop",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameID, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, exists := gamesConfig.GetGame(gameID)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found", gameID)}},
				IsError: true,
			}, nil
		}

		err := s.stopGame(*game, false)
		if err != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to stop %s: %v", gameID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' stopped successfully", gameID)}},
		}, nil
	})

	// games.kill tool
	s.RegisterTool(Tool{
		Name:        "games.kill",
		Description: "Force terminate a running game",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to force terminate",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameID, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, exists := gamesConfig.GetGame(gameID)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found", gameID)}},
				IsError: true,
			}, nil
		}

		err := s.stopGame(*game, true)
		if err != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to kill %s: %v", gameID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' terminated successfully", gameID)}},
		}, nil
	})
}

// RegisterBridgeTools registers the legacy bridge management tools (for compatibility)
func (s *Server) RegisterBridgeTools(ctrl interface{}, client interface{}) {
	// Legacy bridge tools - kept for compatibility but not used in new architecture
	// In the new architecture, game management is done through games.* tools
}

// Game process management methods

// checkGameStatus returns the current status of a game
func (s *Server) checkGameStatus(gameID string) string {
	s.mu.RLock()
	controller, exists := s.games[gameID]
	s.mu.RUnlock()

	if !exists {
		return "stopped"
	}

	// Check if the process is still running
	// This is a simple check - in a more sophisticated implementation
	// we could also check GABP connection status
	if controller != nil {
		// The process controller doesn't expose process state directly,
		// but we can try a simple check by seeing if we can signal it
		return "running"
	}

	return "stopped"
}

// startGame starts a game process using the process controller
func (s *Server) startGame(game config.GameConfig, backoffMin, backoffMax time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already running
	if controller, exists := s.games[game.ID]; exists && controller != nil {
		return fmt.Errorf("game %s is already running", game.ID)
	}

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
	
	s.log.Infow("game started", "gameId", game.ID, "mode", game.LaunchMode)
	return nil
}

// stopGame stops a game process gracefully or by force
func (s *Server) stopGame(game config.GameConfig, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	controller, exists := s.games[game.ID]
	if !exists {
		return fmt.Errorf("game %s is not running", game.ID)
	}

	var err error
	if force {
		err = controller.Kill()
		s.log.Infow("game killed", "gameId", game.ID)
	} else {
		// Use default grace period of 3 seconds
		err = controller.Stop(3 * time.Second)
		s.log.Infow("game stopped", "gameId", game.ID)
	}

	// Remove from tracking regardless of whether stop/kill succeeded
	delete(s.games, game.ID)

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
