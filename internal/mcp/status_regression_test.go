package mcp

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func TestGamesStatusStopsReportingRunningAfterProcessExits(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-status-regression")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{
			"sleepy": {
				ID:         "sleepy",
				Name:       "Sleepy",
				LaunchMode: "DirectPath",
				Target:     "/bin/sleep",
				Args:       []string{"3"},
			},
		},
	}

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	startResp := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-sleepy"`),
		Params: map[string]interface{}{
			"name": "games.start",
			"arguments": map[string]interface{}{
				"gameId": "sleepy",
			},
		},
	})
	startText := marshalMessage(t, startResp)
	if strings.Contains(startText, `"isError":true`) {
		t.Fatalf("expected start to succeed, got: %s", startText)
	}

	time.Sleep(4 * time.Second)

	statusResp := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"status-sleepy"`),
		Params: map[string]interface{}{
			"name": "games.status",
			"arguments": map[string]interface{}{
				"gameId": "sleepy",
			},
		},
	})
	statusText := marshalMessage(t, statusResp)
	if !strings.Contains(statusText, "stopped") {
		t.Fatalf("expected stopped status after process exit, got: %s", statusText)
	}
}

func TestGamesStatusReportsAndCleansStaleSharedRuntimeState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-status-stale-runtime")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	game := config.GameConfig{
		ID:         "stale-runtime",
		Name:       "Stale Runtime",
		LaunchMode: "DirectPath",
		Target:     "/bin/sleep",
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}

	staleState := process.RuntimeState{
		GameID:   game.ID,
		Status:   process.RuntimeStateStatusRunning,
		OwnerPID: 999999,
		GamePID:  999999,
	}
	if err := process.SaveRuntimeState(game.ID, tmpDir, staleState); err != nil {
		t.Fatalf("failed to seed runtime state: %v", err)
	}
	bridgePath := writeBridgeFileForStatusTest(t, tmpDir, game.ID, 49152, "stale-runtime-token")

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("status-stale-runtime", "games.status", game.ID)))
	if !strings.Contains(statusText, "stale-runtime-cleaned") {
		t.Fatalf("expected stale runtime cleanup diagnosis, got: %s", statusText)
	}
	if strings.Contains(statusText, "durable bridge endpoint") || strings.Contains(statusText, "bridge.json") {
		t.Fatalf("status should not surface bridge-file recovery language, got: %s", statusText)
	}
	if state, err := process.LoadRuntimeState(game.ID, tmpDir); err != nil {
		t.Fatalf("failed to inspect runtime state: %v", err)
	} else if state != nil {
		t.Fatalf("expected stale runtime state to be removed, got %#v", state)
	}
	if _, err := os.Stat(bridgePath); err != nil {
		t.Fatalf("expected durable bridge file to remain, stat err: %v", err)
	}
}

func TestGamesStatusTreatsClosedBridgeFileAsIdleDurableEndpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-status-idle-bridge")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	game := config.GameConfig{
		ID:         "idle-bridge",
		Name:       "Idle Bridge",
		LaunchMode: "DirectPath",
		Target:     "/bin/sleep",
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}
	port := unusedLocalPort(t)
	writeBridgeFileForStatusTest(t, tmpDir, game.ID, port, "stale-bridge-token")
	withPassiveBridgeListenerStatus(t, func(checkedPort int) bridgeListenerDiagnostic {
		if checkedPort != port {
			t.Fatalf("expected listener check for port %d, got %d", port, checkedPort)
		}
		return bridgeListenerDiagnostic{Checked: true, Open: false, Method: "test"}
	})

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("status-idle-bridge", "games.status", game.ID)))
	if !strings.Contains(statusText, `"code":"healthy"`) {
		t.Fatalf("expected healthy idle bridge diagnosis, got: %s", statusText)
	}
	if strings.Contains(statusText, "stale-bridge-token") {
		t.Fatalf("raw bridge token leaked in status response: %s", statusText)
	}
}

func TestGamesStatusDoesNotConsumeAcceptOnceBridgeBeforeConnect(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-status-then-connect")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	game := config.GameConfig{
		ID:         "accept-once",
		Name:       "Accept Once",
		LaunchMode: "DirectPath",
		Target:     "/Applications/ExampleGameBridge.app/Contents/MacOS/ExampleGameBridge",
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}

	bridgeToken := "accept-once-token"
	writeBridgeFileForStatusTest(t, tmpDir, game.ID, listener.Addr().(*net.TCPAddr).Port, bridgeToken)
	serverDone := make(chan error, 1)
	go serveTestGabpSession(listener, bridgeToken, serverDone)

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("status-accept-once", "games.status", game.ID)))
	if strings.Contains(statusText, `"isError":true`) {
		t.Fatalf("expected status to succeed, got: %s", statusText)
	}

	connectResp := server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"connect-accept-once"`),
		Params: map[string]interface{}{
			"name": "games.connect",
			"arguments": map[string]interface{}{
				"gameId":  game.ID,
				"timeout": 2,
			},
		},
	})
	connectText := marshalMessage(t, connectResp)
	if strings.Contains(connectText, `"isError":true`) {
		t.Fatalf("expected connect after status to succeed, got: %s", connectText)
	}
	if !strings.Contains(connectText, "Successfully connected to 'accept-once'") {
		t.Fatalf("unexpected connect response: %s", connectText)
	}

	server.CleanupGABPConnection(game.ID)
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("accept-once bridge session failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for accept-once bridge session to finish")
	}
}

func TestGamesStatusTreatsUnverifiedStoppedBridgeFileAsIdleDurableEndpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gabs-status-unverified-bridge")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	game := config.GameConfig{
		ID:         "unverified-bridge",
		Name:       "Unverified Bridge",
		LaunchMode: "DirectPath",
		Target:     "/bin/sleep",
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}
	writeBridgeFileForStatusTest(t, tmpDir, game.ID, 49152, "unverified-bridge-token")
	withPassiveBridgeListenerStatus(t, func(checkedPort int) bridgeListenerDiagnostic {
		return bridgeListenerDiagnostic{Error: "passive listener inspection unavailable"}
	})

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("status-unverified-bridge", "games.status", game.ID)))
	if !strings.Contains(statusText, `"code":"healthy"`) {
		t.Fatalf("expected healthy idle bridge diagnosis, got: %s", statusText)
	}
	if strings.Contains(statusText, "unverified-bridge-token") {
		t.Fatalf("raw bridge token leaked in status response: %s", statusText)
	}
}

func TestGamesStatusIgnoresBridgeFileMismatchWhenProcessEnvironmentIsReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process environment inspection is not supported on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "gabs-status-launcher-env")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	game := config.GameConfig{
		ID:              "steam-env-game",
		Name:            "Steam Env Game",
		LaunchMode:      "SteamAppId",
		Target:          "123456",
		StopProcessName: "unused-test-process-name",
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}

	bridgePath := writeBridgeFileForStatusTest(t, tmpDir, game.ID, 49152, "bridge-token")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to locate test executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestSharedRuntimeStateHelperProcess")
	cmd.Env = append(os.Environ(),
		"GABS_HELPER_PROCESS=1",
		"GABP_SERVER_PORT=49153",
		"GABP_TOKEN=process-token",
		"GABS_GAME_ID="+game.ID,
		"GABS_BRIDGE_PATH="+bridgePath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start helper process: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	runtimeState := process.RuntimeState{
		GameID:   game.ID,
		Status:   process.RuntimeStateStatusRunning,
		OwnerPID: os.Getpid(),
		GamePID:  cmd.Process.Pid,
	}
	if err := process.SaveRuntimeState(game.ID, tmpDir, runtimeState); err != nil {
		t.Fatalf("failed to seed runtime state: %v", err)
	}

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("status-launcher-env", "games.status", game.ID)))
	if !strings.Contains(statusText, `"code":"healthy"`) {
		t.Fatalf("expected bridge-file mismatch to stay out of user-facing diagnostics, got: %s", statusText)
	}
	if strings.Contains(statusText, "bridge.json") || strings.Contains(statusText, "launcher likely reused stale environment") || strings.Contains(statusText, bridgePath) {
		t.Fatalf("status should not surface bridge-file mismatch details, got: %s", statusText)
	}
	if strings.Contains(statusText, "process-token") || strings.Contains(statusText, "bridge-token") {
		t.Fatalf("raw token leaked in status response: %s", statusText)
	}
}

func writeBridgeFileForStatusTest(t *testing.T, configDir, gameID string, port int, token string) string {
	t.Helper()

	bridgeDir := filepath.Join(configDir, gameID)
	if err := os.MkdirAll(bridgeDir, 0755); err != nil {
		t.Fatalf("failed to create bridge dir: %v", err)
	}
	bridgePath := filepath.Join(bridgeDir, "bridge.json")
	bridgeData, err := json.MarshalIndent(config.BridgeJSON{
		Port:   port,
		Token:  token,
		GameId: gameID,
	}, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal bridge config: %v", err)
	}
	if err := os.WriteFile(bridgePath, bridgeData, 0644); err != nil {
		t.Fatalf("failed to write bridge config: %v", err)
	}
	return bridgePath
}

func withPassiveBridgeListenerStatus(t *testing.T, fn func(port int) bridgeListenerDiagnostic) {
	t.Helper()

	original := passiveBridgeListenerStatusFunc
	passiveBridgeListenerStatusFunc = fn
	t.Cleanup(func() {
		passiveBridgeListenerStatusFunc = original
	})
}

func unusedLocalPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve local port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("failed to release local port: %v", err)
	}
	return port
}
