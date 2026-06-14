package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

func TestToolsListHidesTrackedGameToolsButKeepsDiscoveryAndCalls(t *testing.T) {
	logger := util.NewLogger("error")
	server := NewServerForTesting(logger)

	gamesConfig := &config.GamesConfig{}
	if err := gamesConfig.AddGame(config.GameConfig{
		ID:         "adventure",
		Name:       "AdventureGame",
		LaunchMode: "DirectPath",
		Target:     "/bin/true",
	}); err != nil {
		t.Fatalf("add game: %v", err)
	}
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 5*time.Second)

	server.RegisterGameTool("adventure", Tool{
		Name:        "adventure.map_snapshot",
		Description: "Read map state",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{
			Content:           []Content{{Type: "text", Text: "snapshot ok"}},
			StructuredContent: map[string]interface{}{"ok": true},
		}, nil
	}, &config.ToolNormalizationConfig{})

	listResponse := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/list",
		ID:      json.RawMessage(`"list"`),
	})
	if listResponse == nil || listResponse.Error != nil {
		t.Fatalf("tools/list failed: %#v", listResponse)
	}

	var listResult ToolsListResult
	if err := decodeResult(listResponse.Result, &listResult); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	for _, tool := range listResult.Tools {
		if tool.Name == "adventure.map_snapshot" {
			t.Fatal("tracked game tool leaked into public tools/list")
		}
	}

	namesResponse := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"names"`),
		Params: map[string]interface{}{
			"name": "games_tool_names",
			"arguments": map[string]interface{}{
				"gameId": "adventure",
				"brief":  true,
			},
		},
	})
	if namesResponse == nil || namesResponse.Error != nil {
		t.Fatalf("games_tool_names failed: %#v", namesResponse)
	}
	namesBytes, err := json.Marshal(namesResponse.Result)
	if err != nil {
		t.Fatalf("marshal games_tool_names result: %v", err)
	}
	if !strings.Contains(string(namesBytes), "adventure.map_snapshot") {
		t.Fatalf("game tool was not discoverable through games_tool_names: %s", namesBytes)
	}

	callResponse := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"direct-call"`),
		Params: map[string]interface{}{
			"name":      "adventure.map_snapshot",
			"arguments": map[string]interface{}{},
		},
	})
	if callResponse == nil || callResponse.Error != nil {
		t.Fatalf("direct tracked game tool call failed: %#v", callResponse)
	}
	callBytes, err := json.Marshal(callResponse.Result)
	if err != nil {
		t.Fatalf("marshal direct call result: %v", err)
	}
	if !strings.Contains(string(callBytes), "snapshot ok") {
		t.Fatalf("unexpected direct call result: %s", callBytes)
	}
}

func decodeResult(result interface{}, target interface{}) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
