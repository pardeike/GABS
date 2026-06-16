package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func TestGamesStartCapsSynchronousGABPWaitForLargeTimeout(t *testing.T) {
	restoreWait := withMaxSynchronousStartupGABPWait(t, 75*time.Millisecond)
	defer restoreWait()

	tmpDir := t.TempDir()
	game := config.GameConfig{
		ID:         "slow-bridge",
		Name:       "Slow Bridge",
		LaunchMode: "DirectPath",
		Target:     "/bin/sleep",
		Args:       []string{"30"},
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	startedAt := time.Now()
	startText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-slow-bridge"`),
		Params: map[string]interface{}{
			"name": "games.start",
			"arguments": map[string]interface{}{
				"gameId":  game.ID,
				"timeout": 240,
			},
		},
	}))
	elapsed := time.Since(startedAt)
	t.Cleanup(func() {
		_ = server.stopGame(game, true)
	})

	if elapsed > 2*time.Second {
		t.Fatalf("games_start should return before client-level MCP timeouts; took %v: %s", elapsed, startText)
	}
	if !strings.Contains(startText, "GABP was not ready") {
		t.Fatalf("expected bounded-start response, got: %s", startText)
	}
	if strings.Contains(startText, `"isError":true`) {
		t.Fatalf("start should not be a tool error just because GABP is still loading: %s", startText)
	}
}

func TestGamesStartUsesLauncherReusedBridgeEnvironmentWithoutMutatingBridgeFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process environment inspection is not supported on Windows")
	}

	restoreWait := withMaxSynchronousStartupGABPWait(t, 75*time.Millisecond)
	defer restoreWait()

	tmpDir := t.TempDir()
	game := config.GameConfig{
		ID:              "steam-stale-env",
		Name:            "Steam Stale Env",
		LaunchMode:      "SteamAppId",
		Target:          "123456",
		StopProcessName: "ExampleGameBridge",
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}

	freshPort := unusedLocalPort(t)
	stalePort := unusedLocalPort(t)
	if stalePort == freshPort {
		stalePort = unusedLocalPort(t)
	}
	freshToken := "fresh-token"
	staleToken := "stale-token"
	bridgePath, err := config.WriteBridgeJSONWithEndpoint(game.ID, tmpDir, freshPort, freshToken)
	if err != nil {
		t.Fatalf("failed to seed fresh bridge endpoint: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to locate test executable: %v", err)
	}
	pidPath := filepath.Join(tmpDir, "game.pid")
	scriptPath := filepath.Join(tmpDir, "launch-stale-env.sh")
	script := fmt.Sprintf(`#!/bin/sh
GABP_SERVER_PORT=%d GABP_TOKEN=%s GABS_GAME_ID=%s GABS_BRIDGE_PATH=%s GABS_HELPER_PROCESS=1 %s -test.run=TestSharedRuntimeStateHelperProcess &
echo $! > %s
wait
`, stalePort, staleToken, game.ID, shellQuote(bridgePath), shellQuote(exe), shellQuote(pidPath))
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write launcher script: %v", err)
	}

	restoreLauncher := process.SetLaunchCommandFactoriesForTesting(
		func(target string) (string, []string) {
			return scriptPath, nil
		},
		nil,
	)
	defer restoreLauncher()

	restoreFinder := process.SetFindProcessesByNameForTesting(func(name string) ([]int, error) {
		if name != game.StopProcessName {
			return nil, nil
		}
		data, err := os.ReadFile(pidPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, err
		}
		if !process.IsProcessAlive(pid) {
			return nil, nil
		}
		return []int{pid}, nil
	})
	defer restoreFinder()

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	startText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-stale-launcher"`),
		Params: map[string]interface{}{
			"name": "games.start",
			"arguments": map[string]interface{}{
				"gameId":  game.ID,
				"timeout": 1,
			},
		},
	}))
	t.Cleanup(func() {
		_ = server.stopGame(game, true)
	})

	if strings.Contains(startText, `"isError":true`) {
		t.Fatalf("start should adopt stale launcher env and return a bounded non-error result, got: %s", startText)
	}

	_, adoptedPort, adoptedToken, err := config.ReadBridgeJSON(game.ID, tmpDir)
	if err != nil {
		t.Fatalf("failed to read internal bridge endpoint: %v", err)
	}
	if adoptedPort != freshPort || adoptedToken != freshToken {
		statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("status-stale-launcher", "games.status", game.ID)))
		t.Fatalf("expected internal bridge endpoint to remain debug-only %d/%s, got %d/%s; status: %s", freshPort, freshToken, adoptedPort, adoptedToken, statusText)
	}
}

func TestGamesStartRejectsOccupiedEndpointCache(t *testing.T) {
	tmpDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve endpoint port: %v", err)
	}
	defer listener.Close()

	game := config.GameConfig{
		ID:         "occupied-cache",
		Name:       "Occupied Cache",
		LaunchMode: "DirectPath",
		Target:     "/bin/sleep",
		Args:       []string{"30"},
	}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{game.ID: game},
	}
	if _, err := config.WriteBridgeJSONWithEndpoint(game.ID, tmpDir, listener.Addr().(*net.TCPAddr).Port, "existing-token"); err != nil {
		t.Fatalf("failed to seed endpoint cache: %v", err)
	}

	server := NewServerForTesting(util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	startText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-occupied-cache"`),
		Params: map[string]interface{}{
			"name": "games.start",
			"arguments": map[string]interface{}{
				"gameId": game.ID,
			},
		},
	}))

	if !strings.Contains(startText, `"isError":true`) {
		t.Fatalf("expected occupied endpoint cache to be a tool error, got: %s", startText)
	}
	if !strings.Contains(startText, `"status":"endpoint_cache_in_use"`) {
		t.Fatalf("expected endpoint_cache_in_use status, got: %s", startText)
	}
	if !strings.Contains(startText, `"resetEndpoint":true`) || !strings.Contains(startText, "games_connect") {
		t.Fatalf("expected connect and reset next actions, got: %s", startText)
	}
	if runtimeState, err := process.LoadRuntimeState(game.ID, tmpDir); err != nil {
		t.Fatalf("failed to inspect runtime state: %v", err)
	} else if runtimeState != nil {
		t.Fatalf("runtime state should be cleaned up after endpoint-cache failure, got %#v", runtimeState)
	}
}

func withMaxSynchronousStartupGABPWait(t *testing.T, value time.Duration) func() {
	t.Helper()

	previous := maxSynchronousStartupGABPWait
	maxSynchronousStartupGABPWait = value
	return func() {
		maxSynchronousStartupGABPWait = previous
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
