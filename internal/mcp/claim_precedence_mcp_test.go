package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestClaimIDPrecedenceOverTargetAlias pins the lifecycle resolution order: a
// removed-but-claimed game whose ID equals another configured game's unique
// launch target must route specific status/stop/kill to the persisted claim,
// never through the target alias to the other game — an alias capture would
// let a destructive action hit the wrong workload while the intended claim
// stays untouched.
func TestClaimIDPrecedenceOverTargetAlias(t *testing.T) {
	tmpDir := t.TempDir()
	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{
		"other": {ID: "other", Name: "Other", LaunchMode: "DirectPath", Target: "ghost"},
	}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)
	seedRuntimeOnlyClaim(t, tmpDir, "ghost")

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("s", "games.status", "ghost")))
	if !strings.Contains(statusText, "unconfigured") {
		t.Fatalf("the exact claim ID must resolve to the persisted claim, not the target alias: %s", statusText)
	}
	if strings.Contains(statusText, "(Other)") {
		t.Fatalf("the aliased configured game must not answer for the claim ID: %s", statusText)
	}

	stopText := marshalMessage(t, server.HandleMessage(toolCallMessage("k", "games.stop", "ghost")))
	if strings.Contains(stopText, "'other'") || strings.Contains(stopText, "(Other)") {
		t.Fatalf("stop of the claim ID must never be routed to the aliased game: %s", stopText)
	}
}

// TestIndeterminateClaimBlocksAliasFallback pins the error-preserving side of
// claim precedence: when the exact-claim check cannot establish absence (here
// a non-regular runtime.json leaf), resolution must refuse the target-alias
// fallback instead of routing a destructive action to the other game.
func TestIndeterminateClaimBlocksAliasFallback(t *testing.T) {
	tmpDir := t.TempDir()
	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{
		"other": {ID: "other", Name: "Other", LaunchMode: "DirectPath", Target: "ghost"},
	}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)
	// A directory named runtime.json: the claim path exists but is not an
	// addressable regular claim — absence cannot be established.
	if err := os.MkdirAll(filepath.Join(tmpDir, "ghost", "runtime.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	stopText := marshalMessage(t, server.HandleMessage(toolCallMessage("k", "games.stop", "ghost")))
	if !strings.Contains(stopText, "blocked_unknown_state") {
		t.Fatalf("an indeterminate exact claim must block resolution, got: %s", stopText)
	}
	if strings.Contains(stopText, "'other'") || strings.Contains(stopText, "(Other)") {
		t.Fatalf("an indeterminate exact claim must never fall back to the target alias: %s", stopText)
	}
}

// TestTargetAliasStillResolvesWithoutClaim keeps the alias convenience: when
// no claim shadows the target, a unique target reference still resolves to
// its configured game.
func TestTargetAliasStillResolvesWithoutClaim(t *testing.T) {
	tmpDir := t.TempDir()
	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{
		"other": {ID: "other", Name: "Other", LaunchMode: "DirectPath", Target: "ghost"},
	}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(tmpDir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("s", "games.status", "ghost")))
	if !strings.Contains(statusText, "**other**") {
		t.Fatalf("a unique target with no shadowing claim must resolve to its game: %s", statusText)
	}
}

// TestStartByTargetOnModeIncompatibleEdit pins the hot-edit classification: a
// start referencing the game by TARGET must yield the same stable
// launch_mode_incompatible outcome as one referencing it by ID — the
// validation issues are pathed by game ID, so the handler must resolve the
// alias against the last-good snapshot before classifying.
func TestStartByTargetOnModeIncompatibleEdit(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	valid := `{"version":"1.0","games":{"u":{"id":"u","name":"U","launchMode":"DirectPath","target":"12345"}}}`
	if err := os.WriteFile(cfgPath, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}

	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	initial, err := config.LoadGamesConfigFromPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterGameManagementTools(initial, 0, 0)
	s.SetConfigStore(config.NewStore(cfgPath))

	// The hot edit gives the URL-mode game context fields: purely
	// mode-incompatible, the specific stable outcome.
	invalid := `{"version":"1.0","games":{"u":{"id":"u","name":"U","launchMode":"SteamAppId","target":"12345","stopProcessName":"game.exe","defaultProfile":"fast","profiles":{"fast":{"args":["--fast"]}}}}}`
	if err := os.WriteFile(cfgPath, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}

	_, byID := callTool(t, s, "games.start", map[string]interface{}{"gameId": "u"})
	if byID["code"] != "launch_mode_incompatible" {
		t.Fatalf("start by ID must classify the mode-incompatible edit, got %v", byID)
	}
	_, byTarget := callTool(t, s, "games.start", map[string]interface{}{"gameId": "12345"})
	if byTarget["code"] != "launch_mode_incompatible" {
		t.Fatalf("start by target must classify identically to start by ID, got %v", byTarget)
	}
}
