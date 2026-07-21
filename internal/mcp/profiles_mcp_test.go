package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
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
	s := NewServerForTesting(util.NewLogger("error"))
	s.SetConfigDir(t.TempDir())
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
	s := NewServerForTesting(util.NewLogger("error"))
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

	s := NewServerForTesting(util.NewLogger("error"))
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
