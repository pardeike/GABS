package mcp

import (
	"os"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// assertAttributed checks a structured failure carries all three fields
// independently — causeClass, a track-record line, and next actions (round 13
// F2: a partially-attributed result must not escape).
func assertAttributed(t *testing.T, tool string, structured map[string]interface{}, wantClass string) {
	t.Helper()
	if structured["causeClass"] != wantClass {
		t.Fatalf("%s: causeClass=%#v, want %q", tool, structured["causeClass"], wantClass)
	}
	if line, ok := structured["trackRecord"].(string); !ok || !strings.Contains(line, "no successful starts") {
		t.Fatalf("%s: missing neutral track-record line: %#v", tool, structured["trackRecord"])
	}
	if _, ok := structured["nextActions"]; !ok {
		t.Fatalf("%s: missing nextActions", tool)
	}
}

// TestGamesStatusAndShowFailuresCarryAttribution triggers the REAL non-lifecycle
// handler branches the reviewer named (games_status/show game_not_found return
// directly from resolveGameResult) and proves the central completion step
// attributes them — not just the four lifecycle tools.
func TestGamesStatusAndShowFailuresCarryAttribution(t *testing.T) {
	for _, tool := range []string{"games.status", "games.show"} {
		t.Run(tool, func(t *testing.T) {
			s := newProfiledServer(t)
			_, structured := callTool(t, s, tool, map[string]interface{}{"gameId": "does-not-exist"})
			if structured["code"] != "game_not_found" {
				t.Fatalf("%s: expected game_not_found, got %v", tool, structured["code"])
			}
			assertAttributed(t, tool, structured, process.CauseCall)
		})
	}
}

// TestStopKillUnreadableClaimCarriesAttribution triggers the codeless stop/kill
// claim-read branch (lifecycleActionResult): a corrupt runtime.json makes
// LoadRuntimeState error, which now returns the authorized blocked_unknown_state
// with full attribution.
func TestStopKillUnreadableClaimCarriesAttribution(t *testing.T) {
	for _, action := range []string{"games.stop", "games.kill"} {
		t.Run(action, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			s := NewServerForTesting(t, util.NewLogger("error"))
			s.SetConfigDir(dir)
			t.Cleanup(s.Shutdown)
			s.RegisterGameManagementTools(&config.GamesConfig{
				Version: "1.0",
				Games:   map[string]config.GameConfig{"g": {ID: "g", Name: "G", LaunchMode: "DirectPath", Target: exe}},
			}, 0, 0)

			// Corrupt the runtime claim so LoadRuntimeState errors.
			cp, _ := config.NewConfigPaths(dir)
			if err := cp.EnsureGameDir("g"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cp.GetRuntimeStatePath("g"), []byte("{not valid json"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, structured := callTool(t, s, action, map[string]interface{}{"gameId": "g"})
			if structured["code"] != "blocked_unknown_state" {
				t.Fatalf("%s: an unreadable claim must return blocked_unknown_state, got %v", action, structured["code"])
			}
			assertAttributed(t, action, structured, process.CauseState)
		})
	}
}

// TestConnectFailureCarriesAttribution triggers the ordinary connection-failure
// branch (games_connect to a claim with an endpoint no server answers) and
// proves it now carries an authorized code + attribution.
func TestConnectFailureCarriesAttribution(t *testing.T) {
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	t.Cleanup(s.Shutdown)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games:   map[string]config.GameConfig{"g": {ID: "g", Name: "G", LaunchMode: "DirectPath", Target: "/opt/game"}},
	}, 0, 0)
	// A running claim with an endpoint nothing is listening on → connect fails.
	seedClaimEndpointForTest(t, dir, "g", 46111, "tok")

	_, structured := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "g"})
	code, _ := structured["code"].(string)
	if code == "" {
		t.Fatalf("a connect failure must carry a stable code: %#v", structured)
	}
	if process.Classify(code, process.ClassifyContext{}).Class == "" {
		t.Fatalf("connect failure code %q must map to a cause class", code)
	}
	if _, ok := structured["causeClass"]; !ok {
		t.Fatalf("a connect failure must carry causeClass: %#v", structured)
	}
}
