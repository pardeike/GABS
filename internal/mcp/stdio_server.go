package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
	"github.com/pardeike/gabs/internal/version"
)

// Server runs MCP over stdio.
type Server struct {
	log             util.Logger
	tools           map[string]*ToolHandler
	resources       map[string]*ResourceHandler
	games           map[string]process.ControllerInterface // Track running games
	configDir       string                                 // Config directory for bridge files
	apiKey          string                                 // API key for HTTP authentication
	mu              sync.RWMutex
	writers         []util.FrameWriter      // Track client connections for notifications
	writersMu       sync.RWMutex            // Protect writers slice
	gameTools       map[string][]string     // Track which tools belong to which games
	gameResources   map[string][]string     // Track which resources belong to which games
	gabpClients     map[string]*gabp.Client // Track GABP connections per game
	gabpDisconnects map[string]gabpDisconnectRecord
	starter         *process.SerializedStarter // Serialized process starter
	instanceID      string
}

type gabpDisconnectRecord struct {
	At      time.Time
	Message string
}

var serverInstanceCounter uint64

type gameAlreadyActiveError struct {
	status string
}

func (e *gameAlreadyActiveError) Error() string {
	switch e.status {
	case process.RuntimeStateStatusStarting:
		return "game launch is already in progress"
	default:
		return "game is already running"
	}
}

func (e *gameAlreadyActiveError) ToolMessage(game config.GameConfig) string {
	switch e.status {
	case process.RuntimeStateStatusStarting:
		return fmt.Sprintf("Game '%s' (%s) is already starting. Wait for launch to finish, then use games.connect if you need to attach to the existing instance.", game.ID, game.Name)
	default:
		return fmt.Sprintf("Game '%s' (%s) is already running. Use games.status or games.connect instead of starting it again.", game.ID, game.Name)
	}
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
		log:             log,
		tools:           make(map[string]*ToolHandler),
		resources:       make(map[string]*ResourceHandler),
		games:           make(map[string]process.ControllerInterface),
		configDir:       "", // Will be set by SetConfigDir
		writers:         make([]util.FrameWriter, 0),
		gameTools:       make(map[string][]string),
		gameResources:   make(map[string][]string),
		gabpClients:     make(map[string]*gabp.Client),
		gabpDisconnects: make(map[string]gabpDisconnectRecord),
		starter:         process.NewSerializedStarter(), // Initialize serialized starter
		instanceID:      newServerInstanceID(),
	}
}

// NewServerForTesting creates a server with shorter timeouts for testing
func NewServerForTesting(log util.Logger) *Server {
	return &Server{
		log:             log,
		tools:           make(map[string]*ToolHandler),
		resources:       make(map[string]*ResourceHandler),
		games:           make(map[string]process.ControllerInterface),
		configDir:       "", // Will be set by SetConfigDir
		writers:         make([]util.FrameWriter, 0),
		gameTools:       make(map[string][]string),
		gameResources:   make(map[string][]string),
		gabpClients:     make(map[string]*gabp.Client),
		gabpDisconnects: make(map[string]gabpDisconnectRecord),
		starter:         process.NewSerializedStarterForTesting(), // Use testing timeouts
		instanceID:      newServerInstanceID(),
	}
}

func newServerInstanceID() string {
	seq := atomic.AddUint64(&serverInstanceCounter, 1)
	return fmt.Sprintf("%d-%d-%d", os.Getpid(), time.Now().UnixNano(), seq)
}

func parseOptionalPositiveIntValue(raw interface{}, key string) (int, bool, *ToolResult) {
	if raw == nil {
		return 0, false, nil
	}

	var value int
	switch typed := raw.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
				IsError: true,
			}
		}
		value = int(typed)
	case int:
		value = typed
	case int32:
		value = int(typed)
	case int64:
		value = int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
				IsError: true,
			}
		}
		value = parsed
	default:
		return 0, false, &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
			IsError: true,
		}
	}

	if value <= 0 {
		return 0, false, nil
	}

	return value, true, nil
}

func parseOptionalTimeoutSecondsArg(args map[string]interface{}, key string, defaultValue time.Duration) (time.Duration, *ToolResult) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return defaultValue, nil
	}

	seconds, hasValue, invalidArg := parseOptionalPositiveIntValue(raw, key)
	if invalidArg != nil {
		return defaultValue, invalidArg
	}
	if !hasValue {
		return defaultValue, nil
	}

	return time.Duration(seconds) * time.Second, nil
}

func deriveMirroredToolCallTimeout(args map[string]interface{}, defaultValue time.Duration) (time.Duration, *ToolResult) {
	timeout := defaultValue

	if timeoutMs, hasValue, invalidArg := parseOptionalPositiveIntValue(args["timeoutMs"], "timeoutMs"); invalidArg != nil {
		return defaultValue, invalidArg
	} else if hasValue {
		candidate := time.Duration(timeoutMs)*time.Millisecond + (5 * time.Second)
		if candidate > timeout {
			timeout = candidate
		}
	}

	if timeoutSeconds, hasValue, invalidArg := parseOptionalPositiveIntValue(args["timeout"], "timeout"); invalidArg != nil {
		return defaultValue, invalidArg
	} else if hasValue {
		candidate := time.Duration(timeoutSeconds) * time.Second
		if candidate > timeout {
			timeout = candidate
		}
	}

	return timeout, nil
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

			// Get status once to avoid double mutex lock
			status := s.checkGameStatus(game.ID)
			statusDesc := s.getStatusDescriptionFromStatus(status, game)
			content.WriteString(fmt.Sprintf("**%s** (%s): %s\n", game.ID, game.Name, statusDesc))
			if disconnectNote := s.describeLastGABPDisconnect(game.ID); disconnectNote != "" {
				content.WriteString(fmt.Sprintf("\n%s\n", disconnectNote))
			}

			// Add helpful info for launcher games ONLY when we cannot track them
			if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
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

		startResult, err := s.startGame(*game, gamesConfig, backoffMin, backoffMax)
		if err != nil {
			var activeErr *gameAlreadyActiveError
			if errors.As(err, &activeErr) {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: activeErr.ToolMessage(*game)}},
				}, nil
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to start %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		if startResult != nil && !startResult.GABPConnected {
			message := fmt.Sprintf("Game '%s' (%s) started, but GABP was not ready after %s", game.ID, game.Name, startResult.GABPConnectWait.Round(time.Millisecond))
			if startResult.GABPConnectError != nil {
				message = fmt.Sprintf("%s: %v", message, startResult.GABPConnectError)
			}
			message = fmt.Sprintf("%s. The game may still be loading or the mod may be missing. Use games.status, then games.connect once the mod is ready.", message)
			return &ToolResult{
				Content: []Content{{Type: "text", Text: message}},
				StructuredContent: map[string]interface{}{
					"gameId":           game.ID,
					"processStarted":   startResult.ProcessStarted,
					"gabpConnected":    startResult.GABPConnected,
					"gameStillRunning": startResult.GameStillRunning,
					"gabpWaitMs":       startResult.GABPConnectWait.Milliseconds(),
					"gabpError": func() interface{} {
						if startResult.GABPConnectError == nil {
							return nil
						}
						return startResult.GABPConnectError.Error()
					}(),
				},
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) started successfully and connected via GABP.", game.ID, game.Name)}},
			StructuredContent: map[string]interface{}{
				"gameId":           game.ID,
				"processStarted":   true,
				"gabpConnected":    true,
				"gameStillRunning": true,
			},
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

	type listedGameTool struct {
		GameID        string
		Tool          Tool
		CanonicalName string
		LocalName     string
	}

	getOptionalStringArg := func(args map[string]interface{}, key string) (string, bool, *ToolResult) {
		raw, exists := args[key]
		if !exists || raw == nil {
			return "", false, nil
		}

		value, ok := raw.(string)
		if !ok {
			return "", false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be a string", key)}},
				IsError: true,
			}
		}

		value = strings.TrimSpace(value)
		if value == "" {
			return "", false, nil
		}

		return value, true, nil
	}

	getOptionalBoolArg := func(args map[string]interface{}, key string) (bool, bool, *ToolResult) {
		raw, exists := args[key]
		if !exists || raw == nil {
			return false, false, nil
		}

		switch typed := raw.(type) {
		case bool:
			return typed, true, nil
		case string:
			value := strings.TrimSpace(strings.ToLower(typed))
			switch value {
			case "true":
				return true, true, nil
			case "false":
				return false, true, nil
			}
		}

		return false, false, &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be a boolean", key)}},
			IsError: true,
		}
	}

	getOptionalPositiveIntArg := func(args map[string]interface{}, key string) (int, bool, *ToolResult) {
		raw, exists := args[key]
		if !exists || raw == nil {
			return 0, false, nil
		}

		var value int
		switch typed := raw.(type) {
		case float64:
			if typed != float64(int(typed)) {
				return 0, false, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
					IsError: true,
				}
			}
			value = int(typed)
		case int:
			value = typed
		case int32:
			value = int(typed)
		case int64:
			value = int(typed)
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err != nil {
				return 0, false, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
					IsError: true,
				}
			}
			value = parsed
		default:
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
				IsError: true,
			}
		}

		if value <= 0 {
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be greater than zero", key)}},
				IsError: true,
			}
		}

		return value, true, nil
	}

	getCursorOffset := func(args map[string]interface{}, total int) (int, *ToolResult) {
		rawCursor, exists := args["cursor"]
		if !exists || rawCursor == nil {
			return 0, nil
		}

		var cursor int
		switch typed := rawCursor.(type) {
		case float64:
			if typed != float64(int(typed)) {
				return 0, &ToolResult{
					Content: []Content{{Type: "text", Text: "Argument 'cursor' must be an integer offset or string cursor"}},
					IsError: true,
				}
			}
			cursor = int(typed)
		case int:
			cursor = typed
		case int32:
			cursor = int(typed)
		case int64:
			cursor = int(typed)
		case string:
			if strings.TrimSpace(typed) == "" {
				return 0, nil
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err != nil {
				return 0, &ToolResult{
					Content: []Content{{Type: "text", Text: "Argument 'cursor' must be an integer offset or string cursor"}},
					IsError: true,
				}
			}
			cursor = parsed
		default:
			return 0, &ToolResult{
				Content: []Content{{Type: "text", Text: "Argument 'cursor' must be an integer offset or string cursor"}},
				IsError: true,
			}
		}

		if cursor < 0 {
			return 0, &ToolResult{
				Content: []Content{{Type: "text", Text: "Argument 'cursor' must be zero or greater"}},
				IsError: true,
			}
		}
		if cursor > total {
			return 0, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Cursor %d is out of range for %d matching tools", cursor, total)}},
				IsError: true,
			}
		}

		return cursor, nil
	}

	getSortedGames := func() []config.GameConfig {
		games := gamesConfig.ListGames()
		sort.Slice(games, func(i, j int) bool {
			return games[i].ID < games[j].ID
		})
		return games
	}

	listToolsForDiscovery := func(gameID string, hasGameID bool) ([]listedGameTool, *config.GameConfig, *ToolResult) {
		entries := make([]listedGameTool, 0)

		if hasGameID {
			game, exists := s.resolveGameId(gamesConfig, gameID)
			if !exists {
				return nil, nil, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameID)}},
					IsError: true,
				}
			}

			for _, tool := range s.getGameSpecificTools(game.ID) {
				entries = append(entries, listedGameTool{
					GameID:        game.ID,
					Tool:          tool,
					CanonicalName: toolCanonicalName(tool),
					LocalName:     toolLocalName(game.ID, tool),
				})
			}

			return entries, game, nil
		}

		for _, game := range getSortedGames() {
			for _, tool := range s.getGameSpecificTools(game.ID) {
				entries = append(entries, listedGameTool{
					GameID:        game.ID,
					Tool:          tool,
					CanonicalName: toolCanonicalName(tool),
					LocalName:     toolLocalName(game.ID, tool),
				})
			}
		}

		return entries, nil, nil
	}

	filterListedTools := func(entries []listedGameTool, query, prefix string) []listedGameTool {
		if query == "" && prefix == "" {
			return entries
		}

		query = strings.ToLower(query)
		prefix = strings.ToLower(prefix)
		matchesQuery := func(value string) bool {
			value = strings.ToLower(value)
			if strings.ContainsAny(query, "./_- ") {
				return strings.Contains(value, query)
			}

			tokens := strings.FieldsFunc(value, func(r rune) bool {
				return (r < 'a' || r > 'z') && (r < '0' || r > '9')
			})
			for _, token := range tokens {
				if strings.HasPrefix(token, query) {
					return true
				}
			}
			return false
		}

		filtered := make([]listedGameTool, 0, len(entries))
		for _, entry := range entries {
			if query != "" {
				if !matchesQuery(entry.Tool.Name) &&
					!matchesQuery(entry.CanonicalName) &&
					!matchesQuery(entry.LocalName) {
					continue
				}
			}

			if prefix != "" {
				registered := strings.ToLower(entry.Tool.Name)
				canonical := strings.ToLower(entry.CanonicalName)
				local := strings.ToLower(entry.LocalName)
				if !strings.HasPrefix(registered, prefix) &&
					!strings.HasPrefix(canonical, prefix) &&
					!strings.HasPrefix(local, prefix) {
					continue
				}
			}

			filtered = append(filtered, entry)
		}

		return filtered
	}

	paginateListedTools := func(entries []listedGameTool, cursor, limit int) ([]listedGameTool, string) {
		if cursor >= len(entries) {
			return []listedGameTool{}, ""
		}
		if limit <= 0 {
			return entries[cursor:], ""
		}

		end := cursor + limit
		if end > len(entries) {
			end = len(entries)
		}

		nextCursor := ""
		if end < len(entries) {
			nextCursor = strconv.Itoa(end)
		}

		return entries[cursor:end], nextCursor
	}

	buildToolNameItemsWithOptions := func(entries []listedGameTool, brief bool) []map[string]interface{} {
		items := make([]map[string]interface{}, 0, len(entries))
		for _, entry := range entries {
			item := map[string]interface{}{
				"name":      entry.Tool.Name,
				"gameId":    entry.GameID,
				"localName": entry.LocalName,
			}
			if entry.CanonicalName != entry.Tool.Name {
				item["originalName"] = entry.CanonicalName
			}
			if brief {
				if summary := toolBriefDescription(entry.Tool.Description); summary != "" {
					item["summary"] = summary
				}
			}
			items = append(items, item)
		}
		return items
	}

	buildDetailedToolItems := func(entries []listedGameTool) []map[string]interface{} {
		items := make([]map[string]interface{}, 0, len(entries))
		for _, entry := range entries {
			item := map[string]interface{}{
				"name":         entry.Tool.Name,
				"gameId":       entry.GameID,
				"localName":    entry.LocalName,
				"description":  entry.Tool.Description,
				"inputSchema":  entry.Tool.InputSchema,
				"outputSchema": entry.Tool.OutputSchema,
			}
			if entry.CanonicalName != entry.Tool.Name {
				item["originalName"] = entry.CanonicalName
			}
			items = append(items, item)
		}
		return items
	}

	describeDiscoveryFilters := func(query, prefix string) string {
		parts := make([]string, 0, 2)
		if strings.TrimSpace(query) != "" {
			parts = append(parts, fmt.Sprintf("query %q", query))
		}
		if strings.TrimSpace(prefix) != "" {
			parts = append(parts, fmt.Sprintf("prefix %q", prefix))
		}

		return strings.Join(parts, " and ")
	}

	buildNoToolsMessage := func(game *config.GameConfig, noun string) string {
		var content strings.Builder
		if game != nil {
			content.WriteString(fmt.Sprintf("No game-specific %s available for '%s'.\n", noun, game.ID))
			status := s.checkGameStatus(game.ID)
			if status != "running" && status != "connected" {
				content.WriteString(fmt.Sprintf("Game is currently '%s'. Start it with games.start and connect with games.connect to enable GABP tools.\n", status))
			} else {
				content.WriteString("The game is running, but no GABP tools are currently connected.\n")
			}
			return content.String()
		}

		content.WriteString(fmt.Sprintf("No game-specific %s available.\n", noun))
		content.WriteString("Start games with GABP-compliant mods to see their tools.\n")
		return content.String()
	}

	buildNoMatchingToolsMessage := func(game *config.GameConfig, noun string, availableTotal int, query, prefix string) string {
		var content strings.Builder
		content.WriteString(fmt.Sprintf("No matching game-specific %s", noun))
		if game != nil {
			content.WriteString(fmt.Sprintf(" for '%s'", game.ID))
		}
		content.WriteString(".\n")

		filterSummary := describeDiscoveryFilters(query, prefix)
		if game != nil {
			content.WriteString(fmt.Sprintf("%d game-specific tools are currently connected for this game", availableTotal))
		} else {
			content.WriteString(fmt.Sprintf("%d game-specific tools are currently connected across configured games", availableTotal))
		}
		if filterSummary != "" {
			content.WriteString(fmt.Sprintf(", but none matched %s.\n", filterSummary))
		} else {
			content.WriteString(", but none matched the requested filters.\n")
		}
		content.WriteString("Use games.tool_names without filters to browse compact names, then inspect one tool with games.tool_detail.\n")
		return content.String()
	}

	findListedTool := func(entries []listedGameTool, gameID, requested string) (listedGameTool, bool) {
		for _, entry := range entries {
			if toolMatchesRequestedName(gameID, entry.Tool, requested) {
				return entry, true
			}
		}
		return listedGameTool{}, false
	}

	resolveListedTool := func(gameID string, hasGameID bool, requested string) (listedGameTool, *ToolResult) {
		requested = strings.TrimSpace(requested)
		if requested == "" {
			return listedGameTool{}, &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: tool"}},
				IsError: true,
			}
		}

		if hasGameID {
			entries, game, listErr := listToolsForDiscovery(gameID, true)
			if listErr != nil {
				return listedGameTool{}, listErr
			}

			entry, found := findListedTool(entries, game.ID, requested)
			if !found {
				return listedGameTool{}, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Tool '%s' not found for game '%s'. Use games.tool_names to discover available names first.", requested, game.ID)}},
					IsError: true,
				}
			}

			return entry, nil
		}

		entries, _, listErr := listToolsForDiscovery("", false)
		if listErr != nil {
			return listedGameTool{}, listErr
		}

		matches := make([]listedGameTool, 0, 1)
		for _, entry := range entries {
			if toolMatchesRequestedName(entry.GameID, entry.Tool, requested) {
				matches = append(matches, entry)
			}
		}

		switch len(matches) {
		case 0:
			return listedGameTool{}, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Tool '%s' was not found. Use games.tool_names to discover available names first, or include gameId if you are using a local tool name.", requested)}},
				IsError: true,
			}
		case 1:
			return matches[0], nil
		default:
			gameIDs := make([]string, 0, len(matches))
			seen := make(map[string]struct{}, len(matches))
			for _, entry := range matches {
				if _, exists := seen[entry.GameID]; exists {
					continue
				}
				seen[entry.GameID] = struct{}{}
				gameIDs = append(gameIDs, entry.GameID)
			}
			sort.Strings(gameIDs)
			return listedGameTool{}, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Tool '%s' matched multiple games (%s). Include gameId or use the fully qualified mirrored tool name.", requested, strings.Join(gameIDs, ", "))}},
				IsError: true,
			}
		}
	}

	// games.tool_names tool - Compact game tool discovery for AI clients
	s.RegisterToolWithConfig(Tool{
		Name:        "games.tool_names",
		Description: "List compact game-specific tool names. Use this first for low-token discovery, then call games.tool_detail for one tool.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to list tools for (optional, lists all configured games if not provided)",
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Case-insensitive text filter applied to tool names (optional)",
				},
				"prefix": map[string]interface{}{
					"type":        "string",
					"description": "Prefix filter applied to the full tool name and local name (optional)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of names to return (optional, defaults to 50)",
				},
				"cursor": map[string]interface{}{
					"type":        "string",
					"description": "Offset cursor returned by a previous page (optional)",
				},
				"brief": map[string]interface{}{
					"type":        "boolean",
					"description": "Include a one-line summary per tool in structured output only (optional, default false)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		query, _, invalidArg := getOptionalStringArg(args, "query")
		if invalidArg != nil {
			return invalidArg, nil
		}
		prefix, _, invalidArg := getOptionalStringArg(args, "prefix")
		if invalidArg != nil {
			return invalidArg, nil
		}
		brief, _, invalidArg := getOptionalBoolArg(args, "brief")
		if invalidArg != nil {
			return invalidArg, nil
		}

		entries, game, listErr := listToolsForDiscovery(gameID, hasGameID)
		if listErr != nil {
			return listErr, nil
		}

		availableTotal := len(entries)
		entries = filterListedTools(entries, query, prefix)
		total := len(entries)

		limit, hasLimit, invalidArg := getOptionalPositiveIntArg(args, "limit")
		if invalidArg != nil {
			return invalidArg, nil
		}
		if !hasLimit {
			limit = 50
		}

		cursor, invalidCursor := getCursorOffset(args, total)
		if invalidCursor != nil {
			return invalidCursor, nil
		}

		page, nextCursor := paginateListedTools(entries, cursor, limit)
		if len(page) == 0 {
			message := buildNoToolsMessage(game, "tool names")
			if total > 0 && cursor >= total {
				message = fmt.Sprintf("No more matching tool names for cursor %d.\nStart again without a cursor or use a smaller cursor.\n", cursor)
			} else if availableTotal > 0 && (query != "" || prefix != "") {
				message = buildNoMatchingToolsMessage(game, "tool names", availableTotal, query, prefix)
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: message}},
				StructuredContent: map[string]interface{}{
					"availableTotal": availableTotal,
					"gameId":         gameID,
					"total":          total,
					"returned":       0,
					"nextCursor":     nextCursor,
					"tools":          buildToolNameItemsWithOptions(nil, brief),
				},
			}, nil
		}

		var content strings.Builder
		scope := "all games"
		if game != nil {
			scope = fmt.Sprintf("game '%s'", game.ID)
		}
		content.WriteString(fmt.Sprintf("Tool names for %s (%d shown of %d matching):\n", scope, len(page), total))
		for _, entry := range page {
			content.WriteString(entry.Tool.Name)
			content.WriteString("\n")
		}
		content.WriteString("\nUse games.tool_detail with one of these names to inspect parameters and output.")
		if nextCursor != "" {
			content.WriteString(fmt.Sprintf("\nNext cursor: %s", nextCursor))
		}

		structured := map[string]interface{}{
			"availableTotal": availableTotal,
			"total":          total,
			"returned":       len(page),
			"tools":          buildToolNameItemsWithOptions(page, brief),
			"nextCursor":     nextCursor,
		}
		if game != nil {
			structured["gameId"] = game.ID
		}
		if query != "" {
			structured["query"] = query
		}
		if prefix != "" {
			structured["prefix"] = prefix
		}
		if brief {
			structured["brief"] = true
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: strings.TrimRight(content.String(), "\n")}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games.tool_detail tool - Detailed schema for one discovered tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.tool_detail",
		Description: "Show detailed metadata for one game-specific tool, including parameters and output schema.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to inspect the tool in (optional if the tool name is fully qualified or uniquely discoverable)",
				},
				"tool": map[string]interface{}{
					"type":        "string",
					"description": "Tool name as returned by games.tool_names or games.tools (required). Prefer the fully qualified mirrored name, e.g. 'bannerlord.core.ping'.",
				},
			},
			"required": []string{"tool"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		requestedTool, ok := args["tool"].(string)
		if !ok || strings.TrimSpace(requestedTool) == "" {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: tool"}},
				IsError: true,
			}, nil
		}

		entry, resolveErr := resolveListedTool(gameID, hasGameID, requestedTool)
		if resolveErr != nil {
			return resolveErr, nil
		}

		var content strings.Builder
		content.WriteString(fmt.Sprintf("Tool detail for '%s' in game '%s'\n", entry.Tool.Name, entry.GameID))
		if entry.Tool.Description != "" {
			content.WriteString("\n")
			content.WriteString(entry.Tool.Description)
		}
		if entry.CanonicalName != entry.Tool.Name {
			content.WriteString(fmt.Sprintf("\n\nOriginal name: %s", entry.CanonicalName))
		}
		writeToolParams(&content, entry.Tool)

		structured := map[string]interface{}{
			"gameId":       entry.GameID,
			"name":         entry.Tool.Name,
			"localName":    entry.LocalName,
			"description":  entry.Tool.Description,
			"inputSchema":  entry.Tool.InputSchema,
			"outputSchema": entry.Tool.OutputSchema,
		}
		if entry.CanonicalName != entry.Tool.Name {
			structured["originalName"] = entry.CanonicalName
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: strings.TrimSpace(content.String())}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games.tools tool - Detailed tool listing, kept for compatibility
	s.RegisterToolWithConfig(Tool{
		Name:        "games.tools",
		Description: "List game-specific tools in detailed form for compatibility and human-readable inspection. Prefer games.tool_names for compact discovery and games.tool_detail for one tool.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to list tools for (optional, lists all if not provided)",
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Case-insensitive text filter applied to tool names (optional)",
				},
				"prefix": map[string]interface{}{
					"type":        "string",
					"description": "Prefix filter applied to the full tool name and local name (optional)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of tools to return (optional)",
				},
				"cursor": map[string]interface{}{
					"type":        "string",
					"description": "Offset cursor returned by a previous page (optional)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		query, _, invalidArg := getOptionalStringArg(args, "query")
		if invalidArg != nil {
			return invalidArg, nil
		}
		prefix, _, invalidArg := getOptionalStringArg(args, "prefix")
		if invalidArg != nil {
			return invalidArg, nil
		}

		entries, game, listErr := listToolsForDiscovery(gameID, hasGameID)
		if listErr != nil {
			return listErr, nil
		}

		availableTotal := len(entries)
		entries = filterListedTools(entries, query, prefix)
		total := len(entries)

		limit, _, invalidArg := getOptionalPositiveIntArg(args, "limit")
		if invalidArg != nil {
			return invalidArg, nil
		}
		cursor, invalidCursor := getCursorOffset(args, total)
		if invalidCursor != nil {
			return invalidCursor, nil
		}

		page, nextCursor := paginateListedTools(entries, cursor, limit)
		if len(page) == 0 {
			message := buildNoToolsMessage(game, "tools")
			if total > 0 && cursor >= total {
				message = fmt.Sprintf("No more matching tools for cursor %d.\nStart again without a cursor or use a smaller cursor.\n", cursor)
			} else if availableTotal > 0 && (query != "" || prefix != "") {
				message = buildNoMatchingToolsMessage(game, "tools", availableTotal, query, prefix)
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: message}},
				StructuredContent: map[string]interface{}{
					"availableTotal": availableTotal,
					"gameId":         gameID,
					"total":          total,
					"returned":       0,
					"nextCursor":     nextCursor,
					"tools":          buildDetailedToolItems(nil),
				},
			}, nil
		}

		var content strings.Builder
		if game != nil {
			content.WriteString(fmt.Sprintf("Tools for game '%s' (%d shown of %d matching):\n\n", game.ID, len(page), total))
			for _, entry := range page {
				content.WriteString(fmt.Sprintf("• **%s** - %s", entry.Tool.Name, entry.Tool.Description))
				writeToolParams(&content, entry.Tool)
				content.WriteString("\n")
			}
		} else {
			content.WriteString(fmt.Sprintf("Game-Specific Tools Available (%d shown of %d matching):\n\n", len(page), total))
			currentGameID := ""
			for _, entry := range page {
				if entry.GameID != currentGameID {
					if currentGameID != "" {
						content.WriteString("\n")
					}
					currentGameID = entry.GameID
					status := s.checkGameStatus(entry.GameID)
					content.WriteString(fmt.Sprintf("**%s** (%s):\n", entry.GameID, status))
				}
				content.WriteString(fmt.Sprintf("  • %s - %s", entry.Tool.Name, entry.Tool.Description))
				writeToolParams(&content, entry.Tool)
				content.WriteString("\n")
			}
			content.WriteString("\nNote: Tools are prefixed with game ID (e.g., 'minecraft.inventory.get') to avoid conflicts between games.\n")
		}

		content.WriteString("\nUse games.tool_names for a smaller list and games.tool_detail for one tool.")
		if nextCursor != "" {
			content.WriteString(fmt.Sprintf("\nNext cursor: %s", nextCursor))
		}

		structured := map[string]interface{}{
			"availableTotal": availableTotal,
			"total":          total,
			"returned":       len(page),
			"tools":          buildDetailedToolItems(page),
			"nextCursor":     nextCursor,
		}
		if game != nil {
			structured["gameId"] = game.ID
		}
		if query != "" {
			structured["query"] = query
		}
		if prefix != "" {
			structured["prefix"] = prefix
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: strings.TrimRight(content.String(), "\n")}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games.connect tool - Manually connect to a game's GABP server
	s.RegisterToolWithConfig(Tool{
		Name:        "games.connect",
		Description: "Connect to a running game's GABP server to discover and sync tools. Use this after the game has fully loaded.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to connect to (required)",
				},
				"forceTakeover": map[string]interface{}{
					"type":        "boolean",
					"description": "When true, override another live GABS session's ownership record and attempt to connect anyway. Defaults to false.",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Request timeout in seconds (optional, default 15). Increase for slow game loads or slow tool discovery.",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameIdArg, ok := args["gameId"].(string)
		if !ok || gameIdArg == "" {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: gameId"}},
				IsError: true,
			}, nil
		}

		game, exists := s.resolveGameId(gamesConfig, gameIdArg)
		if !exists {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", gameIdArg)}},
				IsError: true,
			}, nil
		}

		forceTakeover, _, forceTakeoverErr := getOptionalBoolArg(args, "forceTakeover")
		if forceTakeoverErr != nil {
			return forceTakeoverErr, nil
		}
		connectTimeout, invalidTimeout := parseOptionalTimeoutSecondsArg(args, "timeout", 15*time.Second)
		if invalidTimeout != nil {
			return invalidTimeout, nil
		}

		// Check if already connected - re-sync tools.
		s.mu.RLock()
		existingClient, alreadyConnected := s.gabpClients[game.ID]
		s.mu.RUnlock()

		if alreadyConnected && existingClient.IsConnected() {
			if err := s.syncGABPToolsWithTimeout(existingClient, game.ID, connectTimeout); err != nil {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Already connected to '%s' but failed to sync tools: %v", game.ID, err)}},
					IsError: true,
				}, nil
			}
			toolCount := len(s.getGameSpecificTools(game.ID))
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Already connected to '%s'. Re-synced %d tools.", game.ID, toolCount)}},
			}, nil
		}

		runtimeState, runtimeErr := process.LoadRuntimeState(game.ID, s.configDir)
		if runtimeErr != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to inspect shared runtime state for '%s': %v", game.ID, runtimeErr)}},
				IsError: true,
			}, nil
		}

		hadForeignOwner := process.RuntimeStateOwnedByAnotherLiveOwner(runtimeState, os.Getpid(), s.instanceID)
		if hadForeignOwner && !forceTakeover {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is already owned by another live GABS session (pid %d). Skipping games.connect here to avoid competing bridge clients.", game.ID, runtimeState.OwnerPID)}},
			}, nil
		}

		status := s.checkGameStatus(game.ID)

		_, port, token, err := config.ReadBridgeJSON(game.ID, s.configDir)
		if err != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to read bridge config for '%s': %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		// Allow reattaching after a GABS restart. If bridge.json is present,
		// attempt a direct GABP reconnect even when this GABS instance does not
		// currently track the process as running.
		connector := NewServerGABPConnector(s, backoffMin, backoffMax)
		connectCtx, connectCancel := context.WithTimeout(context.Background(), connectTimeout)
		defer connectCancel()

		err = connector.AttemptConnection(connectCtx, game.ID, port, token)
		if err != nil {
			disconnectNote := s.describeLastGABPDisconnect(game.ID)
			if status != "running" && status != "connected" {
				if status == "running-disconnected" {
					status = "running"
				}
				if disconnectNote != "" {
					disconnectNote = "\n" + disconnectNote
				}
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to connect to GABP server for '%s' on port %d after %s: %v. GABS currently sees status '%s'. Make sure the game is still running and the mod is fully loaded.%s", game.ID, port, connectTimeout.Round(time.Second), err, status, disconnectNote)}},
					IsError: true,
				}, nil
			}

			if disconnectNote != "" {
				disconnectNote = "\n" + disconnectNote
			}
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to connect to GABP server for '%s' on port %d after %s: %v. Make sure the game mod is loaded.%s", game.ID, port, connectTimeout.Round(time.Second), err, disconnectNote)}},
				IsError: true,
			}, nil
		}

		toolCount := len(s.getGameSpecificTools(game.ID))

		updatedState := process.RuntimeState{
			GameID:          game.ID,
			Status:          process.RuntimeStateStatusRunning,
			OwnerPID:        os.Getpid(),
			OwnerInstanceID: s.instanceID,
			StopProcessName: game.StopProcessName,
		}
		if runtimeState != nil {
			updatedState = *runtimeState
			updatedState.GameID = game.ID
			updatedState.Status = process.RuntimeStateStatusRunning
			updatedState.OwnerPID = os.Getpid()
			updatedState.OwnerInstanceID = s.instanceID
			if updatedState.StopProcessName == "" {
				updatedState.StopProcessName = game.StopProcessName
			}
		}
		if err := process.SaveRuntimeState(game.ID, s.configDir, updatedState); err != nil {
			s.log.Warnw("failed to persist runtime ownership after connect", "gameId", game.ID, "error", err)
		}

		if hadForeignOwner && forceTakeover {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Force-took ownership of '%s' from GABS pid %d and connected to the GABP server on port %d. Discovered %d tools.", game.ID, runtimeState.OwnerPID, port, toolCount)}},
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Successfully connected to '%s' GABP server on port %d. Discovered %d tools.", game.ID, port, toolCount)}},
		}, nil
	}, normalizationConfig)

	// games.call_tool - Proxy tool calls to a game's GABP server
	s.RegisterToolWithConfig(Tool{
		Name:        "games.call_tool",
		Description: "Call a game-specific tool on a running game via its GABP connection. Prefer games.tool_names for discovery and games.tool_detail for schema inspection before calling.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to call the tool on (optional if the tool name is fully qualified or uniquely discoverable)",
				},
				"tool": map[string]interface{}{
					"type":        "string",
					"description": "Tool name as returned by games.tool_names or games.tools (required). Prefer the full mirrored name, e.g. 'bannerlord.core.ping'.",
				},
				"arguments": map[string]interface{}{
					"type":        "object",
					"description": "Arguments to pass to the tool (optional, depends on tool)",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Request timeout in seconds (optional, default 30). Increase for long-running tools like wait_for_screen or wait_for_game_loaded.",
				},
			},
			"required": []string{"tool"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		gameIdArg, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		toolName, ok := args["tool"].(string)
		if !ok || toolName == "" {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: tool"}},
				IsError: true,
			}, nil
		}

		toolArgs, _ := args["arguments"].(map[string]interface{})
		if toolArgs == nil {
			toolArgs = map[string]interface{}{}
		}

		timeout, invalidTimeout := parseOptionalTimeoutSecondsArg(args, "timeout", 30*time.Second)
		if invalidTimeout != nil {
			return invalidTimeout, nil
		}

		entry, resolveErr := resolveListedTool(gameIdArg, hasGameID, toolName)
		if resolveErr != nil {
			return resolveErr, nil
		}

		// Get the GABP client for this game
		s.mu.RLock()
		client, connected := s.gabpClients[entry.GameID]
		s.mu.RUnlock()

		if !connected || !client.IsConnected() {
			disconnectNote := s.describeLastGABPDisconnect(entry.GameID)
			if disconnectNote != "" {
				disconnectNote = " " + disconnectNote
			}
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is not connected via GABP. Use games.status to verify whether it is still running, then use games.connect or games.start as appropriate.%s", entry.GameID, disconnectNote)}},
				IsError: true,
			}, nil
		}

		// Resolve the requested name against the mirrored tools for this game.
		// This accepts the registered MCP name, the original dotted name, or the
		// local tool name, so games.call_tool keeps working under OpenAI tool name
		// normalization as well.
		gabpToolName := gabpToolNameFromTool(entry.GameID, entry.Tool)

		result, isError, err := client.CallToolWithTimeout(gabpToolName, toolArgs, timeout)
		if err != nil {
			disconnectNote := s.describeLastGABPDisconnect(entry.GameID)
			if disconnectNote != "" {
				disconnectNote = " " + disconnectNote
			}
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("GABP tool call failed: %v.%s", err, disconnectNote)}},
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

		// Convert result to text content
		content := []Content{}
		if resultText, ok := result["text"].(string); ok {
			content = append(content, Content{Type: "text", Text: resultText})
		} else {
			if jsonData, err := json.Marshal(result); err != nil {
				content = append(content, Content{Type: "text", Text: fmt.Sprintf("%v", result)})
			} else {
				content = append(content, Content{Type: "text", Text: string(jsonData)})
			}
		}

		return &ToolResult{
			Content:           content,
			StructuredContent: result,
			IsError:           false,
		}, nil
	}, normalizationConfig)
}

// RegisterBridgeTools registers the legacy bridge management tools (for compatibility)
func (s *Server) RegisterBridgeTools(ctrl interface{}, client interface{}) {
	// Legacy bridge tools - kept for compatibility but not used in new architecture
	// In the new architecture, game management is done through games.* tools
}

// getGameFromController extracts game config from controller - helper for status checking
func (s *Server) getGameFromController(controller process.ControllerInterface) *config.GameConfig {
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

type toolSchemaProperty struct {
	Name         string
	Type         string
	Description  string
	Required     bool
	Nullable     bool
	HasDefault   bool
	DefaultValue interface{}
}

func toolCanonicalName(tool Tool) string {
	if tool.Meta != nil {
		if originalName, ok := tool.Meta["originalName"].(string); ok && originalName != "" {
			return originalName
		}
	}
	return tool.Name
}

func toolLocalName(gameID string, tool Tool) string {
	prefix := gameID + "."

	if canonical := toolCanonicalName(tool); strings.HasPrefix(canonical, prefix) {
		return strings.TrimPrefix(canonical, prefix)
	}
	if strings.HasPrefix(tool.Name, prefix) {
		return strings.TrimPrefix(tool.Name, prefix)
	}

	return toolCanonicalName(tool)
}

func toolBelongsToGame(tool Tool, gameID string) bool {
	prefix := gameID + "."
	return strings.HasPrefix(tool.Name, prefix) || strings.HasPrefix(toolCanonicalName(tool), prefix)
}

func toolMatchesRequestedName(gameID string, tool Tool, requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return false
	}

	gamePrefix := gameID + "."
	canonical := toolCanonicalName(tool)
	registeredLocal := strings.TrimPrefix(tool.Name, gamePrefix)
	canonicalLocal := strings.TrimPrefix(canonical, gamePrefix)
	local := toolLocalName(gameID, tool)

	return requested == tool.Name ||
		requested == canonical ||
		requested == local ||
		requested == registeredLocal ||
		requested == canonicalLocal
}

func gabpToolNameFromTool(gameID string, tool Tool) string {
	mirroredName := toolCanonicalName(tool)
	gamePrefix := gameID + "."

	if strings.HasPrefix(mirroredName, gamePrefix) {
		mirroredName = strings.TrimPrefix(mirroredName, gamePrefix)
	} else if strings.HasPrefix(tool.Name, gamePrefix) {
		mirroredName = strings.TrimPrefix(tool.Name, gamePrefix)
	}

	return strings.ReplaceAll(mirroredName, ".", "/")
}

func toolBriefDescription(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}

	if newline := strings.IndexByte(description, '\n'); newline >= 0 {
		description = strings.TrimSpace(description[:newline])
	}

	if sentenceEnd := strings.Index(description, ". "); sentenceEnd >= 0 {
		description = strings.TrimSpace(description[:sentenceEnd+1])
	}

	const maxLen = 140
	if len(description) <= maxLen {
		return description
	}

	return strings.TrimSpace(description[:maxLen-3]) + "..."
}

func getRequiredSchemaFields(schema map[string]interface{}) map[string]struct{} {
	requiredFields := make(map[string]struct{})
	if schema == nil {
		return requiredFields
	}

	switch required := schema["required"].(type) {
	case []string:
		for _, field := range required {
			requiredFields[field] = struct{}{}
		}
	case []interface{}:
		for _, field := range required {
			if name, ok := field.(string); ok {
				requiredFields[name] = struct{}{}
			}
		}
	}

	return requiredFields
}

func getSchemaTypeString(definition map[string]interface{}) (string, bool) {
	nullable := false

	switch rawType := definition["type"].(type) {
	case string:
		return rawType, nullable
	case []string:
		types := make([]string, 0, len(rawType))
		for _, item := range rawType {
			if item == "null" {
				nullable = true
				continue
			}
			types = append(types, item)
		}
		if len(types) > 0 {
			return strings.Join(types, " | "), nullable
		}
	case []interface{}:
		types := make([]string, 0, len(rawType))
		for _, item := range rawType {
			typeName, ok := item.(string)
			if !ok {
				continue
			}
			if typeName == "null" {
				nullable = true
				continue
			}
			types = append(types, typeName)
		}
		if len(types) > 0 {
			return strings.Join(types, " | "), nullable
		}
	}

	return "any", nullable
}

func getSchemaProperties(schema map[string]interface{}) []toolSchemaProperty {
	if schema == nil {
		return nil
	}

	rawProperties, ok := schema["properties"].(map[string]interface{})
	if !ok || len(rawProperties) == 0 {
		return nil
	}

	requiredFields := getRequiredSchemaFields(schema)
	names := make([]string, 0, len(rawProperties))
	for name := range rawProperties {
		names = append(names, name)
	}
	sort.Strings(names)

	properties := make([]toolSchemaProperty, 0, len(names))
	for _, name := range names {
		property := toolSchemaProperty{
			Name:     name,
			Type:     "any",
			Required: false,
		}

		if _, ok := requiredFields[name]; ok {
			property.Required = true
		}

		if rawDefinition, ok := rawProperties[name].(map[string]interface{}); ok {
			property.Type, property.Nullable = getSchemaTypeString(rawDefinition)
			if description, ok := rawDefinition["description"].(string); ok {
				property.Description = description
			}
			if nullable, ok := rawDefinition["nullable"].(bool); ok && nullable {
				property.Nullable = true
			}
			if defaultValue, ok := rawDefinition["default"]; ok {
				property.HasDefault = true
				property.DefaultValue = defaultValue
			}
		}

		properties = append(properties, property)
	}

	return properties
}

func formatSchemaDefaultValue(value interface{}) string {
	if encoded, err := json.Marshal(value); err == nil {
		return string(encoded)
	}
	return fmt.Sprintf("%v", value)
}

// writeToolParams writes parameter and output schema info for a tool to the content builder
func writeToolParams(content *strings.Builder, tool Tool) {
	inputProperties := getSchemaProperties(tool.InputSchema)
	if len(inputProperties) > 0 {
		content.WriteString("\n  Parameters:")
		for _, property := range inputProperties {
			tags := []string{property.Type}
			if !property.Required {
				tags = append(tags, "optional")
			}
			if property.HasDefault {
				tags = append(tags, "default: "+formatSchemaDefaultValue(property.DefaultValue))
			}

			if property.Description != "" {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s): %s", property.Name, strings.Join(tags, ", "), property.Description))
			} else {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s)", property.Name, strings.Join(tags, ", ")))
			}
		}
	}

	outputProperties := getSchemaProperties(tool.OutputSchema)
	if len(outputProperties) > 0 {
		content.WriteString("\n  Returns:")
		for _, property := range outputProperties {
			tags := []string{property.Type}
			if property.Nullable {
				tags = append(tags, "optional")
			}

			if property.Description != "" {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s): %s", property.Name, strings.Join(tags, ", "), property.Description))
			} else {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s)", property.Name, strings.Join(tags, ", ")))
			}
		}
	}
}

// getGameSpecificTools returns tools that belong to a specific game.
// It prefers explicit game tracking and falls back to prefix matching for
// compatibility with older tests and direct registrations.
func (s *Server) getGameSpecificTools(gameID string) []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{})
	gameTools := make([]Tool, 0)

	addTool := func(tool Tool) {
		if _, exists := seen[tool.Name]; exists {
			return
		}
		if !toolBelongsToGame(tool, gameID) {
			return
		}

		seen[tool.Name] = struct{}{}
		gameTools = append(gameTools, tool)
	}

	if trackedToolNames, exists := s.gameTools[gameID]; exists {
		for _, toolName := range trackedToolNames {
			if handler, exists := s.tools[toolName]; exists {
				addTool(handler.Tool)
			}
		}
	}

	for _, handler := range s.tools {
		addTool(handler.Tool)
	}

	sort.Slice(gameTools, func(i, j int) bool {
		left := toolCanonicalName(gameTools[i])
		right := toolCanonicalName(gameTools[j])
		if left == right {
			return gameTools[i].Name < gameTools[j].Name
		}
		return left < right
	})

	return gameTools
}

// checkGameStatus returns the current status of a game
// getStatusDescription provides a user-friendly description of the game status
func (s *Server) getStatusDescription(gameID string, gameConfig *config.GameConfig) string {
	status := s.checkGameStatus(gameID)
	return s.getStatusDescriptionFromStatus(status, gameConfig)
}

// getStatusDescriptionFromStatus provides a user-friendly description from a status string
// This avoids calling checkGameStatus again when the status is already known
func (s *Server) getStatusDescriptionFromStatus(status string, gameConfig *config.GameConfig) string {
	switch status {
	case process.RuntimeStateStatusStarting:
		return "starting (another GABS session is launching the game)"
	case "shared-running":
		return "running (another GABS session owns the process; use games.connect to attach)"
	case "running-disconnected":
		return "running, but the GABP bridge disconnected"
	case "running":
		// Check if this is a launcher-based game with process tracking
		if gameConfig.LaunchMode == "SteamAppId" || gameConfig.LaunchMode == "EpicAppId" {
			if gameConfig.StopProcessName != "" {
				return "running (GABS is tracking the game process)"
			}
		}
		return "running (GABS controls the process)"
	case "connected":
		return "running (connected via GABP; process not managed by this GABS instance)"
	case "disconnected":
		return "GABP disconnected (the game may have crashed or closed the bridge)"
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

func (s *Server) describeLastGABPDisconnect(gameID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.describeLastGABPDisconnectLocked(gameID)
}

func (s *Server) describeLastGABPDisconnectLocked(gameID string) string {
	record, exists := s.gabpDisconnects[gameID]
	if !exists {
		return ""
	}

	return fmt.Sprintf("Last GABP disconnect at %s: %s", record.At.Format(time.RFC3339), record.Message)
}

func (s *Server) recordGABPDisconnectLocked(gameID string, err error) {
	message := "connection closed"
	if err != nil {
		message = err.Error()
	}

	s.gabpDisconnects[gameID] = gabpDisconnectRecord{
		At:      time.Now().UTC(),
		Message: message,
	}
}

func (s *Server) clearGABPDisconnectLocked(gameID string) {
	delete(s.gabpDisconnects, gameID)
}

// HandleUnexpectedGABPDisconnect records bridge loss and removes mirrored tools immediately.
func (s *Server) HandleUnexpectedGABPDisconnect(gameID string, client *gabp.Client, err error) {
	s.mu.Lock()
	current, exists := s.gabpClients[gameID]
	if !exists || current != client {
		s.mu.Unlock()
		return
	}

	s.recordGABPDisconnectLocked(gameID, err)
	toolsChanged := len(s.gameTools[gameID]) > 0
	resourcesChanged := len(s.gameResources[gameID]) > 0
	s.cleanupGameResourcesInternal(gameID)
	s.mu.Unlock()

	if toolsChanged {
		s.SendToolsListChangedNotification()
	}
	if resourcesChanged {
		s.SendResourcesListChangedNotification()
	}

	s.log.Warnw("unexpected GABP disconnect", "gameId", gameID, "error", err)
}

func (s *Server) resolveSharedRuntimeStatus(gameID string) string {
	runtimeState, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil {
		s.log.Warnw("failed to read shared runtime state", "gameId", gameID, "error", err)
		return ""
	}
	if runtimeState == nil {
		return ""
	}

	status := process.ResolveRuntimeStateStatus(runtimeState)
	if status != "" {
		return status
	}

	if err := process.RemoveRuntimeState(gameID, s.configDir); err != nil {
		s.log.Warnw("failed to remove stale runtime state", "gameId", gameID, "error", err)
	} else {
		s.cleanupBridgeConfigInternal(gameID)
		s.log.Debugw("removed stale runtime state", "gameId", gameID)
	}

	return ""
}

func (s *Server) claimSharedRuntimeState(game config.GameConfig, spec process.LaunchSpec) (process.RuntimeState, error) {
	state := process.NewRuntimeState(spec, process.RuntimeStateStatusStarting)
	state.OwnerInstanceID = s.instanceID

	for attempt := 0; attempt < 2; attempt++ {
		err := process.ClaimRuntimeState(game.ID, s.configDir, state)
		if err == nil {
			return state, nil
		}
		if !errors.Is(err, process.ErrRuntimeStateExists) {
			return process.RuntimeState{}, err
		}

		existingState, loadErr := process.LoadRuntimeState(game.ID, s.configDir)
		if loadErr != nil {
			return process.RuntimeState{}, loadErr
		}
		if status := process.ResolveRuntimeStateStatus(existingState); status != "" {
			return process.RuntimeState{}, &gameAlreadyActiveError{status: status}
		}

		if removeErr := process.RemoveRuntimeState(game.ID, s.configDir); removeErr != nil {
			return process.RuntimeState{}, removeErr
		}

		s.log.Infow("removed stale shared runtime state before retrying launch", "gameId", game.ID)
	}

	return process.RuntimeState{}, fmt.Errorf("failed to claim shared runtime state for %s", game.ID)
}

func (s *Server) cleanupRuntimeStateInternal(gameId string) {
	if err := process.RemoveRuntimeState(gameId, s.configDir); err != nil {
		s.log.Warnw("failed to cleanup runtime state", "gameId", gameId, "error", err)
	}
}

func (s *Server) checkGameStatus(gameID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	controller, exists := s.games[gameID]
	client, clientConnected := s.gabpClients[gameID]
	if !exists {
		if clientConnected {
			if client.IsConnected() {
				return "connected"
			}
			return "disconnected"
		}
		if status := s.resolveSharedRuntimeStatus(gameID); status != "" {
			if status == process.RuntimeStateStatusRunning {
				return "shared-running"
			}
			return status
		}
		return "stopped"
	}

	// Simple stateless approach: directly query the system state
	launchMode := controller.GetLaunchMode()

	// For Steam/Epic launcher games, check the actual game process
	if launchMode == "SteamAppId" || launchMode == "EpicAppId" {
		if controller.IsRunning() {
			if clientConnected && !client.IsConnected() {
				return "running-disconnected"
			}
			return "running" // We can track it and it's running
		} else {
			// Check if the launcher process is still active
			if controller.IsLauncherProcessRunning() {
				return "launcher-running" // Launcher process is still active
			}

			// Launcher has exited - determine if we have tracking capability
			game := s.getGameFromController(controller)
			if game != nil && game.StopProcessName != "" {
				// We have tracking capability but game is not running
				s.cleanupStoppedGame(gameID)
				return "stopped"
			} else {
				// We don't have tracking capability, so we can't know the real status
				return "launcher-triggered" // We started the launcher, but can't track the game
			}
		}
	}

	// For direct processes, check if the process is actually running
	if controller.IsRunning() {
		if clientConnected && !client.IsConnected() {
			return "running-disconnected"
		}
		return "running"
	}

	// Process is dead, clean up
	s.cleanupStoppedGame(gameID)
	return "stopped"
}

// cleanupStoppedGame centralizes the cleanup logic for stopped games
func (s *Server) cleanupStoppedGame(gameID string) {
	// Remove from games map - no need for complex cleanup in stateless approach
	delete(s.games, gameID)

	// Note: The mutex is already held when this is called from checkGameStatus
	// So we call internal cleanup methods that don't acquire locks
	s.cleanupGABPConnectionInternal(gameID)
	s.cleanupGameResourcesInternal(gameID)
	s.cleanupBridgeConfigInternal(gameID)
	s.cleanupRuntimeStateInternal(gameID)
	s.log.Debugw("cleaned up dead game process and resources", "gameId", gameID)
}

// startGame starts a game process using the serialized starter approach
// This implements @pardeike's requirements for serialized, verified process starting
func (s *Server) startGame(game config.GameConfig, gamesConfig *config.GamesConfig, backoffMin, backoffMax time.Duration) (*process.ProcessStartResult, error) {
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
	controller := process.NewController()
	if err := controller.Configure(launchSpec); err != nil {
		return nil, fmt.Errorf("failed to configure game launcher for '%s' (mode: %s, target: %s): %w",
			game.ID, game.LaunchMode, game.Target, err)
	}

	runtimeState, err := s.claimSharedRuntimeState(game, launchSpec)
	if err != nil {
		return nil, err
	}

	cleanupRuntimeState := true
	cleanupBridgeConfig := false
	defer func() {
		if cleanupRuntimeState {
			s.cleanupRuntimeStateInternal(game.ID)
		}
		if cleanupBridgeConfig {
			s.cleanupBridgeConfigInternal(game.ID)
		}
	}()

	// Check if already running (this is still needed for safety)
	s.mu.Lock()
	if trackedController, exists := s.games[game.ID]; exists && trackedController != nil && trackedController.IsRunning() {
		s.mu.Unlock()
		return nil, &gameAlreadyActiveError{status: "running"}
	}

	// Clean up any stale controller reference
	delete(s.games, game.ID)
	s.mu.Unlock()

	// Create GABP bridge configuration (prepare environment variables)
	port, token, bridgePath, err := config.WriteBridgeJSONWithConfig(game.ID, s.configDir, gamesConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create bridge config for game '%s': %w", game.ID, err)
	}
	cleanupBridgeConfig = true

	s.log.Infow("created GABP bridge configuration", "gameId", game.ID, "port", port, "token", token[:8]+"...", "host", "127.0.0.1", "configPath", bridgePath)

	// Set bridge connection info for environment variables
	controller.SetBridgeInfo(port, token)

	// Use serialized starter with verification
	// This implements the asynchronous handling requested by @pardeike
	gabpConnector := NewServerGABPConnector(s, backoffMin, backoffMax)
	result := s.starter.StartWithVerification(controller, gabpConnector, game.ID, port, token)

	if result.Error != nil {
		return result, fmt.Errorf("failed to start game '%s' (mode: %s, target: %s): %w",
			game.ID, game.LaunchMode, game.Target, result.Error)
	}

	if !result.GameStillRunning {
		if result.GABPConnectError != nil {
			return result, fmt.Errorf("game '%s' exited during startup before GABP became available: %w", game.ID, result.GABPConnectError)
		}
		return result, fmt.Errorf("game '%s' exited during startup", game.ID)
	}

	runtimeState.Status = process.RuntimeStateStatusRunning
	runtimeState.GamePID = controller.GetPID()
	if err := process.SaveRuntimeState(game.ID, s.configDir, runtimeState); err != nil {
		s.log.Warnw("failed to persist running runtime state", "gameId", game.ID, "error", err)
	}
	cleanupRuntimeState = false
	cleanupBridgeConfig = false

	// Track the running game
	s.mu.Lock()
	s.games[game.ID] = controller
	s.mu.Unlock()

	// Log the result with detailed status
	logMsg := fmt.Sprintf("game started with GABP bridge (pid: %d, port: %d)", controller.GetPID(), port)
	if result.ProcessStarted {
		logMsg += ", process verified"
	}
	if result.GABPConnected {
		logMsg += ", GABP connected"
	} else {
		logMsg += ", GABP not ready yet"
	}

	s.log.Infow(logMsg,
		"gameId", game.ID,
		"mode", game.LaunchMode,
		"processStarted", result.ProcessStarted,
		"gabpConnected", result.GABPConnected,
		"gabpWait", result.GABPConnectWait,
		"gabpError", result.GABPConnectError)

	return result, nil
}

// establishGABPConnection attempts to connect to the game's GABP server with retry logic
// This runs in the background and implements the Future Enhancement workflow:
//  1. Game starts with bridge config (already done in startGame)
//  2. GABP client connects to game mod's server (implemented here)
//  3. Mirror system syncs tools and sends tools/list_changed notification (implemented here)
//  4. AI agents automatically discover new capabilities via games.tool_names,
//     then inspect a few candidates with games.tool_detail (enabled by notification)
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err := client.Connect(ctx, addr, token, backoffMin, backoffMax)
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
	return s.syncGABPToolsWithTimeout(client, gameID, 30*time.Second)
}

func (s *Server) syncGABPToolsWithTimeout(client *gabp.Client, gameID string, timeout time.Duration) error {
	// Get tools from GABP client
	gabpTools, err := client.ListToolsWithTimeout(timeout)
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
			Name:         gameSpecificName,
			Description:  fmt.Sprintf("%s (Game: %s)", tool.Description, gameID),
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
		}

		// Create handler that forwards to GABP with original tool name
		originalToolName := tool.Name // Capture original name for GABP call
		handler := func(toolName string) func(args map[string]interface{}) (*ToolResult, error) {
			return func(args map[string]interface{}) (*ToolResult, error) {
				proxyTimeout, invalidTimeout := deriveMirroredToolCallTimeout(args, 30*time.Second)
				if invalidTimeout != nil {
					return invalidTimeout, nil
				}

				// Call GABP with original tool name (without game prefix)
				result, isError, err := client.CallToolWithTimeout(toolName, args, proxyTimeout)
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

	// Send tools/list_changed notification to AI agents. This alerts clients
	// that new mirrored tools are available without polling, so they can refresh
	// direct tool access or use games.tool_names -> games.tool_detail for the
	// stable discovery flow.
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
	trackedToolName := tool.Name
	if normalizationConfig != nil && normalizationConfig.EnableOpenAINormalization {
		normalizedResult := util.NormalizeToolNameForOpenAI(tool.Name, normalizationConfig.MaxToolNameLength)
		if normalizedResult.WasNormalized {
			trackedToolName = normalizedResult.NormalizedName
		}
	}

	s.mu.Lock()
	for _, existing := range s.gameTools[gameId] {
		if existing == trackedToolName {
			s.mu.Unlock()
			return
		}
	}
	s.gameTools[gameId] = append(s.gameTools[gameId], trackedToolName)
	s.mu.Unlock()
}

// RegisterGameResource registers a resource for a specific game and tracks it for cleanup
func (s *Server) RegisterGameResource(gameId string, resource Resource, handler func() ([]Content, error)) {
	s.RegisterResource(resource, handler)

	// Track which game this resource belongs to
	s.mu.Lock()
	for _, existing := range s.gameResources[gameId] {
		if existing == resource.URI {
			s.mu.Unlock()
			return
		}
	}
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
	delete(s.gabpDisconnects, gameId)
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

// Internal cleanup methods that don't acquire locks (for use when mutex is already held)

// cleanupGameResourcesInternal removes game-specific resources without acquiring mutex
func (s *Server) cleanupGameResourcesInternal(gameId string) {
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

		// Note: We cannot send notifications here because that might require acquiring locks
		// The caller should handle notifications separately if needed
	}
}

// cleanupGABPConnectionInternal cleans up GABP connection without acquiring mutex
func (s *Server) cleanupGABPConnectionInternal(gameId string) {
	// Clean up GABP client connection
	if client, exists := s.gabpClients[gameId]; exists {
		if err := client.Close(); err != nil {
			s.log.Warnw("error closing GABP client", "gameId", gameId, "error", err)
		}
		delete(s.gabpClients, gameId)
		s.log.Debugw("cleaned up GABP client connection", "gameId", gameId)
	}
	delete(s.gabpDisconnects, gameId)
}

// cleanupBridgeConfigInternal removes bridge config without acquiring mutex
func (s *Server) cleanupBridgeConfigInternal(gameId string) {
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
	// MCP stdio uses Content-Length framing. Keep newline-delimited JSON as a
	// fallback so existing local clients keep working.
	reader := util.NewAutoFrameReader(r)
	writer := util.NewAutoFrameWriter(w)
	writerRegistered := false

	// Clean up writer on exit
	defer func() {
		if writerRegistered {
			s.writersMu.Lock()
			// Find and remove writer from slice (safer than using index)
			for i, w := range s.writers {
				if w == writer {
					s.writers = append(s.writers[:i], s.writers[i+1:]...)
					break
				}
			}
			s.writersMu.Unlock()
		}
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

		if !writerRegistered {
			writer.SetMode(reader.Mode())
			s.writersMu.Lock()
			s.writers = append(s.writers, writer)
			s.writersMu.Unlock()
			writerRegistered = true
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
	if msg.ID == nil {
		return s.handleNotification(msg)
	}

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

func (s *Server) handleNotification(msg *Message) *Message {
	switch msg.Method {
	case "notifications/initialized", "initialized":
		s.log.Debugw("client initialized notification received")
	default:
		// Notifications never receive responses. Ignore unsupported ones so
		// spec-compliant clients can continue after initialize.
		s.log.Debugw("ignoring unsupported notification", "method", msg.Method)
	}

	return nil
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
