package mirror

import (
	"fmt"
	"strings"

	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/util"
)

// Mirror maps GABP tools/resources/events to MCP.
type Mirror struct {
	log    util.Logger
	server *mcp.Server
	client *gabp.Client
	gameId string // Game ID for namespacing tools
}

func New(log util.Logger, server *mcp.Server, client *gabp.Client, gameId string) *Mirror {
	return &Mirror{
		log:    log,
		server: server,
		client: client,
		gameId: gameId,
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
		// Convert slashes to dots for reverse domain notation, keep dots as-is
		sanitizedToolName := strings.ReplaceAll(tool.Name, "/", ".")
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

		m.server.RegisterTool(mcpTool, handler)
		m.log.Debugw("registered GABP tool as game-specific MCP tool", "gameId", m.gameId, "originalName", tool.Name, "mcpName", gameSpecificName)
	}

	m.log.Infow("synced GABP tools to MCP with game namespacing", "gameId", m.gameId, "count", len(gabpTools))

	// TODO: Future enhancement - send tools/list_changed notification to AI agents
	// This would automatically alert AI agents that new tools are available without
	// them needing to poll. AI agents would then use games.tools to discover the
	// new capabilities.
	//
	// Notification format:
	// {"method": "notifications/tools/list_changed", "params": {}}
	//
	// This follows MCP specification for server-initiated notifications and ensures
	// AI agents are immediately aware of dynamic tool expansion.

	return nil
}

func (m *Mirror) ExposeResources() error {
	// TODO: Expose GABP event channels as MCP resources
	// For now, expose a basic logs resource with game-specific naming
	logsResource := mcp.Resource{
		URI:         fmt.Sprintf("gab://%s/events/logs", m.gameId),
		Name:        fmt.Sprintf("%s Event Logs", m.gameId),
		Description: fmt.Sprintf("Recent GABP events and logs for game: %s", m.gameId),
		MimeType:    "application/json",
	}

	handler := func() ([]mcp.Content, error) {
		// Return game-specific content
		return []mcp.Content{
			{Type: "text", Text: fmt.Sprintf("Event logs for game %s would appear here", m.gameId)},
		}, nil
	}

	m.server.RegisterResource(logsResource, handler)
	m.log.Infow("exposed GABP resources as game-specific MCP resources", "gameId", m.gameId)
	return nil
}
