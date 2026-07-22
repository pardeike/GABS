package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestExitedDuringStartIsGameAcrossLaunchModes is the F6 production contract:
// a post-spawn exit is game-class (the evidence-based default) no matter the
// launch mode — GABS cannot distinguish a game crash from a wrapper/container
// exit at the first process it created, so mode, target shape, and status-hook
// results are never cause evidence. Each mode drives a deterministic
// exited_during_start through the real handler and asserts causeClass=game.
func TestExitedDuringStartIsGameAcrossLaunchModes(t *testing.T) {
	execFile := func(t *testing.T) string {
		dir := t.TempDir()
		p := filepath.Join(dir, "game.sh")
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name string
		mode string
		// build returns the target and any resolver injection restore func.
		setup func(t *testing.T) (target string, restore func())
	}{
		{
			name:  "DirectPath",
			mode:  "DirectPath",
			setup: func(t *testing.T) (string, func()) { return execFile(t), func() {} },
		},
		{
			name:  "CustomCommand",
			mode:  "CustomCommand",
			setup: func(t *testing.T) (string, func()) { return execFile(t), func() {} },
		},
		{
			name: "SteamManaged",
			mode: "SteamManaged",
			setup: func(t *testing.T) (string, func()) {
				exe := execFile(t)
				restore := launch.SetSteamResolveExecutableForTesting(func(appID string) (string, error) { return exe, nil })
				return "480", restore // any app id; the resolver is pinned to exe
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, restore := tc.setup(t)
			defer restore()

			s := NewServerForTesting(util.NewLogger("error"))
			s.SetConfigDir(t.TempDir())
			t.Cleanup(s.Shutdown)
			s.SetControllerFactoryForTesting(newExitBeforeStage4ControllerMode(tc.mode))
			game := config.GameConfig{ID: "g", Name: "G", LaunchMode: tc.mode, Target: target}
			s.RegisterGameManagementTools(&config.GamesConfig{
				Version: "1.0", Games: map[string]config.GameConfig{game.ID: game},
			}, 0, 0)

			raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "g", "timeout": 1})
			if structured["code"] != "exited_during_start" {
				t.Fatalf("%s: expected exited_during_start, got %v (%s)", tc.mode, structured["code"], raw)
			}
			if structured["causeClass"] != process.CauseGame {
				t.Fatalf("%s: a post-spawn exit must be game-class, got %#v", tc.mode, structured["causeClass"])
			}
			// The failure is recorded with the game class.
			h, _ := process.LoadHistory("g", s.configDir)
			if e := h.Profiles[""]; e == nil || e.LastFailure == nil || e.LastFailure.Class != process.CauseGame {
				t.Fatalf("%s: the terminal failure must record the game class: %+v", tc.mode, h.Profiles[""])
			}
		})
	}
}

// TestStatusHookStoppedExitRendersGame proves the status hook is liveness, not
// cause evidence (F6): the production render for exited_during_start —
// finalizeStartFailure, the exact function the handler calls whether the exit
// was surfaced by a dead PID or a status hook reporting stopped — no longer
// takes any hook/wrapper input and yields game-class unconditionally. The
// removed WrapperExit seam cannot resurface here.
func TestStatusHookStoppedExitRendersGame(t *testing.T) {
	s := newProfiledServer(t)
	game := config.GameConfig{ID: "adventure", Name: "Adventure", LaunchMode: "DirectPath"}
	structured := map[string]interface{}{"code": "exited_during_start", "gameId": game.ID}
	s.finalizeStartFailure(structured, game, historyContext{}, "exited_during_start")
	if structured["causeClass"] != process.CauseGame {
		t.Fatalf("the production render of exited_during_start must be game-class, got %#v", structured["causeClass"])
	}
}
