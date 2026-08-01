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
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/steam"
	"github.com/pardeike/gabs/internal/util"
)

// The Stage-2 store-launcher advisory (design/05, M2.15): a Steam mode warns
// when the client is not observable; SteamManaged additionally runs bounded
// best-effort assistance, SteamAppId does not; a present client emits neither.
func TestSteamStoreLauncherAdvisory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix shell script as the launch target")
	}

	run := func(t *testing.T, mode string, running func() bool) (text string, ensureCalled bool) {
		// This regression covers the retained advisory behavior; the strict
		// macOS SteamManaged gate has its own test below.
		t.Cleanup(steam.SetFunctionalReadinessForTesting(false, nil))
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "game.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		called := false
		restoreClient := steam.SetClientControlForTesting(
			func() (string, []string, error) { called = true; return "/bin/true", nil, nil },
			running, 0, 0,
		)
		t.Cleanup(restoreClient)
		restoreResolve := process.SetSteamResolveAppForTesting(func(id string) (process.SteamApp, error) {
			return process.SteamApp{Executable: script}, nil
		})
		t.Cleanup(restoreResolve)
		// The MCP layer statically resolves a SteamManaged executable before the
		// spawn; seam it to the script so resolution succeeds.
		restoreStatic := launch.SetSteamResolveExecutableForTesting(func(id string) (string, error) {
			return script, nil
		})
		t.Cleanup(restoreStatic)
		// URL modes track an opener helper — use the script so the test never
		// opens a real steam:// URL.
		restoreFactory := process.SetLaunchCommandFactoriesForTesting(
			func(target string) (string, []string) { return script, nil },
			func(target string) (string, []string) { return script, nil },
		)
		t.Cleanup(restoreFactory)

		game := config.GameConfig{ID: "g", Name: "G", LaunchMode: mode, Target: "123", StopProcessName: "game.sh"}
		gamesConfig := &config.GamesConfig{
			Games: map[string]config.GameConfig{"g": game},
			// Bound every mode's process-start deadline (URL modes default to
			// 60s) so the test stays fast, while leaving budget past the
			// bridge-lock + spawn reserve for SteamManaged assistance to run.
			Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{ProcessStartSeconds: 10}},
		}
		server := NewServerForTesting(t, util.NewLogger("error"))
		server.SetConfigDir(tmpDir)
		server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)
		t.Cleanup(func() { _ = server.stopGame(game, true) })

		text = marshalMessage(t, server.HandleMessage(&Message{
			JSONRPC: "2.0", Method: "tools/call", ID: json.RawMessage(`"s"`),
			Params: map[string]interface{}{"name": "games.start", "arguments": map[string]interface{}{"gameId": "g", "timeout": 1}},
		}))
		return text, called
	}

	// false on the scan, true when assistance re-checks → advisory recorded AND
	// assistance completes fast (warm path).
	absentThenUp := func() func() bool {
		n := 0
		return func() bool { n++; return n > 1 }
	}
	alwaysUp := func() bool { return true }
	alwaysDown := func() bool { return false }

	t.Run("SteamManaged absent: advisory once + assistance attempted", func(t *testing.T) {
		text, called := run(t, "SteamManaged", absentThenUp())
		if strings.Count(text, steamNotRunningAdvisory) != 1 {
			t.Fatalf("advisory must appear exactly once, got: %s", text)
		}
		if !called {
			t.Fatal("SteamManaged must attempt assistance when Steam is absent")
		}
	})

	t.Run("SteamManaged present: no advisory, no assistance", func(t *testing.T) {
		text, called := run(t, "SteamManaged", alwaysUp)
		if strings.Contains(text, steamNotRunningAdvisory) {
			t.Fatalf("no advisory when Steam is present, got: %s", text)
		}
		if called {
			t.Fatal("no assistance when Steam is present")
		}
	})

	t.Run("SteamAppId absent: advisory but no managed assistance", func(t *testing.T) {
		text, called := run(t, "SteamAppId", alwaysDown)
		if !strings.Contains(text, steamNotRunningAdvisory) {
			t.Fatalf("SteamAppId must still get the advisory, got: %s", text)
		}
		if called {
			t.Fatal("SteamAppId must NOT run the managed ensure step")
		}
	})
}

func TestMacSteamManagedReadinessFailureIsStructuredAndDoesNotSpawn(t *testing.T) {
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "game.sh")
	marker := filepath.Join(tmpDir, "spawned")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \""+marker+"\"\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var receivedTimeout time.Duration
	t.Cleanup(steam.SetFunctionalReadinessForTesting(true, func(appID string, timeout time.Duration) steam.ReadinessResult {
		if appID != "123456" {
			t.Fatalf("readiness app ID = %q", appID)
		}
		receivedTimeout = timeout
		return steam.ReadinessResult{
			Reason: steam.ReadinessReasonTimeout, Stage: steam.ReadinessStageSteamAPI,
			Detail: "Steamworks API initialization failed", Retryable: true,
			Waited: 1500 * time.Millisecond, Timeout: timeout,
		}
	}))
	t.Cleanup(process.SetSteamResolveAppForTesting(func(string) (process.SteamApp, error) {
		return process.SteamApp{Executable: script, WorkingDir: tmpDir}, nil
	}))
	t.Cleanup(launch.SetSteamResolveExecutableForTesting(func(string) (string, error) { return script, nil }))

	game := config.GameConfig{
		ID: "factory", Name: "Factory", LaunchMode: "SteamManaged", Target: "123456",
		LaunchInputs: map[string]config.LaunchInputConfig{
			"scenario": {Description: "scenario", Type: "string", Args: []string{"--scenario=${value}"}},
		},
	}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	t.Cleanup(server.Shutdown)
	server.RegisterGameManagementTools(&config.GamesConfig{Games: map[string]config.GameConfig{game.ID: game}}, 10*time.Millisecond, 20*time.Millisecond)

	const secretInput = "private-scenario-value"
	raw, structured := callTool(t, server, "games.start", map[string]interface{}{
		"gameId": game.ID, "timeout": 2,
		"launchInputs": map[string]interface{}{"scenario": secretInput},
	})
	if structured["code"] != "store_client_not_ready" || structured["causeClass"] != process.CauseEnvironment {
		t.Fatalf("unexpected readiness result: %s", raw)
	}
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("readiness failure must be an MCP error result: %s", raw)
	}
	for key, want := range map[string]interface{}{
		"store": "steam", "reason": "readiness_timeout", "readinessStage": "steam_api",
		"retryable": true, "processStarted": false,
	} {
		if structured[key] != want {
			t.Errorf("%s = %#v, want %#v", key, structured[key], want)
		}
	}
	if receivedTimeout != 2*time.Second || structured["timeoutMs"] != float64(2000) || structured["waitedMs"] != float64(1500) {
		t.Fatalf("timeout evidence/caller cap was not preserved: received=%v structured=%#v", receivedTimeout, structured)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("game process ran before readiness proof: %v", err)
	}
	if claim, err := process.LoadRuntimeState(game.ID, tmpDir); err != nil || claim != nil {
		t.Fatalf("readiness failure must release the claim: claim=%+v err=%v", claim, err)
	}
	statusRaw, status := callTool(t, server, "games.status", map[string]interface{}{"gameId": game.ID})
	if status["status"] != "stopped" {
		t.Fatalf("status after a pre-spawn failure must be stopped, not starting/unobserved: %s", statusRaw)
	}
	if _, ok := status["nextActions"].([]interface{}); !ok {
		t.Fatalf("status must retain structured nextActions after readiness failure: %s", statusRaw)
	}
	history, err := process.LoadHistory(game.ID, tmpDir)
	if err != nil || len(history.Profiles) != 0 {
		t.Fatalf("readiness failure must not mutate history: history=%+v err=%v", history, err)
	}
	if strings.Contains(raw, secretInput) {
		t.Fatalf("diagnostic echoed a raw launch-input value: %s", raw)
	}
	if !strings.Contains(raw, "preserveOriginalLaunchInputs") || !strings.Contains(raw, "scenario") {
		t.Fatalf("retry guidance must preserve input names without values: %s", raw)
	}
}
