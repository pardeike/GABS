package mcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

func compactDiscoveryServer(t *testing.T) *Server {
	t.Helper()
	server := NewServerForTesting(t, util.NewLogger("error"))
	cfg := &config.GamesConfig{}
	for _, gameID := range []string{"factory", "adventure"} {
		if err := cfg.AddGame(config.GameConfig{
			ID: gameID, Name: gameID, LaunchMode: "DirectPath", Target: "/opt/example/GameName.exe",
		}); err != nil {
			t.Fatal(err)
		}
	}
	server.RegisterGameManagementTools(cfg, 0, 0)
	for _, gameID := range []string{"factory", "adventure"} {
		registerCompactDiscoveryTool(server, gameID, "map/summary")
	}
	return server
}

func registerCompactDiscoveryTool(server *Server, gameID, gabpName string) {
	server.RegisterGameTool(gameID, Tool{
		Name: safeMCPToolName(gameID, gabpName, 64), Description: "Read a compact map summary.",
		InputSchema:  map[string]interface{}{"type": "object"},
		OutputSchema: map[string]interface{}{"type": "object"},
		Meta: map[string]interface{}{
			toolMetaGABPName: gabpName, "originalName": legacyMCPToolName(gameID, gabpName),
			toolMetaTags: []string{"read-only"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{}, nil
	}, &config.ToolNormalizationConfig{})
}

func compactDiscoveryCall(t *testing.T, server *Server, name string, args map[string]interface{}) ToolResult {
	t.Helper()
	response := server.HandleMessage(&Message{
		JSONRPC: "2.0", Method: "tools/call", ID: json.RawMessage(`"compact-discovery"`),
		Params: map[string]interface{}{"name": name, "arguments": args},
	})
	if response == nil || response.Error != nil {
		t.Fatalf("%s failed: %#v", name, response)
	}
	var result ToolResult
	if err := decodeResult(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func compactDiscoveryItems(t *testing.T, result ToolResult, key string) []map[string]interface{} {
	t.Helper()
	var items []map[string]interface{}
	if err := decodeResult(result.StructuredContent[key], &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestCompactGameToolDiscovery(t *testing.T) {
	server := compactDiscoveryServer(t)
	for _, brief := range []bool{false, true} {
		for _, scoped := range []bool{false, true} {
			t.Run(fmt.Sprintf("brief=%t/scoped=%t", brief, scoped), func(t *testing.T) {
				args := map[string]interface{}{"brief": brief}
				if scoped {
					args["gameId"] = "factory"
				}
				result := compactDiscoveryCall(t, server, "games_tool_names", args)
				if result.IsError {
					t.Fatalf("discovery failed: %+v", result)
				}
				items := compactDiscoveryItems(t, result, "tools")
				wantCount := 2
				if scoped {
					wantCount = 1
					if result.StructuredContent["gameId"] != "factory" {
						t.Fatal("single-game response must retain top-level gameId")
					}
				}
				if len(items) != wantCount {
					t.Fatalf("got %d tools, want %d", len(items), wantCount)
				}
				for _, item := range items {
					gameID := "factory"
					if !scoped {
						gameID, _ = item["gameId"].(string)
						if gameID != "factory" && gameID != "adventure" {
							t.Fatalf("missing game identity: %v", item)
						}
					}
					want := map[string]interface{}{
						"name": gameID + "_map_summary", "tags": []interface{}{"read-only"},
					}
					if brief {
						want["summary"] = "Read a compact map summary."
					}
					if !scoped {
						want["gameId"] = gameID
					}
					if !reflect.DeepEqual(item, want) {
						t.Fatalf("compact item = %v, want %v", item, want)
					}
				}
			})
		}
	}

	t.Run("aliases and detail metadata", func(t *testing.T) {
		for _, alias := range []string{"factory_map_summary", "factory.map.summary", "factory/map/summary", "map/summary", "map.summary"} {
			result := compactDiscoveryCall(t, server, "games_tool_detail", map[string]interface{}{"gameId": "factory", "tool": alias})
			if result.IsError {
				t.Fatalf("alias %q stopped resolving: %+v", alias, result)
			}
			for key, want := range map[string]string{"name": "factory_map_summary", "gameId": "factory", "localName": "map/summary", "gabpName": "map/summary", "originalName": "factory.map.summary"} {
				if result.StructuredContent[key] != want {
					t.Fatalf("detail %s = %v, want %q", key, result.StructuredContent[key], want)
				}
			}
		}
		result := compactDiscoveryCall(t, server, "games_tools", map[string]interface{}{"gameId": "factory"})
		item := compactDiscoveryItems(t, result, "tools")[0]
		for _, key := range []string{"gameId", "localName", "gabpName", "originalName", "inputSchema", "outputSchema"} {
			if _, ok := item[key]; !ok {
				t.Fatalf("detailed compatibility listing lost %s", key)
			}
		}
	})

	t.Run("filtering and pagination", func(t *testing.T) {
		for _, filter := range []map[string]interface{}{{"query": "factory.map"}, {"prefix": "map/"}, {"prefix": "factory_map"}} {
			filter["gameId"] = "factory"
			result := compactDiscoveryCall(t, server, "games_tool_names", filter)
			if len(compactDiscoveryItems(t, result, "tools")) != 1 {
				t.Fatalf("filter no longer matches: %v", filter)
			}
		}
		first := compactDiscoveryCall(t, server, "games_tool_names", map[string]interface{}{"limit": 1})
		second := compactDiscoveryCall(t, server, "games_tool_names", map[string]interface{}{"limit": 1, "cursor": first.StructuredContent["nextCursor"]})
		if first.StructuredContent["nextCursor"] != "1" || second.StructuredContent["nextCursor"] != "" ||
			compactDiscoveryItems(t, first, "tools")[0]["gameId"] != "adventure" || compactDiscoveryItems(t, second, "tools")[0]["gameId"] != "factory" {
			t.Fatal("pagination must retain sorted game identities")
		}
	})

	t.Run("compact missing-tool candidates", func(t *testing.T) {
		for _, scoped := range []bool{false, true} {
			args := map[string]interface{}{"tool": "map"}
			if scoped {
				args["gameId"] = "factory"
			}
			result := compactDiscoveryCall(t, server, "games_tool_detail", args)
			items := compactDiscoveryItems(t, result, "candidates")
			if !result.IsError || len(items) == 0 {
				t.Fatalf("expected recovery candidates: %+v", result)
			}
			for _, item := range items {
				for _, key := range []string{"localName", "gabpName", "originalName"} {
					if _, ok := item[key]; ok {
						t.Fatalf("candidate repeats %s", key)
					}
				}
				_, hasGameID := item["gameId"]
				if hasGameID == scoped {
					t.Fatal("candidate gameId must follow response scope")
				}
			}
		}
	})
}

func TestCompactDiscoveryNameCanBeCalled(t *testing.T) {
	server, done := newGamesCallToolTimeoutTestServer(t, 0)
	connect := compactDiscoveryCall(t, server, "games_connect", map[string]interface{}{"gameId": "adventure"})
	if connect.IsError {
		t.Fatalf("connect failed: %+v", connect)
	}
	names := compactDiscoveryCall(t, server, "games_tool_names", map[string]interface{}{"gameId": "adventure", "brief": true})
	items := compactDiscoveryItems(t, names, "tools")
	if len(items) != 1 {
		t.Fatalf("expected one bridge tool: %+v", names)
	}
	name := items[0]["name"]
	detail := compactDiscoveryCall(t, server, "games_tool_detail", map[string]interface{}{"tool": name})
	if detail.IsError || detail.StructuredContent["gabpName"] != "adventure/load_game_ready" {
		t.Fatalf("discovered name did not resolve: %+v", detail)
	}
	call := compactDiscoveryCall(t, server, "games_call_tool", map[string]interface{}{"tool": name})
	if call.IsError || call.StructuredContent["status"] != "loaded" {
		t.Fatalf("discovered name could not be called: %+v", call)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test bridge did not finish")
	}
}

func TestCompactDiscoveryResponseSize(t *testing.T) {
	server := compactDiscoveryServer(t)
	gabpNames := map[string]string{"factory_map_summary": "map/summary"}
	for i := 0; i < 137; i++ {
		gabpName := fmt.Sprintf("map/tool_%03d", i)
		registerCompactDiscoveryTool(server, "factory", gabpName)
		gabpNames[safeMCPToolName("factory", gabpName, 64)] = gabpName
	}
	result := compactDiscoveryCall(t, server, "games_tool_names", map[string]interface{}{"gameId": "factory", "brief": true, "limit": 138})
	items := compactDiscoveryItems(t, result, "tools")
	if len(items) != 138 {
		t.Fatalf("expected 138 tools, got %d", len(items))
	}
	compact, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the previous response fields, keeping text and summaries identical.
	for _, item := range items {
		localName := gabpNames[item["name"].(string)]
		item["gameId"] = "factory"
		item["localName"] = localName
		item["gabpName"] = localName
		item["originalName"] = legacyMCPToolName("factory", localName)
	}
	result.StructuredContent["tools"] = items
	previous, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) >= len(previous) {
		t.Fatal("compact discovery must reduce response size")
	}
	t.Logf("138-tool response: %d -> %d JSON bytes (%.1f%% smaller)", len(previous), len(compact), 100*(1-float64(len(compact))/float64(len(previous))))
}
