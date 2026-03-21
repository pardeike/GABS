package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/util"
)

func TestGamesCallToolHonorsOuterTimeout(t *testing.T) {
	server, serverDone := newGamesCallToolTimeoutTestServer(t, 2*time.Second)

	connectText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-timeout"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}))
	if strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected connect to succeed, got: %s", connectText)
	}

	callStart := time.Now()
	callText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"call-timeout"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"tool":    "rimworld.rimworld.load_game_ready",
				"timeout": 1,
			},
		},
	}))
	callDuration := time.Since(callStart)

	if callDuration > 1500*time.Millisecond {
		t.Fatalf("expected games.call_tool to time out around 1s, took %v (%s)", callDuration, callText)
	}
	if !strings.Contains(callText, `"isError":true`) {
		t.Fatalf("expected timeout error, got: %s", callText)
	}
	if !strings.Contains(callText, "request timeout after 1s") {
		t.Fatalf("expected timeout details in response, got: %s", callText)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func TestGamesCallToolUsesToolTimeoutHintsFromArguments(t *testing.T) {
	server, serverDone := newGamesCallToolTimeoutTestServer(t, 2*time.Second)

	connectText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-timeout-hint"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}))
	if strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected connect to succeed, got: %s", connectText)
	}

	callStart := time.Now()
	callText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"call-timeout-hint"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"tool": "rimworld.rimworld.load_game_ready",
				"arguments": map[string]interface{}{
					"timeoutMs":         2500,
					"waitForScreenFade": true,
				},
				"timeout": 1,
			},
		},
	}))
	callDuration := time.Since(callStart)

	if strings.Contains(callText, `"isError":true`) {
		t.Fatalf("expected timeout hint to extend proxy timeout, got: %s", callText)
	}
	if !strings.Contains(callText, "loaded") {
		t.Fatalf("expected delayed tool response, got: %s", callText)
	}
	if callDuration < 2*time.Second || callDuration > 4*time.Second {
		t.Fatalf("expected delayed tool response after about 2s, took %v (%s)", callDuration, callText)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func newGamesCallToolTimeoutTestServer(t *testing.T, toolDelay time.Duration) (*Server, <-chan error) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "gabs-games-call-tool-timeout")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	bridgeToken := "games-call-tool-timeout-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionWithDelayedToolResponse(listener, bridgeToken, toolDelay, serverDone)

	bridgeDir := filepath.Join(tmpDir, "rimworld")
	if err := os.MkdirAll(bridgeDir, 0755); err != nil {
		t.Fatalf("failed to create bridge dir: %v", err)
	}

	bridgeData, err := json.MarshalIndent(config.BridgeJSON{
		Port:   listener.Addr().(*net.TCPAddr).Port,
		Token:  bridgeToken,
		GameId: "rimworld",
	}, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal bridge.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bridgeDir, "bridge.json"), bridgeData, 0644); err != nil {
		t.Fatalf("failed to write bridge.json: %v", err)
	}

	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{
			"rimworld": {
				ID:         "rimworld",
				Name:       "RimWorld",
				LaunchMode: "DirectPath",
				Target:     "/Applications/RimWorldMac.app/Contents/MacOS/RimWorld by Ludeon Studios",
			},
		},
	}

	log := util.NewLogger("error")
	server := NewServerForTesting(log)
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 1*time.Second)
	return server, serverDone
}

func serveTestGabpSessionWithDelayedToolResponse(listener net.Listener, expectedToken string, toolDelay time.Duration, done chan<- error) {
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
				AgentID: "rimworld",
				App: gabp.AppInfo{
					Name:    "RimBridgeServer",
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
						"name":        "rimworld/load_game_ready",
						"description": "Wait until the RimWorld session is ready for automation.",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"timeoutMs": map[string]interface{}{
									"type": "integer",
								},
								"waitForScreenFade": map[string]interface{}{
									"type": "boolean",
								},
							},
						},
						"outputSchema": map[string]interface{}{
							"type": "object",
						},
					},
				},
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case "tools/call":
			requestParams, ok := request.Params.(map[string]interface{})
			if !ok {
				done <- fmt.Errorf("tools/call params not decoded as object: %#v", request.Params)
				return
			}
			if name, _ := requestParams["name"].(string); name != "rimworld/load_game_ready" {
				done <- fmt.Errorf("unexpected tools/call target: %q", name)
				return
			}

			time.Sleep(toolDelay)

			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"text":   "loaded",
				"status": "loaded",
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
