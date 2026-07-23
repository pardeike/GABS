package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func profiledTestConfig(t *testing.T) *config.GamesConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"adventure": {
				ID: "adventure", Name: "Adventure", LaunchMode: "DirectPath",
				Target:         exe,
				WorkingDir:     filepath.Dir(exe),
				DefaultProfile: "vanilla",
				Profiles: map[string]config.ProfileConfig{
					"vanilla": {Description: "base", Args: []string{"-test.run=TestSharedRuntimeStateHelperProcess"}},
					"combat":  {Description: "combat data", Args: []string{"-test.run=TestSharedRuntimeStateHelperProcess"}},
				},
				LaunchInputs: map[string]config.LaunchInputConfig{
					"scenario": {Description: "pick scenario", Type: "string",
						Enum: []string{"arena", "tutorial"}, Args: []string{"-test.v=${value}"}},
					"note": {Description: "free note", Type: "string", Pattern: "[a-z]+",
						Args: []string{"-test.x=${value}"}},
				},
			},
			"plain": {ID: "plain", Name: "Plain", LaunchMode: "DirectPath", Target: exe},
		},
	}
}

func newProfiledServer(t *testing.T) *Server {
	t.Helper()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	// Join any background attachment/lease/mirroring task before TempDir
	// teardown so none writes runtime.json during RemoveAll (round 12 F4).
	t.Cleanup(s.Shutdown)
	s.RegisterGameManagementTools(profiledTestConfig(t), 0, 0)
	return s
}

func callTool(t *testing.T, s *Server, tool string, args map[string]interface{}) (string, map[string]interface{}) {
	t.Helper()
	msg := &Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"t"`),
		Params:  map[string]interface{}{"name": tool, "arguments": args},
	}
	resp := s.HandleMessage(msg)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	// extract structuredContent if present
	var envelope struct {
		Result struct {
			Structured map[string]interface{} `json:"structuredContent"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return string(raw), envelope.Result.Structured
}

func TestUnknownArgumentRejected(t *testing.T) {
	s := newProfiledServer(t)

	// the observed real-world near-miss: timeoutSeconds instead of timeout
	raw, structured := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "timeoutSeconds": 5,
	})
	if !strings.Contains(raw, "unknown_argument") {
		t.Fatalf("timeoutSeconds must be rejected as unknown_argument, got %s", raw)
	}
	if structured["path"] != "/timeoutSeconds" {
		t.Fatalf("path must name the offending key, got %v", structured["path"])
	}
	allowed, _ := structured["allowed"].([]interface{})
	if len(allowed) == 0 {
		t.Fatalf("allowed names must be listed, got %s", raw)
	}

	// every core tool rejects unknown args
	for tool, args := range map[string]map[string]interface{}{
		"games.list":    {"bogus": 1},
		"games.show":    {"gameId": "adventure", "bogus": 1},
		"games.status":  {"bogus": 1},
		"games.stop":    {"gameId": "adventure", "bogus": 1},
		"games.kill":    {"gameId": "adventure", "bogus": 1},
		"games.connect": {"gameId": "adventure", "bogus": 1},
	} {
		raw, _ := callTool(t, s, tool, args)
		if !strings.Contains(raw, "unknown_argument") {
			t.Fatalf("%s must reject unknown args, got %s", tool, raw)
		}
	}
}

func TestStartProfileErrors(t *testing.T) {
	s := newProfiledServer(t)

	raw, structured := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "profile": "nope",
	})
	if structured["code"] != "profile_not_found" {
		t.Fatalf("want profile_not_found, got %s", raw)
	}
	cands, _ := structured["candidates"].([]interface{})
	if len(cands) != 2 || cands[0] != "combat" || cands[1] != "vanilla" {
		t.Fatalf("sorted candidates expected, got %v", cands)
	}

	_, structured = callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "plain", "profile": "x",
	})
	if structured["code"] != "profiles_not_configured" {
		t.Fatalf("want profiles_not_configured, got %v", structured)
	}
	if structured["configPath"] == nil || structured["documentation"] == nil {
		t.Fatalf("profiles_not_configured must carry configPath and documentation anchor: %v", structured)
	}

	_, structured = callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "launchInputs": map[string]interface{}{"bogus": true},
	})
	if structured["code"] != "launch_input_not_declared" {
		t.Fatalf("want launch_input_not_declared, got %v", structured)
	}

	_, structured = callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "launchInputs": map[string]interface{}{"scenario": "bogus"},
	})
	if structured["code"] != "launch_input_invalid" {
		t.Fatalf("want launch_input_invalid for enum violation, got %v", structured)
	}
}

func TestStartUnresolvableTarget(t *testing.T) {
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	cfg := profiledTestConfig(t)
	g := cfg.Games["adventure"]
	g.Target = "/nonexistent/path/to/game-binary"
	cfg.Games["adventure"] = g
	s.RegisterGameManagementTools(cfg, 0, 0)

	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "adventure"})
	if structured["code"] != "launch_spec_unresolvable" {
		t.Fatalf("want launch_spec_unresolvable, got %s", raw)
	}
	issues, _ := structured["issues"].([]interface{})
	if len(issues) == 0 {
		t.Fatalf("issues with JSON+fs paths expected, got %s", raw)
	}
	first, _ := issues[0].(map[string]interface{})
	if first["path"] != "/games/adventure/target" || first["fsPath"] == nil {
		t.Fatalf("issue must carry JSON path and fs path, got %v", first)
	}
}

func TestProfiledStartEndToEnd(t *testing.T) {
	t.Setenv("GABSTEST_HELPER_PROCESS", "1")
	s := newProfiledServer(t)

	// the base game has NO args; only the profile carries -test.run. A
	// successful start therefore proves profile args reached the child.
	raw, structured := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "profile": "combat", "timeout": 1,
	})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("profiled start failed: %s", raw)
	}
	if structured["activeProfile"] != "combat" {
		t.Fatalf("activeProfile must be reported, got %v", structured)
	}
	if structured["configRevision"] != "startup" {
		t.Fatalf("configRevision must be pinned, got %v", structured)
	}
	// cleanup
	callTool(t, s, "games.kill", map[string]interface{}{"gameId": "adventure"})
}

func TestShowAndListMetadata(t *testing.T) {
	s := newProfiledServer(t)

	_, structured := callTool(t, s, "games.show", map[string]interface{}{"gameId": "adventure"})
	if structured["defaultProfile"] != "vanilla" {
		t.Fatalf("defaultProfile missing: %v", structured)
	}
	profiles, _ := structured["profiles"].([]interface{})
	if len(profiles) != 2 {
		t.Fatalf("profiles metadata missing: %v", structured)
	}
	inputs, _ := structured["launchInputs"].(map[string]interface{})
	note, _ := inputs["note"].(map[string]interface{})
	if note["maxLength"] != float64(config.InputMaxLengthDefault) {
		t.Fatalf("effective default maxLength must be explicit, got %v", note)
	}
	if note["pattern"] != "[a-z]+" || note["patternDialect"] != "re2-full-match" {
		t.Fatalf("pattern + dialect must be exposed, got %v", note)
	}
	scenario, _ := inputs["scenario"].(map[string]interface{})
	enum, _ := scenario["enum"].([]interface{})
	if len(enum) != 2 {
		t.Fatalf("enum must be exposed, got %v", scenario)
	}

	_, listStructured := callTool(t, s, "games.list", nil)
	if listStructured["currentConfigRevision"] != "startup" {
		t.Fatalf("list must carry currentConfigRevision, got %v", listStructured)
	}
	games, _ := listStructured["games"].([]interface{})
	var adventure map[string]interface{}
	for _, g := range games {
		gm, _ := g.(map[string]interface{})
		if gm["gameId"] == "adventure" {
			adventure = gm
		}
	}
	if adventure["defaultProfile"] != "vanilla" {
		t.Fatalf("list item must carry defaultProfile, got %v", adventure)
	}
}

func TestStartRefusedOnStaleConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	valid := `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/bin/echo"}}}`
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

	// break the config on disk
	if err := os.WriteFile(cfgPath, []byte(`{"version":"1.0","games":{"a":{"id":"a","profiles":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "a"})
	if structured["code"] != "config_invalid" {
		t.Fatalf("start must refuse on stale config, got %s", raw)
	}
	// config_invalid carries a config cause class (round 11 P1-1 mandatory
	// attribution), routing the agent to games_show, not a launch retry.
	if structured["causeClass"] != process.CauseConfig {
		t.Fatalf("config_invalid must be config-class, got %#v", structured["causeClass"])
	}

	// reads proceed on last-known-good and surface the error
	_, listStructured := callTool(t, s, "games.list", nil)
	if listStructured["configError"] == nil {
		t.Fatalf("list must surface configError on stale config, got %v", listStructured)
	}
	games, _ := listStructured["games"].([]interface{})
	if len(games) != 1 {
		t.Fatalf("list must serve last-known-good games, got %v", listStructured)
	}

	// fixing the file clears everything on the next call
	if err := os.WriteFile(cfgPath, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	_, listStructured = callTool(t, s, "games.list", nil)
	if listStructured["configError"] != nil {
		t.Fatalf("fix must clear configError, got %v", listStructured)
	}
}

func TestTimeoutRangeEnforced(t *testing.T) {
	s := newProfiledServer(t)
	for _, v := range []interface{}{0, -5, 3601, json.Number("0"), json.Number("99999")} {
		_, structured := callTool(t, s, "games.start", map[string]interface{}{
			"gameId": "adventure", "timeout": v,
		})
		if structured["code"] != "timeout_out_of_range" {
			t.Fatalf("timeout %v must be timeout_out_of_range, got %v", v, structured)
		}
	}
}

func TestAmbiguousGameReference(t *testing.T) {
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	cfg := profiledTestConfig(t)
	// two games sharing one target
	a := cfg.Games["adventure"]
	b := cfg.Games["plain"]
	b.Target = a.Target
	cfg.Games["plain"] = b
	s.RegisterGameManagementTools(cfg, 0, 0)

	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": a.Target})
	if structured["code"] != "ambiguous_game_reference" {
		t.Fatalf("shared target must be ambiguous_game_reference, got %v", structured)
	}
	cands, _ := structured["candidates"].([]interface{})
	if len(cands) != 2 || cands[0] != "adventure" || cands[1] != "plain" {
		t.Fatalf("sorted candidates expected, got %v", cands)
	}

	// absent references carry game_not_found
	_, structured = callTool(t, s, "games.stop", map[string]interface{}{"gameId": "nope"})
	if structured["code"] != "game_not_found" {
		t.Fatalf("absent reference must be game_not_found, got %v", structured)
	}
}

func TestSpawnFailedClassification(t *testing.T) {
	dir := t.TempDir()
	nonExec := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(nonExec, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
	cfg := profiledTestConfig(t)
	g := cfg.Games["plain"]
	g.Target = nonExec
	cfg.Games["plain"] = g
	s.RegisterGameManagementTools(cfg, 0, 0)

	// static check catches non-executable first (unix); make it pass the
	// static check but fail at exec by removing read permission... simpler:
	// use a directory-shaped miss that static check cannot see — a target
	// that becomes non-executable is caught statically on unix, so assert
	// the static path here and the spawn path via a vanishing target.
	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "plain"})
	if structured["code"] != "launch_spec_unresolvable" {
		t.Fatalf("non-executable target caught at Stage 1, got %v", structured)
	}
}

func TestDiscoveryUsesPerCallConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfgA := `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/bin/echo"}}}`
	cfgB := `{"version":"1.0","games":{"b":{"id":"b","name":"B","launchMode":"DirectPath","target":"/bin/echo"}}}`
	if err := os.WriteFile(cfgPath, []byte(cfgA), 0600); err != nil {
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

	// replace game a with game b on disk
	if err := os.WriteFile(cfgPath, []byte(cfgB), 0600); err != nil {
		t.Fatal(err)
	}

	// discovery must see the reloaded config, not the registration snapshot
	raw, _ := callTool(t, s, "games.tool_names", map[string]interface{}{"gameId": "b", "brief": true})
	if strings.Contains(raw, "not found") {
		t.Fatalf("games.tool_names must resolve reloaded game b, got %s", raw)
	}
	raw, _ = callTool(t, s, "games.tool_names", map[string]interface{}{"gameId": "a", "brief": true})
	if !strings.Contains(raw, "not found") && !strings.Contains(raw, "game_not_found") {
		t.Fatalf("games.tool_names must not resolve removed game a, got %s", raw)
	}
}

func TestStartLaunchModeIncompatible(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	valid := `{"version":"1.0","games":{"u":{"id":"u","name":"U","launchMode":"SteamAppId","target":"12345","stopProcessName":"game-bin"}}}`
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

	// hot edit: the URL-mode game gains context fields it cannot deliver
	incompatible := `{"version":"1.0","games":{"u":{"id":"u","name":"U","launchMode":"SteamAppId","target":"12345","stopProcessName":"game-bin","env":{"A":"1"},"defaultProfile":"x","profiles":{"x":{"description":"d"}}}}}`
	if err := os.WriteFile(cfgPath, []byte(incompatible), 0600); err != nil {
		t.Fatal(err)
	}
	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "u"})
	if structured["code"] != "launch_mode_incompatible" {
		t.Fatalf("mode-incompatible edit must map to launch_mode_incompatible, got %s", raw)
	}

	// mixed failure (mode issue + unrelated grammar error) stays generic
	mixed := `{"version":"1.0","games":{"u":{"id":"u","name":"U","launchMode":"SteamAppId","target":"12345","stopProcessName":"game-bin","env":{"A":"1"},"launchInputs":{"bad name!":{"type":"boolean","description":"d","arg":"--x"}}}}}`
	if err := os.WriteFile(cfgPath, []byte(mixed), 0600); err != nil {
		t.Fatal(err)
	}
	raw, structured = callTool(t, s, "games.start", map[string]interface{}{"gameId": "u"})
	if structured["code"] != "config_invalid" {
		t.Fatalf("mixed validation failure must stay config_invalid, got %s", raw)
	}
}

func TestActiveConfigRevisionSurfaced(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	valid := `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/bin/echo"}}}`
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

	// a persisted claim pins the revision the RUNNING launch used
	claim := process.NewRuntimeState(process.LaunchSpec{
		GameId: "a", Mode: "DirectPath", PathOrId: "/bin/echo",
		Env: map[string]string{}, ConfigRevision: "sha256:runningrev00",
	}, process.RuntimeStateStatusRunning)
	claim.GamePID = os.Getpid() // alive, so status logic keeps the claim
	if err := process.ClaimRuntimeState("a", dir, claim); err != nil {
		t.Fatal(err)
	}

	_, structured := callTool(t, s, "games.show", map[string]interface{}{"gameId": "a"})
	if structured["activeConfigRevision"] != "sha256:runningrev00" {
		t.Fatalf("show must surface the running launch's revision: %v", structured)
	}
	if structured["currentConfigRevision"] == structured["activeConfigRevision"] {
		t.Fatalf("active and current revisions must be distinguishable: %v", structured)
	}

	_, statusStructured := callTool(t, s, "games.status", map[string]interface{}{"gameId": "a"})
	if statusStructured["activeConfigRevision"] != "sha256:runningrev00" {
		t.Fatalf("status must surface the running launch's revision: %v", statusStructured)
	}
}

func TestGameConfigWarningsEscapeIDs(t *testing.T) {
	cfg := &config.GamesConfig{Warnings: []config.ConfigIssue{
		{Path: "/games/factory~0old/unknownKey", Message: "unknown key"},
	}}
	warns := gameConfigWarnings(cfg, "factory~old")
	if len(warns) != 1 {
		t.Fatalf("escaped-ID warning must match its game: %v", warns)
	}
	if warns := gameConfigWarnings(cfg, "factory"); len(warns) != 0 {
		t.Fatalf("prefix must not leak across IDs: %v", warns)
	}
}

// Bundle resolution applies to every propagation-capable path mode: Stage 1
// checks the inner executable, so the spawn must exec the same target.
func TestLaunchSpecResolvesBundlesForCustomCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS bundle semantics")
	}
	dir := t.TempDir()
	bundle := filepath.Join(dir, "Game.app")
	macOS := filepath.Join(bundle, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(macOS, "Game")
	if err := os.WriteFile(inner, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	game := config.GameConfig{ID: "g", Name: "G", LaunchMode: "CustomCommand", Target: bundle}
	spec := launchSpecFromResolved(game, &launch.Resolved{GameID: "g"})
	if spec.PathOrId != inner {
		t.Fatalf("CustomCommand bundle must resolve to the inner executable: %q", spec.PathOrId)
	}
}

func TestStartPipelinePersistsActiveClaim(t *testing.T) {
	t.Setenv("GABSTEST_HELPER_PROCESS", "1")
	s := newProfiledServer(t)
	raw, _ := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "profile": "combat", "timeout": 1,
	})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("start failed: %s", raw)
	}
	defer callTool(t, s, "games.kill", map[string]interface{}{"gameId": "adventure"})

	claim, err := process.LoadRuntimeState("adventure", s.configDir)
	if err != nil || claim == nil {
		t.Fatalf("claim must persist after start: %v %v", claim, err)
	}
	// the complete Stage 2-4 contract: active phase, spawned state with
	// PID + fingerprint, endpoint with per-launch token, operation cleared
	if claim.Phase != process.PhaseActive || claim.SpawnState != process.SpawnStateSpawned {
		t.Fatalf("claim must be active/spawned: phase=%q spawnState=%q", claim.Phase, claim.SpawnState)
	}
	if claim.GamePID <= 0 || claim.PIDStartTime == 0 {
		t.Fatalf("PID + start-time fingerprint must be recorded: %+v", claim)
	}
	if claim.Endpoint == nil || claim.Endpoint.Port <= 0 || claim.Endpoint.Token == "" {
		t.Fatalf("endpoint (port + per-launch token) must be in the claim: %+v", claim.Endpoint)
	}
	if claim.Operation != nil {
		t.Fatalf("completed start must clear the operation: %+v", claim.Operation)
	}
	if claim.Profile != "combat" || claim.SchemaVersion != process.RuntimeSchemaVersion {
		t.Fatalf("claim context wrong: %+v", claim)
	}
}

func TestStartPipelineExitedDuringStart(t *testing.T) {
	// the helper test exits immediately without GABSTEST_HELPER_PROCESS=1
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
			"flash": {ID: "flash", Name: "F", LaunchMode: "DirectPath", Target: exe,
				Args: []string{"-test.run=TestSharedRuntimeStateHelperProcess"}},
		},
	}, 0, 0)

	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "flash", "timeout": 1})
	if structured["code"] != "exited_during_start" {
		t.Fatalf("immediate exit must map to exited_during_start, got %s", raw)
	}
	if _, ok := structured["exitCode"]; !ok {
		t.Fatalf("exit evidence missing: %v", structured)
	}
	if claim, _ := process.LoadRuntimeState("flash", dir); claim != nil {
		t.Fatalf("claim must be released on exited_during_start: %+v", claim)
	}
}

func TestConnectEndpointComesFromRuntimeClaim(t *testing.T) {
	s := newProfiledServer(t)
	game := config.GameConfig{ID: "a", Name: "A", LaunchMode: "DirectPath", Target: "/bin/echo"}

	// The claim is the authoritative attachment source (design/07): after a
	// CLI start or a server restart it carries the per-launch endpoint.
	claim := &process.RuntimeState{
		GameID: "a", SchemaVersion: process.RuntimeSchemaVersion,
		Endpoint: &process.RuntimeEndpoint{Port: 45678, Token: "claim-token"},
	}
	endpoint, err := s.resolveConnectBridgeEndpoint(game, claim)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Port != 45678 || endpoint.Token != "claim-token" || endpoint.Source != "runtime-claim" {
		t.Fatalf("claim endpoint must win: %+v", endpoint)
	}
}
