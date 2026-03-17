package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func TestSharedRuntimeStateHelperProcess(t *testing.T) {
	if os.Getenv("GABS_HELPER_PROCESS") != "1" {
		return
	}

	time.Sleep(5 * time.Second)
	os.Exit(0)
}

func TestGamesStartShortCircuitsAcrossServers(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "gabs-shared-runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("GABS_HELPER_PROCESS", "1")

	game := helperProcessGameConfig(t, "shared-runtime-game")
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			game.ID: game,
		},
	}

	logger := util.NewLogger("error")
	serverA := NewServerForTesting(logger)
	serverA.SetConfigDir(tempDir)
	serverA.RegisterGameManagementTools(gamesConfig, 0, 0)

	serverB := NewServerForTesting(logger)
	serverB.SetConfigDir(tempDir)
	serverB.RegisterGameManagementTools(gamesConfig, 0, 0)

	startDone := make(chan string, 1)
	go func() {
		response := serverA.HandleMessage(toolCallMessage("start-a", "games.start", game.ID))
		startDone <- marshalMessage(t, response)
	}()

	waitForRuntimeState(t, game.ID, tempDir)

	startTime := time.Now()
	secondStart := marshalMessage(t, serverB.HandleMessage(toolCallMessage("start-b", "games.start", game.ID)))
	duration := time.Since(startTime)

	if duration > time.Second {
		t.Fatalf("expected duplicate start to return quickly, took %v (%s)", duration, secondStart)
	}
	if strings.Contains(secondStart, `"isError":true`) {
		t.Fatalf("expected idempotent duplicate start response, got %s", secondStart)
	}
	if !strings.Contains(secondStart, "already starting") && !strings.Contains(secondStart, "already running") {
		t.Fatalf("expected duplicate start warning, got %s", secondStart)
	}

	firstStart := waitForResponse(t, startDone, 5*time.Second)
	if strings.Contains(firstStart, `"isError":true`) {
		t.Fatalf("expected first start to succeed, got %s", firstStart)
	}

	status := marshalMessage(t, serverB.HandleMessage(toolCallMessage("status-b", "games.status", game.ID)))
	if !strings.Contains(status, "another GABS session owns the process") {
		t.Fatalf("expected shared runtime status, got %s", status)
	}

	connectStart := time.Now()
	connect := marshalMessage(t, serverB.HandleMessage(toolCallMessage("connect-b", "games.connect", game.ID)))
	connectDuration := time.Since(connectStart)
	if connectDuration > time.Second {
		t.Fatalf("expected duplicate connect to return quickly, took %v (%s)", connectDuration, connect)
	}
	if strings.Contains(connect, `"isError":true`) {
		t.Fatalf("expected duplicate connect to short-circuit cleanly, got %s", connect)
	}
	if !strings.Contains(connect, "another live GABS session") {
		t.Fatalf("expected duplicate connect guard, got %s", connect)
	}

	if err := serverA.stopGame(game, true); err != nil && !strings.Contains(err.Error(), "not running") {
		t.Fatalf("failed to stop helper game: %v", err)
	}
}

func TestGamesStartRemovesStaleSharedRuntimeState(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "gabs-stale-runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("GABS_HELPER_PROCESS", "1")

	game := helperProcessGameConfig(t, "stale-runtime-game")
	gamesConfig := &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			game.ID: game,
		},
	}

	staleState := process.RuntimeState{
		GameID:    game.ID,
		Status:    process.RuntimeStateStatusRunning,
		OwnerPID:  999999,
		GamePID:   999999,
		UpdatedAt: time.Now().UTC(),
	}
	if err := process.ClaimRuntimeState(game.ID, tempDir, staleState); err != nil {
		t.Fatalf("failed to seed stale runtime state: %v", err)
	}

	logger := util.NewLogger("error")
	server := NewServerForTesting(logger)
	server.SetConfigDir(tempDir)
	server.RegisterGameManagementTools(gamesConfig, 0, 0)

	start := marshalMessage(t, server.HandleMessage(toolCallMessage("start-stale", "games.start", game.ID)))
	if strings.Contains(start, `"isError":true`) {
		t.Fatalf("expected stale runtime state to be replaced, got %s", start)
	}

	runtimeState, err := process.LoadRuntimeState(game.ID, tempDir)
	if err != nil {
		t.Fatalf("failed to load runtime state after start: %v", err)
	}
	if runtimeState == nil {
		t.Fatal("expected runtime state to exist after successful start")
	}
	if runtimeState.OwnerPID != os.Getpid() {
		t.Fatalf("expected runtime state owner to be refreshed, got %d", runtimeState.OwnerPID)
	}

	if err := server.stopGame(game, true); err != nil && !strings.Contains(err.Error(), "not running") {
		t.Fatalf("failed to stop helper game: %v", err)
	}
}

func helperProcessGameConfig(t *testing.T, gameID string) config.GameConfig {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to locate test executable: %v", err)
	}

	return config.GameConfig{
		ID:         gameID,
		Name:       "Helper Process Game",
		LaunchMode: "DirectPath",
		Target:     exe,
		WorkingDir: filepath.Dir(exe),
		Args:       []string{"-test.run=TestSharedRuntimeStateHelperProcess"},
	}
}

func toolCallMessage(id, toolName, gameID string) *Message {
	return &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"` + id + `"`),
		Params: map[string]interface{}{
			"name": toolName,
			"arguments": map[string]interface{}{
				"gameId": gameID,
			},
		},
	}
}

func marshalMessage(t *testing.T, message *Message) string {
	t.Helper()

	data, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	return string(data)
}

func waitForRuntimeState(t *testing.T, gameID, configDir string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtimeState, err := process.LoadRuntimeState(gameID, configDir)
		if err == nil && runtimeState != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for runtime state for %s", gameID)
}

func waitForResponse(t *testing.T, ch <-chan string, timeout time.Duration) string {
	t.Helper()

	select {
	case response := <-ch:
		return response
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for response after %v", timeout)
		return ""
	}
}
