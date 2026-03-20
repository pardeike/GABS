package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/process"
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

func TestGamesCallToolFailsFastAndStatusTurnsDisconnectedAfterBridgeDrop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-reconnect-disconnect")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeToken := "disconnect-on-call-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionDisconnectOnToolCall(listener, bridgeToken, serverDone)

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

	connectResp := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-disconnect"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	})
	connectText := marshalMessage(t, connectResp)
	if strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected connect to succeed, got: %s", connectText)
	}

	callStart := time.Now()
	callResp := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"call-disconnect"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"tool":    "rimworld.rimbridge.core.ping",
				"timeout": 120,
			},
		},
	})
	callDuration := time.Since(callStart)
	callText := marshalMessage(t, callResp)
	if callDuration > 2*time.Second {
		t.Fatalf("expected call_tool to fail fast after disconnect, took %v (%s)", callDuration, callText)
	}
	if !strings.Contains(callText, `"isError":true`) {
		t.Fatalf("expected call_tool to fail after disconnect, got: %s", callText)
	}
	if !strings.Contains(callText, "connection unavailable") {
		t.Fatalf("expected disconnect details in call_tool response, got: %s", callText)
	}

	statusResp := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"status-disconnect"`),
		Params: map[string]interface{}{
			"name": "games.status",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	})
	statusText := marshalMessage(t, statusResp)
	if !strings.Contains(statusText, "GABP disconnected") {
		t.Fatalf("expected disconnected status after bridge drop, got: %s", statusText)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func TestGamesCallToolCanInferGameFromQualifiedName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-reconnect-call-tool")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeToken := "reconnect-call-tool-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionWithToolCalls(listener, bridgeToken, serverDone)

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
		ID:      json.RawMessage(`"reconnect-rimworld-call-tool"`),
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

	callMsg := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"call-rimworld-qualified-tool"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"tool": "rimworld.rimbridge.core.ping",
			},
		},
	}

	callResp := server.HandleMessage(callMsg)
	callBytes, _ := json.Marshal(callResp)
	callText := string(callBytes)
	if strings.Contains(callText, `"isError":true`) {
		t.Fatalf("expected call_tool without gameId to succeed, got: %s", callText)
	}
	if !strings.Contains(callText, "pong") {
		t.Fatalf("expected call_tool without gameId to return pong, got: %s", callText)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func TestGamesConnectForceTakeoverCanOverrideSharedOwner(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-reconnect-force-takeover")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeToken := "force-takeover-token"
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
	ownerServer := NewServerForTesting(log)
	ownerServer.SetConfigDir(tmpDir)
	ownerServer.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 1*time.Second)

	joinerServer := NewServerForTesting(log)
	joinerServer.SetConfigDir(tmpDir)
	joinerServer.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, 1*time.Second)

	staleOwner := process.RuntimeState{
		GameID:          "rimworld",
		Status:          process.RuntimeStateStatusRunning,
		OwnerPID:        os.Getpid(),
		OwnerInstanceID: ownerServer.instanceID,
		GamePID:         os.Getpid(),
		StopProcessName: "",
		UpdatedAt:       time.Now().UTC(),
	}
	if err := process.ClaimRuntimeState("rimworld", tmpDir, staleOwner); err != nil {
		t.Fatalf("failed to write runtime state: %v", err)
	}

	connectBlocked := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"reconnect-rimworld-no-force"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}

	blockedResp := joinerServer.HandleMessage(connectBlocked)
	blockedBytes, _ := json.Marshal(blockedResp)
	blockedText := string(blockedBytes)
	if strings.Contains(blockedText, `"isError":true`) {
		t.Fatalf("expected non-error response for blocked connect, got: %s", blockedText)
	}
	if !strings.Contains(blockedText, "another live GABS session") {
		t.Fatalf("expected ownership block message, got: %s", blockedText)
	}

	connectForced := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"reconnect-rimworld-force"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId":        "rimworld",
				"forceTakeover": true,
			},
		},
	}

	forcedResp := joinerServer.HandleMessage(connectForced)
	forcedBytes, _ := json.Marshal(forcedResp)
	forcedText := string(forcedBytes)
	if strings.Contains(forcedText, `"isError":true`) {
		t.Fatalf("expected forced reconnect to succeed, got: %s", forcedText)
	}
	if !strings.Contains(forcedText, "Force-took ownership") {
		t.Fatalf("expected force takeover success message, got: %s", forcedText)
	}

	runtimeState, err := process.LoadRuntimeState("rimworld", tmpDir)
	if err != nil {
		t.Fatalf("failed to load runtime state: %v", err)
	}
	if runtimeState == nil {
		t.Fatal("expected runtime state after forced reconnect")
	}
	if runtimeState.OwnerInstanceID != joinerServer.instanceID {
		t.Fatalf("expected runtime state owner instance to change, got %q", runtimeState.OwnerInstanceID)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func TestGamesCallToolBlocksUntilAttentionIsAcknowledged(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-attention")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	var forwardedToolCalls int32
	bridgeToken := "attention-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionWithAttention(listener, bridgeToken, &forwardedToolCalls, serverDone)

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
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)
	defer server.CleanupGABPConnection("rimworld")

	connectText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-attention"`),
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

	getAttentionText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"get-attention"`),
		Params: map[string]interface{}{
			"name": "games.get_attention",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
			},
		},
	}))
	if !strings.Contains(getAttentionText, `"supported":true`) || !strings.Contains(getAttentionText, `"attentionId":"attn_42"`) {
		t.Fatalf("expected open attention item, got: %s", getAttentionText)
	}

	blockedText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"call-blocked"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
				"tool":   "rimworld.rimbridge.core.ping",
			},
		},
	}))
	if !strings.Contains(blockedText, `"status":"blocked_by_attention"`) {
		t.Fatalf("expected blocked_by_attention result, got: %s", blockedText)
	}
	if got := atomic.LoadInt32(&forwardedToolCalls); got != 0 {
		t.Fatalf("expected no forwarded tool calls while attention is open, got %d", got)
	}

	ackText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"ack-attention"`),
		Params: map[string]interface{}{
			"name": "games.ack_attention",
			"arguments": map[string]interface{}{
				"gameId":      "rimworld",
				"attentionId": "attn_42",
			},
		},
	}))
	if !strings.Contains(ackText, `"acknowledged":true`) {
		t.Fatalf("expected acknowledgement to succeed, got: %s", ackText)
	}

	callAfterAckText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"call-after-ack"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"gameId": "rimworld",
				"tool":   "rimworld.rimbridge.core.ping",
			},
		},
	}))
	if strings.Contains(callAfterAckText, `"isError":true`) || !strings.Contains(callAfterAckText, "pong") {
		t.Fatalf("expected call to succeed after ack, got: %s", callAfterAckText)
	}

	if got := atomic.LoadInt32(&forwardedToolCalls); got != 1 {
		t.Fatalf("expected exactly one forwarded tool call after acknowledgement, got %d", got)
	}

	server.CleanupGABPConnection("rimworld")
	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func TestMirroredToolCallBlocksWhileAttentionIsOpen(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-attention-mirrored")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	var forwardedToolCalls int32
	bridgeToken := "attention-mirrored-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSessionWithAttention(listener, bridgeToken, &forwardedToolCalls, serverDone)

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
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)
	defer server.CleanupGABPConnection("rimworld")

	connectText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-mirrored-attention"`),
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

	mirroredBlockedText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"mirrored-blocked"`),
		Params: map[string]interface{}{
			"name":      "rimworld.rimbridge.core.ping",
			"arguments": map[string]interface{}{},
		},
	}))
	if !strings.Contains(mirroredBlockedText, `"status":"blocked_by_attention"`) {
		t.Fatalf("expected mirrored tool call to be blocked by attention, got: %s", mirroredBlockedText)
	}
	if got := atomic.LoadInt32(&forwardedToolCalls); got != 0 {
		t.Fatalf("expected mirrored tool call to be blocked before forwarding, got %d forwarded calls", got)
	}

	server.CleanupGABPConnection("rimworld")
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

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		done <- err
		return
	}
	if _, err := reader.ReadMessage(); err != nil {
		var netErr net.Error
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || (errors.As(err, &netErr) && netErr.Timeout()) {
			done <- nil
			return
		}
		done <- err
		return
	}

	done <- nil
}

func serveTestGabpSessionWithToolCalls(listener net.Listener, expectedToken string, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()

	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)

	for i := 0; i < 3; i++ {
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
		case "tools/call":
			if request.Method != "tools/call" {
				done <- fmt.Errorf("unexpected method: %s", request.Method)
				return
			}
			if requestParams, ok := request.Params.(map[string]interface{}); ok {
				if name, _ := requestParams["name"].(string); name != "rimbridge/core/ping" {
					done <- fmt.Errorf("unexpected tools/call target: %q", name)
					return
				}
			}
			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"text":    "pong",
				"message": "pong",
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

func serveTestGabpSessionDisconnectOnToolCall(listener net.Listener, expectedToken string, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()

	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)

	for i := 0; i < 3; i++ {
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
		case "tools/call":
			done <- nil
			return
		default:
			done <- fmt.Errorf("unexpected method: %s", request.Method)
			return
		}
	}

	done <- nil
}

func serveTestGabpSessionWithAttention(listener net.Listener, expectedToken string, forwardedToolCalls *int32, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()

	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)

	attentionOpen := true
	attentionItem := map[string]interface{}{
		"attentionId":        "attn_42",
		"state":              "open",
		"severity":           "error",
		"blocking":           true,
		"stateInvalidated":   true,
		"summary":            "Game state may have changed after the last operation.",
		"openedAtSequence":   1201,
		"latestSequence":     1237,
		"totalUrgentEntries": 3,
		"sample": []map[string]interface{}{
			{
				"level":          "error",
				"message":        "Test attention error",
				"repeatCount":    1,
				"latestSequence": 1237,
			},
		},
	}

	for {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			done <- err
			return
		}

		data, err := reader.ReadMessage()
		if err != nil {
			var netErr net.Error
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || (errors.As(err, &netErr) && netErr.Timeout()) {
				done <- nil
				return
			}
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
					Methods: []string{
						"tools/list",
						"tools/call",
						gabp.AttentionCurrentMethod,
						gabp.AttentionAckMethod,
					},
					Events: []string{
						gabp.AttentionOpenedChannel,
						gabp.AttentionUpdatedChannel,
						gabp.AttentionClearedChannel,
					},
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
		case "events/subscribe":
			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"channels": []string{
					gabp.AttentionOpenedChannel,
					gabp.AttentionUpdatedChannel,
					gabp.AttentionClearedChannel,
				},
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case gabp.AttentionCurrentMethod:
			var current interface{}
			if attentionOpen {
				current = attentionItem
			}
			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"attention": current,
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case gabp.AttentionAckMethod:
			params, ok := request.Params.(map[string]interface{})
			if !ok {
				done <- fmt.Errorf("attention/ack params not decoded as object: %#v", request.Params)
				return
			}
			attentionID, _ := params["attentionId"].(string)
			acknowledged := attentionOpen && attentionID == "attn_42"
			if acknowledged {
				attentionOpen = false
			}

			var current interface{}
			if attentionOpen {
				current = attentionItem
			}

			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"acknowledged":     acknowledged,
				"attentionId":      attentionID,
				"currentAttention": current,
			})
			if err := writer.WriteJSON(response); err != nil {
				done <- err
				return
			}
		case "tools/call":
			if requestParams, ok := request.Params.(map[string]interface{}); ok {
				if name, _ := requestParams["name"].(string); name != "rimbridge/core/ping" {
					done <- fmt.Errorf("unexpected tools/call target: %q", name)
					return
				}
			}
			atomic.AddInt32(forwardedToolCalls, 1)
			response := util.NewGABPResponse(request.ID, map[string]interface{}{
				"text":    "pong",
				"message": "pong",
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
}
