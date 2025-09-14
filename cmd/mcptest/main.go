package main

import (
	"fmt"
	"os"

	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/util"
)

func main() {
	log := util.NewLogger("info")
	server := mcp.NewServer(log)

	// Register a test tool
	testTool := mcp.Tool{
		Name:        "test.echo",
		Description: "Echo back the input",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message": map[string]interface{}{
					"type":        "string",
					"description": "Message to echo back",
				},
			},
			"required": []string{"message"},
		},
	}

	server.RegisterTool(testTool, func(args map[string]interface{}) (*mcp.ToolResult, error) {
		message, ok := args["message"].(string)
		if !ok {
			message = "No message provided"
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("Echo: %s", message),
			}},
		}, nil
	})

	// Register a test resource
	testResource := mcp.Resource{
		URI:         "test://info",
		Name:        "Test Info",
		Description: "Basic test information",
		MimeType:    "text/plain",
	}

	server.RegisterResource(testResource, func() ([]mcp.Content, error) {
		return []mcp.Content{{
			Type: "text",
			Text: "This is a test resource from GABS MCP server",
		}}, nil
	})

	fmt.Fprintln(os.Stderr, "MCP server ready on stdio")

	// Serve on stdio
	if err := server.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
