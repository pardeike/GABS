package mirror

import (
	"fmt"

	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/util"
)

// Mirror maps GABP tools/resources/events to MCP.
type Mirror struct {
	log    util.Logger
	server *mcp.Server
	client *gabp.Client
}

func New(log util.Logger, server *mcp.Server, client *gabp.Client) *Mirror {
	return &Mirror{
		log:    log,
		server: server,
		client: client,
	}
}

func (m *Mirror) SyncTools() error {
	// Get tools from GABP client
	gabpTools, err := m.client.ListTools()
	if err != nil {
		return fmt.Errorf("failed to list GABP tools: %w", err)
	}

	// Register each GABP tool as an MCP tool
	for _, tool := range gabpTools {
		mcpTool := mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		}
		
		// Create handler that forwards to GABP
		handler := func(toolName string) func(args map[string]interface{}) (*mcp.ToolResult, error) {
			return func(args map[string]interface{}) (*mcp.ToolResult, error) {
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
		}(tool.Name)

		m.server.RegisterTool(mcpTool, handler)
		m.log.Debugw("registered GABP tool as MCP tool", "name", tool.Name)
	}

	m.log.Infow("synced GABP tools to MCP", "count", len(gabpTools))
	return nil
}

func (m *Mirror) ExposeResources() error {
	// TODO: Expose GABP event channels as MCP resources
	// For now, expose a basic logs resource
	logsResource := mcp.Resource{
		URI:         "gab://events/logs",
		Name:        "Event Logs",
		Description: "Recent GABP events and logs",
		MimeType:    "application/json",
	}

	handler := func() ([]mcp.Content, error) {
		// Return empty content for now - this would fetch recent events
		return []mcp.Content{
			{Type: "text", Text: "Event logs would appear here"},
		}, nil
	}

	m.server.RegisterResource(logsResource, handler)
	m.log.Infow("exposed GABP resources as MCP resources")
	return nil
}
