package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
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
