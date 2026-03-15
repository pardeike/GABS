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

func TestGamesConnectCanReattachUsingBridgeConfigWithoutTrackedProcess(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-reconnect")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeToken := "reconnect-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSession(listener, bridgeToken, serverDone)

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

	connectMsg := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"reconnect-rimworld"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}

	connectResp := server.HandleMessage(connectMsg)
	connectBytes, _ := json.Marshal(connectResp)
	connectText := string(connectBytes)

	if strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected reconnect to succeed, got: %s", connectText)
	}
	if !strings.Contains(connectText, "Successfully connected to 'rimworld'") {
		t.Fatalf("unexpected reconnect response: %s", connectText)
	}

	statusMsg := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"status-rimworld"`),
		Params: map[string]interface{}{
			"name": "games.status",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}

	statusResp := server.HandleMessage(statusMsg)
	statusBytes, _ := json.Marshal(statusResp)
	statusText := string(statusBytes)
	if !strings.Contains(statusText, "connected via GABP") {
		t.Fatalf("expected connected status after reattach, got: %s", statusText)
	}

	toolsMsg := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"tools-rimworld"`),
		Params: map[string]interface{}{
			"name": "games.tools",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}

	toolsResp := server.HandleMessage(toolsMsg)
	toolsBytes, _ := json.Marshal(toolsResp)
	toolsText := string(toolsBytes)
	if !strings.Contains(toolsText, "rimworld.rimbridge.core.ping") {
		t.Fatalf("expected mirrored tool after reconnect, got: %s", toolsText)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func serveTestGabpSession(listener net.Listener, expectedToken string, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()

	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)

	for i := 0; i < 2; i++ {
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
				AgentId: "rimworld",
				App: &gabp.AppInfo{
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
						"name":        "rimbridge.core/ping",
						"description": "Connectivity test",
						"inputSchema": map[string]interface{}{
							"type":       "object",
							"properties": map[string]interface{}{},
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
		default:
			done <- fmt.Errorf("unexpected method: %s", request.Method)
			return
		}
	}

	done <- nil
}
