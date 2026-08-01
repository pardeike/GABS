package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// The persisted operation deadline is authoritative across Stage 2 + Stage 4
// (M2.15): if contended endpoint preparation consumes the deadline, the accepted
// start must NOT spawn against a now-supersedable operation, and a concurrent
// second start must not replace the first while its executor is legitimately
// proceeding (before the deadline).
func TestStartCannotSpawnAfterDeadlineConsumedByEndpointContention(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix shell script + bridge flock")
	}
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "spawned.marker")
	script := filepath.Join(tmpDir, "game.sh")
	// The workload records a spawn side-effect; its presence proves a spawn.
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	game := config.GameConfig{ID: "g", Name: "G", LaunchMode: "DirectPath", Target: script}
	gamesConfig := &config.GamesConfig{
		Games: map[string]config.GameConfig{"g": game},
		// A short absolute deadline so contended endpoint prep exceeds it.
		Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{ProcessStartSeconds: 1}},
	}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)
	t.Cleanup(func() { _ = server.stopGame(game, true) })

	startMsg := func(id string) *Message {
		return &Message{JSONRPC: "2.0", Method: "tools/call", ID: json.RawMessage(`"` + id + `"`),
			Params: map[string]interface{}{"name": "games.start", "arguments": map[string]interface{}{"gameId": "g", "timeout": 1}}}
	}

	// Hold the bridge lock so the first start's endpoint prep blocks past the 1s
	// operation deadline.
	release, err := config.AcquireBridgeLockForTesting(tmpDir, "g")
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan string, 1)
	go func() { firstDone <- marshalMessage(t, server.HandleMessage(startMsg("first"))) }()

	// Within the deadline window, a second start cannot replace the legitimately
	// proceeding first — it is refused operation_in_progress.
	time.Sleep(300 * time.Millisecond)
	secondText := marshalMessage(t, server.HandleMessage(startMsg("second")))
	if !strings.Contains(secondText, "operation_in_progress") {
		t.Fatalf("a second start must be refused while the first legitimately proceeds: %s", secondText)
	}

	// Let the deadline pass, then free the lock so the first's endpoint prep
	// proceeds — but now past its deadline; the pre-spawn gate must refuse to spawn.
	time.Sleep(1 * time.Second)
	release()

	firstText := <-firstDone
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("the first start spawned after its operation became supersedable: %s", firstText)
	}
	if strings.Contains(firstText, `"causeClass":"game"`) || strings.Contains(firstText, "spawn_failed") || strings.Contains(firstText, "exited_during_start") {
		t.Fatalf("a deadline abort must be a state/supersession outcome, not a game fault: %s", firstText)
	}
}
