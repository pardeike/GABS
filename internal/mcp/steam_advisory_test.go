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
			// 60s) so the test stays fast; still leaves > headroom for assistance.
			Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{ProcessStartSeconds: 5}},
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
