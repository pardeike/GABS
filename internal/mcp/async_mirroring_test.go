package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/util"
)

func TestDelimitedGABPToolNameCandidates(t *testing.T) {
	tests := []struct {
		name      string
		gameID    string
		requested string
		expected  []string
	}{
		{
			name:      "mirrored adventure tool",
			gameID:    "adventure",
			requested: "adventure.adventure.start_debug_game_ready",
			expected:  []string{"adventure/adventure/start_debug_game_ready", "adventure/start_debug_game_ready"},
		},
		{
			name:      "local adventure tool with explicit game",
			gameID:    "adventure",
			requested: "adventure.start_debug_game_ready",
			expected:  []string{"adventure/start_debug_game_ready", "start_debug_game_ready"},
		},
		{
			name:      "plain gabp tool",
			gameID:    "adventure",
			requested: "corebridge.core.ping",
			expected:  []string{"corebridge/core/ping"},
		},
		{
			name:      "duplicates are removed",
			gameID:    "adventure",
			requested: "adventure/corebridge/core/ping",
			expected:  []string{"adventure/corebridge/core/ping", "corebridge/core/ping"},
		},
		{
			name:      "strict-safe tool names are descriptor resolved, not guessed",
			gameID:    "adventure",
			requested: "adventure_corebridge_core_ping",
			expected:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := gabpToolNameFromDelimitedRequest(tt.gameID, tt.requested)
			if !reflect.DeepEqual(actual, tt.expected) {
				t.Fatalf("expected %#v, got %#v", tt.expected, actual)
			}
		})
	}
}

func TestAsyncConnectorAllowsGamesCallToolBeforeMirroring(t *testing.T) {
	server, port, bridgeToken, serverDone := newAsyncMirroringTestServer(t)

	connector := newServerGABPConnector(server, 5*time.Millisecond, 10*time.Millisecond, false, 250*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	connectStarted := time.Now()
	if err := connector.AttemptConnection(ctx, "adventure", port, bridgeToken); err != nil {
		t.Fatalf("expected async connector to connect: %v", err)
	}
	if elapsed := time.Since(connectStarted); elapsed > 150*time.Millisecond {
		t.Fatalf("expected async connector to return before mirroring, took %v", elapsed)
	}

	callText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"direct-before-mirror"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"gameId": "adventure",
				"tool":   "adventure.corebridge.core.ping",
			},
		},
	}))

	assertPongToolResult(t, callText)
	assertAsyncMirroringServerDone(t, serverDone)
}

func TestGamesToolNamesForcesInitialMirrorSync(t *testing.T) {
	server, port, bridgeToken, serverDone := newAsyncMirroringDiscoveryTestServer(t)

	connector := newServerGABPConnector(server, 5*time.Millisecond, 10*time.Millisecond, false, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := connector.AttemptConnection(ctx, "adventure", port, bridgeToken); err != nil {
		t.Fatalf("expected async connector to connect: %v", err)
	}

	namesText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"names-before-mirror"`),
		Params: map[string]interface{}{
			"name": "games.tool_names",
			"arguments": map[string]interface{}{
				"gameId": "adventure",
				"brief":  true,
				"query":  "ping",
			},
		},
	}))

	if strings.Contains(namesText, `"isError":true`) {
		t.Fatalf("expected discovery to succeed before async mirror, got: %s", namesText)
	}
	if !strings.Contains(namesText, "adventure_corebridge_core_ping") {
		t.Fatalf("expected discovery to force initial mirror sync, got: %s", namesText)
	}
	assertAsyncMirroringServerDone(t, serverDone)
}

func TestUnmirroredDirectMCPToolCallUsesGABPFallback(t *testing.T) {
	server, port, bridgeToken, serverDone := newAsyncMirroringTestServer(t)

	connector := newServerGABPConnector(server, 5*time.Millisecond, 10*time.Millisecond, false, 250*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := connector.AttemptConnection(ctx, "adventure", port, bridgeToken); err != nil {
		t.Fatalf("expected async connector to connect: %v", err)
	}

	callText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"unmirrored-direct"`),
		Params: map[string]interface{}{
			"name":      "adventure.corebridge.core.ping",
			"arguments": map[string]interface{}{},
		},
	}))

	assertPongToolResult(t, callText)
	assertAsyncMirroringServerDone(t, serverDone)
}

func TestUnmirroredStrictSafeDirectMCPToolCallUsesDescriptorAliasFallback(t *testing.T) {
	server, port, bridgeToken, serverDone := newAsyncMirroringDescriptorAliasTestServer(t)

	connector := newServerGABPConnector(server, 5*time.Millisecond, 10*time.Millisecond, false, 250*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := connector.AttemptConnection(ctx, "adventure", port, bridgeToken); err != nil {
		t.Fatalf("expected async connector to connect: %v", err)
	}

	callText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"unmirrored-normalized-direct"`),
		Params: map[string]interface{}{
			"name":      "adventure_corebridge_core_ping",
			"arguments": map[string]interface{}{},
		},
	}))

	assertPongToolResult(t, callText)
	assertAsyncMirroringServerDone(t, serverDone)
}

func newAsyncMirroringDescriptorAliasTestServer(t *testing.T) (*Server, int, string, <-chan error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	bridgeToken := "async-mirroring-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionExpectListBeforeSafeToolCall(listener, bridgeToken, serverDone)

	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{
			"adventure": {
				ID:         "adventure",
				Name:       "AdventureGame",
				LaunchMode: "DirectPath",
				Target:     "/Applications/AdventureGameMac.app/Contents/MacOS/AdventureGame by ExampleStudio Studios",
			},
		},
	}

	log := util.NewLogger("error")
	server := NewServerForTesting(log)
	server.RegisterGameManagementTools(gamesConfig, 5*time.Millisecond, 10*time.Millisecond)

	return server, listener.Addr().(*net.TCPAddr).Port, bridgeToken, serverDone
}

func newAsyncMirroringTestServer(t *testing.T) (*Server, int, string, <-chan error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	bridgeToken := "async-mirroring-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionExpectToolCallBeforeList(listener, bridgeToken, serverDone)

	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{
			"adventure": {
				ID:         "adventure",
				Name:       "AdventureGame",
				LaunchMode: "DirectPath",
				Target:     "/Applications/AdventureGameMac.app/Contents/MacOS/AdventureGame by ExampleStudio Studios",
			},
		},
	}

	log := util.NewLogger("error")
	server := NewServerForTesting(log)
	server.RegisterGameManagementTools(gamesConfig, 5*time.Millisecond, 10*time.Millisecond)

	return server, listener.Addr().(*net.TCPAddr).Port, bridgeToken, serverDone
}

func newAsyncMirroringDiscoveryTestServer(t *testing.T) (*Server, int, string, <-chan error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	bridgeToken := "async-mirroring-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionExpectListForDiscovery(listener, bridgeToken, serverDone)

	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{
			"adventure": {
				ID:         "adventure",
				Name:       "AdventureGame",
				LaunchMode: "DirectPath",
				Target:     "/Applications/AdventureGameMac.app/Contents/MacOS/AdventureGame by ExampleStudio Studios",
			},
		},
	}

	log := util.NewLogger("error")
	server := NewServerForTesting(log)
	server.RegisterGameManagementTools(gamesConfig, 5*time.Millisecond, 10*time.Millisecond)

	return server, listener.Addr().(*net.TCPAddr).Port, bridgeToken, serverDone
}

func assertPongToolResult(t *testing.T, callText string) {
	t.Helper()
	if strings.Contains(callText, `"isError":true`) {
		t.Fatalf("expected direct GABP fallback to succeed, got: %s", callText)
	}
	if !strings.Contains(callText, "pong") {
		t.Fatalf("expected ping result, got: %s", callText)
	}
}

func assertAsyncMirroringServerDone(t *testing.T, serverDone <-chan error) {
	t.Helper()

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("test GABP server failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for test GABP server")
	}
}

func serveTestGabpSessionExpectToolCallBeforeList(listener net.Listener, expectedToken string, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()

	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)

	for {
		data, err := reader.ReadMessage()
		if err != nil {
			done <- err
			return
		}

		var request util.GABPMessage
		if err := json.Unmarshal(data, &request); err != nil {
			done <- err
			return
		}

		switch request.Method {
		case "session/hello":
			params, ok := request.Params.(map[string]interface{})
			if !ok {
				done <- fmt.Errorf("session/hello params not decoded as object: %#v", request.Params)
				return
			}
			if token, _ := params["token"].(string); token != expectedToken {
				done <- fmt.Errorf("unexpected handshake token: %q", token)
				return
			}

			response := util.NewGABPResponse(request.ID, gabp.SessionWelcomeResult{
				AgentID: "adventure",
				App: gabp.AppInfo{
					Name:    "ExampleGameBridge",
					Version: "0.1.0",
				},
				Capabilities: gabp.Capabilities{
					Methods:   []string{"tools/list", "tools/call"},
					Events:    []string{"system/log"},
					Resources: []string{},
				},
				SchemaVersion: "1.0",
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case "tools/list":
			done <- fmt.Errorf("tools/list arrived before first tool call")
			return
		case "tools/call":
			requestParams, ok := request.Params.(map[string]interface{})
			if !ok {
				done <- fmt.Errorf("tools/call params not decoded as object: %#v", request.Params)
				return
			}

			switch name, _ := requestParams["name"].(string); name {
			case "adventure/corebridge/core/ping":
				if err := writer.WriteJSON(util.NewGABPError(request.ID, 404, "tool not found: "+name, nil)); err != nil {
					done <- err
					return
				}
			case "corebridge/core/ping":
				response := util.NewGABPResponse(request.ID, map[string]interface{}{
					"text":    "pong",
					"message": "pong",
				})
				if err := writer.WriteJSON(response); err != nil {
					done <- err
					return
				}
				done <- nil
				return
			default:
				if err := writer.WriteJSON(util.NewGABPError(request.ID, 404, "tool not found: "+name, nil)); err != nil {
					done <- err
					return
				}
			}
		default:
			done <- fmt.Errorf("unexpected method: %s", request.Method)
			return
		}
	}
}

func serveTestGabpSessionExpectListBeforeSafeToolCall(listener net.Listener, expectedToken string, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()

	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)
	listed := false

	for {
		data, err := reader.ReadMessage()
		if err != nil {
			done <- err
			return
		}

		var request util.GABPMessage
		if err := json.Unmarshal(data, &request); err != nil {
			done <- err
			return
		}

		switch request.Method {
		case "session/hello":
			params, ok := request.Params.(map[string]interface{})
			if !ok {
				done <- fmt.Errorf("session/hello params not decoded as object: %#v", request.Params)
				return
			}
			if token, _ := params["token"].(string); token != expectedToken {
				done <- fmt.Errorf("unexpected handshake token: %q", token)
				return
			}

			response := util.NewGABPResponse(request.ID, gabp.SessionWelcomeResult{
				AgentID: "adventure",
				App: gabp.AppInfo{
					Name:    "ExampleGameBridge",
					Version: "0.1.0",
				},
				Capabilities: gabp.Capabilities{
					Methods:   []string{"tools/list", "tools/call"},
					Events:    []string{"system/log"},
					Resources: []string{},
				},
				SchemaVersion: "1.0",
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case "tools/list":
			listed = true
			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"tools": []map[string]interface{}{
					{
						"name":        "corebridge/core/ping",
						"description": "Ping bridge",
						"inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
					},
				},
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case "tools/call":
			if !listed {
				done <- fmt.Errorf("tools/call arrived before tools/list for strict-safe alias")
				return
			}
			requestParams, ok := request.Params.(map[string]interface{})
			if !ok {
				done <- fmt.Errorf("tools/call params not decoded as object: %#v", request.Params)
				return
			}

			if name, _ := requestParams["name"].(string); name != "corebridge/core/ping" {
				done <- fmt.Errorf("expected descriptor-resolved tool name, got %q", name)
				return
			}

			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"text":    "pong",
				"message": "pong",
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
			done <- nil
			return
		default:
			done <- fmt.Errorf("unexpected method: %s", request.Method)
			return
		}
	}
}

func serveTestGabpSessionExpectListForDiscovery(listener net.Listener, expectedToken string, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()

	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)

	for {
		data, err := reader.ReadMessage()
		if err != nil {
			done <- err
			return
		}

		var request util.GABPMessage
		if err := json.Unmarshal(data, &request); err != nil {
			done <- err
			return
		}

		switch request.Method {
		case "session/hello":
			params, ok := request.Params.(map[string]interface{})
			if !ok {
				done <- fmt.Errorf("session/hello params not decoded as object: %#v", request.Params)
				return
			}
			if token, _ := params["token"].(string); token != expectedToken {
				done <- fmt.Errorf("unexpected handshake token: %q", token)
				return
			}

			response := util.NewGABPResponse(request.ID, gabp.SessionWelcomeResult{
				AgentID: "adventure",
				App: gabp.AppInfo{
					Name:    "ExampleGameBridge",
					Version: "0.1.0",
				},
				Capabilities: gabp.Capabilities{
					Methods:   []string{"tools/list", "tools/call"},
					Events:    []string{"system/log"},
					Resources: []string{},
				},
				SchemaVersion: "1.0",
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case "tools/list":
			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"tools": []map[string]interface{}{
					{
						"name":        "corebridge/core/ping",
						"description": "Ping bridge",
						"inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
					},
				},
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
			done <- nil
			return
		default:
			done <- fmt.Errorf("unexpected method: %s", request.Method)
			return
		}
	}
}
