package mcp

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// flashExitServer configures a game whose workload exits BEFORE Stage 4, via a
// deterministic fake controller (round 12 F5) — a real subprocess's timing is
// non-deterministic under -race and would sometimes be credited a Stage-4
// workloadStart before dying. The injected controller proves the exit, so
// exited_during_start with no workloadStart is deterministic.
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
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	t.Cleanup(s.Shutdown)
	s.SetControllerFactoryForTesting(newExitBeforeStage4Controller)
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

// TestVerifiedThenStage5DeathRecordsStartAndFailure is the F5 companion cell:
// a workload verified running at Stage 4 (one workloadStart) that then dies
// during the Stage-5 bridge wait records exactly one failure on top. Both
// halves are deterministic via the fake controller.
func TestVerifiedThenStage5DeathRecordsStartAndFailure(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	t.Cleanup(s.Shutdown)
	s.SetControllerFactoryForTesting(newVerifiedThenDeathController)
	game := config.GameConfig{
		ID: "flash", Name: "Flash", LaunchMode: "DirectPath", Target: exe,
		Args: []string{"-test.run=TestSharedRuntimeStateHelperProcess"},
	}
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0", Games: map[string]config.GameConfig{game.ID: game},
	}, 0, 0)

	callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1})

	h, _ := process.LoadHistory(game.ID, s.configDir)
	e := h.Profiles[""]
	if e == nil || e.WorkloadStarts != 1 {
		t.Fatalf("a Stage-4-verified start must credit one workloadStart: %+v", e)
	}
	if e.ConsecutiveFailures != 1 || e.LastFailure == nil || e.LastFailure.Outcome != "exited_during_start" {
		t.Fatalf("a Stage-5 death must record one failure after the credit: %+v", e)
	}
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
	lid := seedTrackClaim(t, game.ID, s.configDir)
	_ = process.RecordWorkloadStart(game.ID, s.configDir, lid, "", h.Profiles[""].ContextHash,
		process.ContextSnapshot{Target: "SECRET-TARGET", ConfigEnv: map[string]string{"K": "SECRET-VALUE"}},
		process.SuccessBucket{}, timeNow())
	raw, _ := callTool(t, s, "games.status", map[string]interface{}{"gameId": game.ID})
	if strings.Contains(raw, "SECRET-VALUE") || strings.Contains(raw, "entrySnapshot") {
		t.Fatalf("the last-good snapshot must never reach an MCP result: %s", raw)
	}
}

func TestEditNoticeFiresOncePerEdit(t *testing.T) {
	dir := t.TempDir()
	g := config.GameConfig{ID: "adv", LaunchMode: "DirectPath", Target: "/opt/game", Args: []string{"-v1"}}

	// Prove the context, then record a non-config (environment) failure.
	lid := seedTrackClaim(t, "adv", dir)
	hash := process.ContextHash(&launch.BaseContext{Target: "/opt/game", Mode: "DirectPath", Args: []string{"-v1"}})
	_ = process.RecordWorkloadStart("adv", dir, lid, "", hash, process.ContextSnapshot{}, process.SuccessBucket{}, timeNow())
	_ = process.RecordFailure("adv", dir, lid, "", hash, "spawn_failed", process.CauseEnvironment, nil, timeNow())

	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)

	// The config changed (new args → new hash): the notice fires once.
	_ = g
	editedHash := process.ContextHash(&launch.BaseContext{Target: "/opt/game", Mode: "DirectPath", Args: []string{"-v2"}})
	hc := historyContext{Profile: "", ContextHash: editedHash}
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
	lid := seedTrackClaim(t, "adv", dir)
	hashA := process.ContextHash(&launch.BaseContext{Target: "/opt/game", Mode: "DirectPath", Args: []string{"-base", "-a"}})
	_ = process.RecordWorkloadStart("adv", dir, lid, "a", hashA, process.ContextSnapshot{}, process.SuccessBucket{}, timeNow())
	_ = process.RecordFailure("adv", dir, lid, "a", hashA, "spawn_failed", process.CauseEnvironment, nil, timeNow())

	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	// Adding profile B leaves A's hash unchanged: no notice for A.
	hc := historyContext{Profile: "a", ContextHash: hashA}
	if n := s.editNoticeFor("adv", hc); n != "" {
		t.Fatalf("an additive edit must not fire the notice for an unchanged context: %q", n)
	}
}

func TestGamesShowExposesPerProfileTrackRecord(t *testing.T) {
	s, game := flashExitServer(t)
	// A real start crashes → a game-class failure is recorded for the default
	// context, through the real handler and finalizer.
	if _, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1}); structured["code"] != "exited_during_start" {
		t.Fatalf("setup: expected a crash-on-start, got %v", structured["code"])
	}

	_, show := callTool(t, s, "games.show", map[string]interface{}{"gameId": game.ID})
	rows, ok := show["trackRecord"].([]interface{})
	if !ok || len(rows) == 0 {
		t.Fatalf("games.show must expose a per-profile track record: %#v", show["trackRecord"])
	}
	row := rows[0].(map[string]interface{})
	if row["currentContextProven"] != false {
		t.Fatalf("a crashed-only context is not proven: %#v", row)
	}
	if row["consecutiveFailures"].(float64) != 1 {
		t.Fatalf("the recorded failure count must surface: %#v", row)
	}
	if row["lastFailureClass"] != process.CauseGame {
		t.Fatalf("the recorded failure class must surface: %#v", row)
	}
	if line, _ := row["trackRecord"].(string); !strings.Contains(line, "no successful starts") {
		t.Fatalf("a never-proven context renders the explicit line: %#v", row)
	}
}

func TestGamesShowEditedContextReadsNeverProven(t *testing.T) {
	s, game := flashExitServer(t)
	// A proven start recorded under a NOW-superseded context (a config edit
	// moved the current hash away from this one).
	lid := seedTrackClaim(t, game.ID, s.configDir)
	_ = process.RecordWorkloadStart(game.ID, s.configDir, lid, "", "sha256:superseded-context",
		process.ContextSnapshot{}, process.SuccessBucket{}, timeNow())
	_ = process.RemoveRuntimeState(game.ID, s.configDir)

	_, show := callTool(t, s, "games.show", map[string]interface{}{"gameId": game.ID})
	rows := show["trackRecord"].([]interface{})
	row := rows[0].(map[string]interface{})
	if row["currentContextProven"] != false {
		t.Fatalf("an edited context must read never-proven despite a prior proven start: %#v", row)
	}
	if row["contextChangedSinceLastRecord"] != true {
		t.Fatalf("the changed-context flag must be set: %#v", row)
	}
	if row["workloadStarts"].(float64) != 1 {
		t.Fatalf("the historical counter is still shown for context: %#v", row)
	}
}

func TestReloadEditingInputDeclarationInvalidatesBucketOnStart(t *testing.T) {
	s := newProfiledServer(t)
	snap, _ := s.currentSnapshot()
	game := snap.Config.Games["adventure"]

	// The input-free base context hash the default-profile start will use.
	base, berr := launch.ResolveBaseContext(snap, "adventure", game.DefaultProfile, launch.Options{
		InheritedEnv: os.Environ(), CaseInsensitiveEnv: runtime.GOOS == "windows",
	})
	if berr != nil {
		t.Fatalf("resolve base context: %v", berr)
	}
	hash := process.ContextHash(base)

	// Seed a proven success bucket for scenario=arena recorded under a STALE
	// per-input declaration (as if the declaration has since been edited).
	lid := seedTrackClaim(t, "adventure", s.configDir)
	_ = process.RecordWorkloadStart("adventure", s.configDir, lid, game.DefaultProfile, hash,
		process.ContextSnapshot{}, process.SuccessBucket{
			InputNames:   []string{"scenario"},
			PerInputDecl: map[string]string{"scenario": "sha256:stale-declaration"},
			DeclHash:     "sha256:stale-declaration",
			ValueDigest:  "digest",
		}, timeNow())
	_ = process.RemoveRuntimeState("adventure", s.configDir)

	pre, _ := process.LoadHistory("adventure", s.configDir)
	if e := pre.Profiles[game.DefaultProfile]; e == nil || len(e.Buckets) != 1 {
		t.Fatalf("setup: expected one seeded bucket, got %+v", pre.Profiles[game.DefaultProfile])
	}

	// A real start with the input runs buildHistoryContext, which invalidates
	// any bucket whose declaration no longer matches the live config (P2-9).
	callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "launchInputs": map[string]interface{}{"scenario": "arena"}, "timeout": 1,
	})

	post, _ := process.LoadHistory("adventure", s.configDir)
	if e := post.Profiles[game.DefaultProfile]; e != nil {
		for _, b := range e.Buckets {
			if b.PerInputDecl["scenario"] == "sha256:stale-declaration" {
				t.Fatalf("the stale-declaration bucket must be invalidated on start: %+v", e.Buckets)
			}
		}
	}
}

func TestSpecTooLargeRecordsViaDeferPathAndRenders(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	// A resolvable target with arguments past every supported platform's real
	// combined exec limit: the start is accepted, claims, then fails the
	// pre-spawn size check — the terminal failure whose history write rides the
	// DEFER path (pendingFailCode), not the inline exitedFailure recorder
	// (round 10 P1-2).
	game := config.GameConfig{
		ID: "big", Name: "Big", LaunchMode: "DirectPath", Target: exe,
		Args: definitelyOversizedExecArgs(),
	}
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0", Games: map[string]config.GameConfig{game.ID: game},
	}, 0, 0)

	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1})
	if structured["code"] != "spec_too_large" {
		t.Fatalf("expected spec_too_large: %s", raw)
	}
	if structured["causeClass"] != process.CauseConfig {
		t.Fatalf("spec_too_large is config class, got %#v", structured["causeClass"])
	}
	// The deferred record must have persisted the failure while the claim was
	// alive — a reorder that moved the release before the record would drop it.
	h, _ := process.LoadHistory(game.ID, s.configDir)
	e := h.Profiles[""]
	if e == nil || e.LastFailure == nil || e.LastFailure.Outcome != "spec_too_large" || e.ConsecutiveFailures != 1 {
		t.Fatalf("the deferred record site must persist the failure: %+v", e)
	}
	// The claim must have been released (the size failure is terminal).
	if claim, _ := process.LoadRuntimeState(game.ID, dir); claim != nil {
		t.Fatalf("a terminal spec_too_large must release the claim: %+v", claim)
	}
}

func TestStopRefusalCarriesCauseClassAndTrackRecord(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"urlgame": {ID: "urlgame", Name: "U", LaunchMode: "SteamAppId", Target: "123456"},
		},
	}, 0, 0)

	const ctxHash = "sha256:urlgame-context"
	// A kill-only claim (no stop mechanism) pinned to a history context, so a
	// games_stop is refused as stop_unsupported without touching any process.
	spec := process.LaunchSpec{GameId: "urlgame", Mode: "SteamAppId", PathOrId: "123456"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.HistoryContextHash = ctxHash
	st.Lifecycle = &launch.ResolvedLifecycle{
		Kill: &launch.ResolvedHook{Command: exe, TimeoutSeconds: 5, VerifyTimeoutSeconds: 1},
	}
	if err := process.ClaimRuntimeState("urlgame", dir, st); err != nil {
		t.Fatal(err)
	}
	// A prior failure recorded for that pinned context establishes an entry
	// the refusal can render.
	_ = process.RecordFailure("urlgame", dir, st.LaunchID, "", ctxHash, "action_failed", process.CauseEnvironment, nil, timeNow())

	raw, structured := callTool(t, s, "games.stop", map[string]interface{}{"gameId": "urlgame"})
	if structured["code"] != "stop_unsupported" {
		t.Fatalf("expected stop_unsupported: %s", raw)
	}
	if structured["causeClass"] != process.CauseConfig {
		t.Fatalf("a stop_unsupported refusal is config class, got %#v", structured["causeClass"])
	}
	if _, ok := structured["trackRecord"].(string); !ok {
		t.Fatalf("the refusal must render the pinned context's track record: %s", raw)
	}
	// A refusal must still mutate nothing.
	if claim, _ := process.LoadRuntimeState("urlgame", dir); claim == nil || claim.Operation != nil {
		t.Fatalf("a refusal must not write an operation: %+v", claim)
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

func seedTrackClaim(t *testing.T, gameID, dir string) string {
	t.Helper()
	spec := process.LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	if err := process.ClaimRuntimeState(gameID, dir, st); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	return st.LaunchID
}
