package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// flashExitServer configures a game whose target exits immediately, so a
// start reliably produces exited_during_start with a resolved context.
func flashExitServer(t *testing.T) (*Server, config.GameConfig) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	game := config.GameConfig{
		ID: "flash", Name: "Flash", LaunchMode: "DirectPath", Target: exe,
		Args: []string{"-test.run=TestSharedRuntimeStateHelperProcess"},
	}
	s := NewServerForTesting(util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games:   map[string]config.GameConfig{game.ID: game},
	}, 0, 0)
	return s, game
}

func TestExitedDuringStartCarriesGameCauseAndActions(t *testing.T) {
	s, game := flashExitServer(t)
	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1})
	if structured["code"] != "exited_during_start" {
		t.Fatalf("expected exited_during_start: %s", raw)
	}
	if structured["causeClass"] != process.CauseGame {
		t.Fatalf("a crash-on-start is game class, got %v", structured["causeClass"])
	}
	// The failure was recorded with the game class.
	h, _ := process.LoadHistory(game.ID, s.configDir)
	e := h.Profiles[""]
	if e == nil || e.ConsecutiveFailures != 1 || e.LastFailure == nil || e.LastFailure.Class != process.CauseGame {
		t.Fatalf("the terminal failure must be recorded with the game class: %+v", e)
	}
	// No next action for a non-config class may propose editing config.
	assertNoConfigEditAction(t, raw, structured)
}

func TestNextActionsNeverProposeConfigEditForNonConfigClasses(t *testing.T) {
	// A template-level assertion over every non-config class.
	for _, class := range []string{process.CauseCall, process.CauseEnvironment, process.CauseGame, process.CauseState} {
		actions := failureNextActions("g", class)
		blob, _ := json.Marshal(actions)
		low := strings.ToLower(string(blob))
		if strings.Contains(low, "edit") && strings.Contains(low, "config") {
			t.Fatalf("class %s must not propose a config edit: %s", class, blob)
		}
	}
	// The config class MAY route to games_show for correction.
	cfg, _ := json.Marshal(failureNextActions("g", process.CauseConfig))
	if !strings.Contains(string(cfg), "games_show") {
		t.Fatalf("the config class should route to games_show: %s", cfg)
	}
}

func TestCallClassErrorsMutateNoHistory(t *testing.T) {
	s := newProfiledServer(t)
	// An undeclared input is a call-class error — before any context exists.
	raw, _ := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "launchInputs": map[string]interface{}{"nope": "x"},
	})
	if !strings.Contains(raw, "launch_input_not_declared") {
		t.Fatalf("expected launch_input_not_declared: %s", raw)
	}
	// No history file was created; a caller typo never distorts proof.
	if h, _ := process.LoadHistory("adventure", s.configDir); len(h.Profiles) != 0 {
		t.Fatalf("a call-class error must mutate no history: %+v", h)
	}
}

func TestTrackRecordSurvivesFailureAndReset(t *testing.T) {
	s, game := flashExitServer(t)

	// First start: crash → recorded game failure.
	callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1})
	h, _ := process.LoadHistory(game.ID, s.configDir)
	if h.Profiles[""].ConsecutiveFailures != 1 {
		t.Fatalf("one failure recorded: %+v", h.Profiles[""])
	}

	// history.json is 0600.
	cp, _ := config.NewConfigPaths(s.configDir)
	if fi, err := os.Stat(cp.GetHistoryPath(game.ID)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("history.json must be 0600: mode=%v err=%v", fi.Mode().Perm(), err)
	}

	// The entrySnapshot (which may hold env values) never appears in an MCP
	// result. Prime a lastGood, then assert no status/start result leaks it.
	_ = process.RecordWorkloadStart(game.ID, s.configDir, "", h.Profiles[""].ContextHash,
		process.ContextSnapshot{Target: "SECRET-TARGET", ConfigEnv: map[string]string{"K": "SECRET-VALUE"}},
		nil, "", "", timeNow())
	raw, _ := callTool(t, s, "games.status", map[string]interface{}{"gameId": game.ID})
	if strings.Contains(raw, "SECRET-VALUE") || strings.Contains(raw, "entrySnapshot") {
		t.Fatalf("the last-good snapshot must never reach an MCP result: %s", raw)
	}
}

func TestEditNoticeFiresOncePerEdit(t *testing.T) {
	dir := t.TempDir()
	g := config.GameConfig{ID: "adv", LaunchMode: "DirectPath", Target: "/opt/game", Args: []string{"-v1"}}

	// Prove the context, then record a non-config (environment) failure.
	hash := process.ContextHash(g, "", nil)
	_ = process.RecordWorkloadStart("adv", dir, "", hash, process.ContextSnapshot{}, nil, "", "", timeNow())
	_ = process.RecordFailure("adv", dir, "", hash, "spawn_failed", process.CauseEnvironment, nil, timeNow())

	s := NewServerForTesting(util.NewLogger("error"))
	s.SetConfigDir(dir)

	// The config changed (new args → new hash): the notice fires once.
	edited := g
	edited.Args = []string{"-v2"}
	hc := historyContext{profile: "", contextHash: process.ContextHash(edited, "", nil)}
	n1 := s.editNoticeFor("adv", hc)
	if n1 == "" || !strings.Contains(n1, "environment-class failure") {
		t.Fatalf("the edit notice must fire after a proven+non-config-failure edit: %q", n1)
	}
	// Exactly once per edit.
	if n2 := s.editNoticeFor("adv", hc); n2 != "" {
		t.Fatalf("the notice must fire only once per edit, got %q", n2)
	}
}

func TestEditNoticeDoesNotFireForAdditiveEdit(t *testing.T) {
	dir := t.TempDir()
	g := config.GameConfig{ID: "adv", LaunchMode: "DirectPath", Target: "/opt/game",
		Profiles: map[string]config.ProfileConfig{"a": {Args: []string{"-a"}}}}
	hashA := process.ContextHash(g, "a", nil)
	_ = process.RecordWorkloadStart("adv", dir, "a", hashA, process.ContextSnapshot{}, nil, "", "", timeNow())
	_ = process.RecordFailure("adv", dir, "a", hashA, "spawn_failed", process.CauseEnvironment, nil, timeNow())

	s := NewServerForTesting(util.NewLogger("error"))
	s.SetConfigDir(dir)
	// Adding profile B leaves A's hash unchanged: no notice for A.
	added := g
	added.Profiles["b"] = config.ProfileConfig{Args: []string{"-b"}}
	hc := historyContext{profile: "a", contextHash: process.ContextHash(added, "a", nil)}
	if n := s.editNoticeFor("adv", hc); n != "" {
		t.Fatalf("an additive edit must not fire the notice for an unchanged context: %q", n)
	}
}

func assertNoConfigEditAction(t *testing.T, raw string, structured map[string]interface{}) {
	t.Helper()
	actions, _ := structured["nextActions"].([]interface{})
	blob, _ := json.Marshal(actions)
	low := strings.ToLower(string(blob))
	if strings.Contains(low, "edit") && strings.Contains(low, "config") {
		t.Fatalf("a non-config failure must not propose a config edit: %s", raw)
	}
}

func timeNow() time.Time { return time.Now().UTC() }
