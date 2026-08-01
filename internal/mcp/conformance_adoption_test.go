package mcp

import (
	"encoding/json"
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

// TestConformanceEnvScrubbingLauncherAdopted is the T-DELIV adoption cell
// (design/05 Stage 4, design/30): a Steam-style launcher scrubs the environment,
// backgrounds the workload, and exits. The direct child is reaped while the
// workload remains observable by name, so the START MANAGER adopts it — the
// injected context did not survive the re-exec (delivery unverified), but the
// game is tracked as running rather than reported failed. This exercises the
// real games.start lifecycle, not Controller.Start in isolation.
func TestConformanceEnvScrubbingLauncherAdopted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh scrubbing launcher; the Windows adoption path runs on the CI lane")
	}
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "workload.pid")
	launcher := filepath.Join(tmpDir, "launch.sh")
	// Scrub the environment (Steam-style re-exec), background a long-lived
	// workload, record its pid, and exit so the direct child is reaped.
	script := "#!/bin/sh\nenv -i /bin/sleep 30 &\necho $! > " + shellQuote(pidPath) + "\nexit 0\n"
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	game := config.GameConfig{
		ID:              "adopt-scrub",
		Name:            "Adopt Scrub",
		LaunchMode:      "CustomCommand",
		Target:          launcher,
		StopProcessName: "sleep-workload",
	}
	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{game.ID: game}}

	// The scrubbed workload remains observable by name — the adoption backstop
	// (design/04): after the launcher exits, the name scan finds the survivor.
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

	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	startText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"start-adopt-scrub"`),
		Params: map[string]interface{}{
			"name": "games.start",
			"arguments": map[string]interface{}{
				"gameId":  game.ID,
				"timeout": 1,
			},
		},
	}))
	t.Cleanup(func() { _ = server.stopGame(game, true) })

	if strings.Contains(startText, `"isError":true`) {
		t.Fatalf("an adopted launch is bounded and non-error, got: %s", startText)
	}
	if !strings.Contains(startText, `"adopted":true`) && !strings.Contains(startText, "the launch was adopted") {
		t.Fatalf("the env-scrubbing launcher exit must yield an adopted result, got: %s", startText)
	}
}
