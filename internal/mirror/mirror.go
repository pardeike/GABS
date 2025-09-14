package mirror

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/util"
)

// GABPClient interface for Mirror to work with both real and mock GABP clients
type GABPClient interface {
	ListTools() ([]gabp.ToolDescriptor, error)
	GetCapabilities() gabp.Capabilities
	CallTool(name string, args map[string]any) (map[string]any, bool, error)
}

// MCPServer interface for Mirror to work with both real and mock servers
type MCPServer interface {
	RegisterToolWithConfig(tool mcp.Tool, handler func(args map[string]interface{}) (*mcp.ToolResult, error), normalizationConfig *config.ToolNormalizationConfig)
	RegisterResource(resource mcp.Resource, handler func() ([]mcp.Content, error))
	SendToolsListChangedNotification()
	SendResourcesListChangedNotification()
}

// Mirror maps GABP tools/resources/events to MCP.
type Mirror struct {
	log                 util.Logger
	server              MCPServer
	client              GABPClient
	gameId              string // Game ID for namespacing tools
	normalizationConfig *config.ToolNormalizationConfig
}

func New(log util.Logger, server MCPServer, client GABPClient, gameId string, normalizationConfig *config.ToolNormalizationConfig) *Mirror {
	return &Mirror{
		log:                 log,
		server:              server,
		client:              client,
		gameId:              gameId,
		normalizationConfig: normalizationConfig,
	}
}

func (m *Mirror) SyncTools() error {
	// Get tools from GABP client
	gabpTools, err := m.client.ListTools()
	if err != nil {
		return fmt.Errorf("failed to list GABP tools: %w", err)
	}

	// Register each GABP tool as an MCP tool with game-specific naming
	for _, tool := range gabpTools {
		// Create game-prefixed tool name for multi-game clarity
		// Apply basic normalization first (convert slashes to dots)
		sanitizedToolName := util.NormalizeToolNameBasic(tool.Name)
		gameSpecificName := fmt.Sprintf("%s.%s", m.gameId, sanitizedToolName)

		mcpTool := mcp.Tool{
			Name:        gameSpecificName,
			Description: fmt.Sprintf("%s (Game: %s)", tool.Description, m.gameId),
			InputSchema: tool.InputSchema,
		}

		// Create handler that forwards to GABP with original tool name
		originalToolName := tool.Name // Capture original name for GABP call
		handler := func(toolName string) func(args map[string]interface{}) (*mcp.ToolResult, error) {
			return func(args map[string]interface{}) (*mcp.ToolResult, error) {
				// Call GABP with original tool name (without game prefix)
				result, isError, err := m.client.CallTool(toolName, args)
				if err != nil {
					return &mcp.ToolResult{
						Content: []mcp.Content{{Type: "text", Text: err.Error()}},
						IsError: true,
					}, nil
				}

				if isError {
					return &mcp.ToolResult{
						Content:           []mcp.Content{{Type: "text", Text: fmt.Sprintf("Tool error: %v", result)}},
						StructuredContent: result,
						IsError:           true,
					}, nil
				}

				// Convert result to MCP format
				content := []mcp.Content{}
				if resultText, ok := result["text"].(string); ok {
					content = append(content, mcp.Content{Type: "text", Text: resultText})
				} else {
					content = append(content, mcp.Content{Type: "text", Text: fmt.Sprintf("%v", result)})
				}

				return &mcp.ToolResult{
					Content:           content,
					StructuredContent: result,
					IsError:           false,
				}, nil
			}
		}(originalToolName)

		// Register the tool with normalization config
		m.server.RegisterToolWithConfig(mcpTool, handler, m.normalizationConfig)
		m.log.Debugw("registered GABP tool as game-specific MCP tool", "gameId", m.gameId, "originalName", tool.Name, "mcpName", gameSpecificName)
	}

	m.log.Infow("synced GABP tools to MCP with game namespacing", "gameId", m.gameId, "count", len(gabpTools))

	// Send tools/list_changed notification to AI agents
	// This automatically alerts AI agents that new tools are available without
	// them needing to poll. AI agents can then use games.tools to discover the
	// new capabilities.
	//
	// This follows MCP specification for server-initiated notifications and ensures
	// AI agents are immediately aware of dynamic tool expansion.
	m.server.SendToolsListChangedNotification()

	return nil
}

func (m *Mirror) ExposeResources() error {
	// Implement comprehensive GABP resource mirroring
	// - Expose actual GABP event channels as MCP resources with streaming support
	// - Mirror game state resources (world data, player stats, etc.)
	// - Add real-time GABP event streaming via MCP resource updates
	// - Support filtering and historical event querying

	// Basic event logs resource (enhanced from placeholder)
	logsResource := mcp.Resource{
		URI:         fmt.Sprintf("gab://%s/events/logs", m.gameId),
		Name:        fmt.Sprintf("%s Event Logs", m.gameId),
		Description: fmt.Sprintf("Real-time GABP events and logs for game: %s", m.gameId),
		MimeType:    "application/json",
	}

	logsHandler := func() ([]mcp.Content, error) {
		// Enhanced content with structured event information
		eventData := map[string]interface{}{
			"gameId":     m.gameId,
			"timestamp":  fmt.Sprintf("%d", time.Now().Unix()),
			"eventType":  "system",
			"message":    fmt.Sprintf("GABP bridge active for game %s", m.gameId),
			"source":     "gabs-mirror",
			"capabilities": m.client.GetCapabilities(),
		}

		eventJson, _ := json.Marshal(eventData)
		
		return []mcp.Content{
			{Type: "text", Text: string(eventJson)},
		}, nil
	}

	// Game state resource for exposing current game information
	stateResource := mcp.Resource{
		URI:         fmt.Sprintf("gab://%s/state", m.gameId),
		Name:        fmt.Sprintf("%s Game State", m.gameId),
		Description: fmt.Sprintf("Current state and capabilities of game: %s", m.gameId),
		MimeType:    "application/json",
	}

	stateHandler := func() ([]mcp.Content, error) {
		// Get current tools to show game capabilities
		tools, err := m.client.ListTools()
		if err != nil {
			return []mcp.Content{
				{Type: "text", Text: fmt.Sprintf("Error retrieving game state: %v", err)},
			}, nil
		}

		stateData := map[string]interface{}{
			"gameId":       m.gameId,
			"connected":    true,
			"toolCount":    len(tools),
			"capabilities": m.client.GetCapabilities(),
			"availableTools": func() []string {
				var toolNames []string
				for _, tool := range tools {
					toolNames = append(toolNames, tool.Name)
				}
				return toolNames
			}(),
			"lastUpdate":   fmt.Sprintf("%d", time.Now().Unix()),
		}

		stateJson, _ := json.Marshal(stateData)
		
		return []mcp.Content{
			{Type: "text", Text: string(stateJson)},
		}, nil
	}

	// Event stream resource for real-time events (when supported)
	streamResource := mcp.Resource{
		URI:         fmt.Sprintf("gab://%s/events/stream", m.gameId),
		Name:        fmt.Sprintf("%s Event Stream", m.gameId),
		Description: fmt.Sprintf("Real-time event stream from game: %s (when GABP events are configured)", m.gameId),
		MimeType:    "application/x-ndjson", // Newline-delimited JSON for streaming
	}

	streamHandler := func() ([]mcp.Content, error) {
		// This would be enhanced when GABP event subscription is fully implemented
		// For now, return information about available event channels
		capabilities := m.client.GetCapabilities()
		streamInfo := map[string]interface{}{
			"gameId":      m.gameId,
			"streamType":  "gabp-events",
			"status":      "available",
			"description": "Real-time GABP events will appear here when the game mod supports event streaming",
			"channels":    capabilities.Events, // Available event channels
			"note":        "Use GABP client event subscription to receive real-time updates",
		}

		streamJson, _ := json.Marshal(streamInfo)
		
		return []mcp.Content{
			{Type: "text", Text: string(streamJson)},
		}, nil
	}

	// Register all resources
	m.server.RegisterResource(logsResource, logsHandler)
	m.server.RegisterResource(stateResource, stateHandler)
	m.server.RegisterResource(streamResource, streamHandler)

	m.log.Infow("exposed comprehensive GABP resources as game-specific MCP resources", 
		"gameId", m.gameId, 
		"resources", []string{"logs", "state", "stream"})

	// Send resources/list_changed notification to alert AI agents
	m.server.SendResourcesListChangedNotification()
	
	return nil
}
