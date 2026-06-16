package mcp

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func TestGamesConnectTakesOverIdleRuntimeOwner(t *testing.T) {
	tmpDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeToken := "idle-takeover-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSession(listener, bridgeToken, serverDone)

	writeBridgeJSONForTest(t, tmpDir, "adventure", listener.Addr().(*net.TCPAddr).Port, bridgeToken)
	gamesConfig := roamingGamesConfig()
	log := util.NewLogger("error")

	ownerServer := NewServerForTesting(log)
	ownerServer.SetConfigDir(tmpDir)
	ownerServer.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	joinerServer := NewServerForTesting(log)
	joinerServer.SetConfigDir(tmpDir)
	joinerServer.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	expiredLease := time.Now().UTC().Add(-time.Second)
	staleOwner := process.RuntimeState{
		GameID:          "adventure",
		Status:          process.RuntimeStateStatusRunning,
		OwnerPID:        os.Getpid(),
		OwnerInstanceID: ownerServer.instanceID,
		OwnerLastActive: expiredLease.Add(-time.Second),
		OwnerLeaseUntil: expiredLease,
		GamePID:         os.Getpid(),
	}
	if err := process.ClaimRuntimeState("adventure", tmpDir, staleOwner); err != nil {
		t.Fatalf("failed to seed runtime state: %v", err)
	}

	connectText := marshalMessage(t, joinerServer.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-idle-owner"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "adventure",
			},
		},
	}))

	if strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected idle takeover connect to succeed, got: %s", connectText)
	}
	if !strings.Contains(connectText, "Took ownership of idle GABS session") {
		t.Fatalf("expected idle takeover message, got: %s", connectText)
	}

	runtimeState, err := process.LoadRuntimeState("adventure", tmpDir)
	if err != nil {
		t.Fatalf("failed to load runtime state: %v", err)
	}
	if runtimeState == nil {
		t.Fatal("expected runtime state after idle takeover")
	}
	if runtimeState.OwnerInstanceID != joinerServer.instanceID {
		t.Fatalf("expected joiner to own runtime state, got %q", runtimeState.OwnerInstanceID)
	}
	if !process.RuntimeOwnerLeaseActive(runtimeState, joinerServer.runtimeOwnerLeaseDuration(), time.Now().UTC()) {
		t.Fatalf("expected joiner lease to be active: %#v", runtimeState)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func TestGameBoundCallBlocksOldOwnerAfterRoamingTakeover(t *testing.T) {
	tmpDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeToken := "old-owner-block-token"
	serverDone := make(chan error, 1)
	go serveTestGabpSession(listener, bridgeToken, serverDone)

	writeBridgeJSONForTest(t, tmpDir, "adventure", listener.Addr().(*net.TCPAddr).Port, bridgeToken)
	gamesConfig := roamingGamesConfig()
	log := util.NewLogger("error")

	ownerServer := NewServerForTesting(log)
	ownerServer.SetConfigDir(tmpDir)
	ownerServer.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	connectText := marshalMessage(t, ownerServer.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-owner"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "adventure",
			},
		},
	}))
	if strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected owner connect to succeed, got: %s", connectText)
	}

	newOwner := process.RuntimeState{
		GameID:          "adventure",
		Status:          process.RuntimeStateStatusRunning,
		OwnerPID:        os.Getpid(),
		OwnerInstanceID: "new-owner-instance",
		OwnerLastActive: time.Now().UTC(),
		OwnerLeaseUntil: time.Now().UTC().Add(time.Minute),
		GamePID:         os.Getpid(),
	}
	if err := process.SaveRuntimeState("adventure", tmpDir, newOwner); err != nil {
		t.Fatalf("failed to move runtime ownership: %v", err)
	}

	callText := marshalMessage(t, ownerServer.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"old-owner-call"`),
		Params: map[string]interface{}{
			"name": "games.call_tool",
			"arguments": map[string]interface{}{
				"gameId": "adventure",
				"tool":   "adventure.corebridge.core.ping",
			},
		},
	}))

	if !strings.Contains(callText, `"isError":true`) {
		t.Fatalf("expected old owner call to be blocked, got: %s", callText)
	}
	if !strings.Contains(callText, "blocked_by_active_runtime_owner") {
		t.Fatalf("expected runtime owner block, got: %s", callText)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("test GABP server failed: %v", err)
	}
}

func TestGamesConnectClearsRuntimeOwnerAfterFailedDial(t *testing.T) {
	tmpDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("failed to close listener: %v", err)
	}

	writeBridgeJSONForTest(t, tmpDir, "adventure", port, "failed-dial-token")
	gamesConfig := roamingGamesConfig()
	log := util.NewLogger("error")

	server := NewServerForTesting(log)
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	connectText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-failed-dial"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId": "adventure",
			},
		},
	}))

	if !strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected connect to fail, got: %s", connectText)
	}

	runtimeState, err := process.LoadRuntimeState("adventure", tmpDir)
	if err != nil {
		t.Fatalf("failed to load runtime state: %v", err)
	}
	if runtimeState != nil {
		t.Fatalf("expected failed connect to clear runtime owner claim, got: %#v", runtimeState)
	}
}

func roamingGamesConfig() *config.GamesConfig {
	return &config.GamesConfig{
		Games: map[string]config.GameConfig{
			"adventure": {
				ID:         "adventure",
				Name:       "AdventureGame",
				LaunchMode: "DirectPath",
				Target:     "/Applications/AdventureGameMac.app/Contents/MacOS/AdventureGame by ExampleStudio Studios",
			},
		},
	}
}

func writeBridgeJSONForTest(t *testing.T, configDir, gameID string, port int, token string) {
	t.Helper()

	bridgeDir := filepath.Join(configDir, gameID)
	if err := os.MkdirAll(bridgeDir, 0755); err != nil {
		t.Fatalf("failed to create bridge dir: %v", err)
	}
	bridgeData, err := json.MarshalIndent(config.BridgeJSON{
		Port:   port,
		Token:  token,
		GameId: gameID,
	}, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal bridge.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bridgeDir, "bridge.json"), bridgeData, 0644); err != nil {
		t.Fatalf("failed to write bridge.json: %v", err)
	}
}
