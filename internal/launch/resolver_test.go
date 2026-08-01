package launch

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pardeike/gabs/internal/config"
)

func snapWith(g config.GameConfig) *config.Snapshot {
	return &config.Snapshot{
		Config:   &config.GamesConfig{Version: "1.0", Games: map[string]config.GameConfig{g.ID: g}},
		Revision: "sha256:abcdef123456",
	}
}

func testGame() config.GameConfig {
	return config.GameConfig{
		ID: "adventure", Name: "Adventure", LaunchMode: "DirectPath", Target: "/opt/adventure",
		Args:           []string{"--bridge"},
		Env:            map[string]string{"LOG_FORMAT": "json"},
		UnsetEnv:       []string{"HOST_OVERRIDE"},
		DefaultProfile: "vanilla",
		Profiles: map[string]config.ProfileConfig{
			"vanilla": {Args: []string{"--data-root", "/srv/v"}},
			"combat": {
				Args:       []string{"--data-root", "/srv/c"},
				Env:        map[string]string{"CONTENT_SET": "combat"},
				UnsetEnv:   []string{"LOG_FORMAT"},
				WorkingDir: "/srv/c",
			},
		},
		LaunchInputs: map[string]config.LaunchInputConfig{
			"quickStart": {Description: "d", Type: "boolean", Args: []string{"--quick-start"}},
			"scenario": {Description: "d", Type: "string", Enum: []string{"arena", "tutorial"},
				Profiles: []string{"combat"}, Args: []string{"--scenario", "${value}"},
				Env: map[string]string{"START_SCENARIO": "${value}"}},
			"seed": {Description: "d", Type: "integer", Minimum: i64(0), Maximum: i64(1 << 60),
				Args: []string{"--seed=${value}"}},
			"note": {Description: "d", Type: "string", MaxLength: iptr(5), Args: []string{"--note", "${value}"}},
			"tag":  {Description: "d", Type: "string", Pattern: "[a-z]+", Args: []string{"--tag", "${value}"}},
		},
	}
}

func i64(v int64) *int64 { return &v }
func iptr(v int) *int    { return &v }

var baseEnv = []string{"PATH=/usr/bin", "HOME=/home/u", "HOST_OVERRIDE=1", "GABS_OLD=x", "GABP_TOKEN=stale"}

func resolveOK(t *testing.T, req Request) *Resolved {
	t.Helper()
	r, rerr := Resolve(snapWith(testGame()), req, Options{InheritedEnv: baseEnv})
	if rerr != nil {
		t.Fatalf("unexpected resolve error: %+v", rerr)
	}
	return r
}

func resolveErr(t *testing.T, req Request) *ResolveError {
	t.Helper()
	_, rerr := Resolve(snapWith(testGame()), req, Options{InheritedEnv: baseEnv})
	if rerr == nil {
		t.Fatalf("expected resolve error")
	}
	return rerr
}

func TestDefaultProfileSelection(t *testing.T) {
	r := resolveOK(t, Request{GameID: "adventure"})
	if r.Profile != "vanilla" {
		t.Fatalf("expected default profile vanilla, got %q", r.Profile)
	}
	if r.ConfigRevision != "sha256:abcdef123456" {
		t.Fatalf("resolved spec must pin the snapshot revision")
	}
}

func TestExplicitProfileAndArgOrder(t *testing.T) {
	r := resolveOK(t, Request{GameID: "adventure", Profile: "combat", Inputs: map[string]any{
		"quickStart": true,
		"scenario":   "arena",
	}})
	// game args -> profile args -> input arg groups in lexical input-name order
	want := []string{"--bridge", "--data-root", "/srv/c", "--quick-start", "--scenario", "arena"}
	if !reflect.DeepEqual(r.Args, want) {
		t.Fatalf("arg order wrong:\n got %v\nwant %v", r.Args, want)
	}
	if !reflect.DeepEqual(r.AppliedInputs, []string{"quickStart", "scenario"}) {
		t.Fatalf("applied inputs wrong: %v", r.AppliedInputs)
	}
	if r.WorkingDir != "/srv/c" {
		t.Fatalf("profile workingDir must override, got %q", r.WorkingDir)
	}
}

func TestEnvPrecedenceAndSanitization(t *testing.T) {
	r := resolveOK(t, Request{GameID: "adventure", Profile: "combat", Inputs: map[string]any{"scenario": "tutorial"}})
	// inherited GABS_*/GABP_* stripped
	for _, k := range []string{"GABS_OLD", "GABP_TOKEN"} {
		if _, ok := r.Env[k]; ok {
			t.Fatalf("inherited %s must be stripped", k)
		}
	}
	// inherited preserved
	if r.Env["HOME"] != "/home/u" {
		t.Fatalf("inherited HOME lost")
	}
	// game unsetEnv removes inherited key entirely (absent, not empty)
	if _, ok := r.Env["HOST_OVERRIDE"]; ok {
		t.Fatalf("HOST_OVERRIDE must be absent")
	}
	// profile unsetEnv removes game env key
	if _, ok := r.Env["LOG_FORMAT"]; ok {
		t.Fatalf("profile unsetEnv must remove game env LOG_FORMAT")
	}
	if r.Env["CONTENT_SET"] != "combat" || r.Env["START_SCENARIO"] != "tutorial" {
		t.Fatalf("profile/input env not applied: %v", r.Env)
	}
	// absent names reported for GABS_ABSENT_ENV
	if !reflect.DeepEqual(r.AbsentEnvNames, []string{"HOST_OVERRIDE", "LOG_FORMAT"}) {
		t.Fatalf("absent names wrong: %v", r.AbsentEnvNames)
	}
	// context keys reported for GABS_FORWARD_ENV: game+profile+applied input env keys
	if !reflect.DeepEqual(r.ContextEnvKeys, []string{"CONTENT_SET", "LOG_FORMAT", "START_SCENARIO"}) {
		t.Fatalf("context keys wrong: %v", r.ContextEnvKeys)
	}
}

func TestUnsetThenSetSameKeyAcrossLayers(t *testing.T) {
	// profile unsets LOG_FORMAT; a later layer (input) sets it again ->
	// present, and NOT in AbsentEnvNames
	g := testGame()
	g.LaunchInputs["logfmt"] = config.LaunchInputConfig{
		Description: "d", Type: "string", Env: map[string]string{"LOG_FORMAT": "${value}"},
	}
	r, rerr := Resolve(snapWith(g), Request{GameID: "adventure", Profile: "combat",
		Inputs: map[string]any{"logfmt": "text"}}, Options{InheritedEnv: baseEnv})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if r.Env["LOG_FORMAT"] != "text" {
		t.Fatalf("later layer must win: %v", r.Env)
	}
	for _, n := range r.AbsentEnvNames {
		if n == "LOG_FORMAT" {
			t.Fatalf("re-set key must not be reported absent")
		}
	}
}

func TestCaseInsensitiveEnvMerge(t *testing.T) {
	g := testGame()
	g.Env = map[string]string{"MYVAR": "new"}
	g.UnsetEnv = nil
	r, rerr := Resolve(snapWith(g), Request{GameID: "adventure"},
		Options{InheritedEnv: []string{"MyVar=old"}, CaseInsensitiveEnv: true})
	if rerr != nil {
		t.Fatal(rerr)
	}
	count := 0
	for k, v := range r.Env {
		if k == "MYVAR" || k == "MyVar" {
			count++
			if v != "new" {
				t.Fatalf("config layer must override inherited case-variant, got %q", v)
			}
		}
	}
	if count != 1 {
		t.Fatalf("case-insensitive merge must keep exactly one variant, got %d", count)
	}
}

func TestBooleanFalseEqualsOmission(t *testing.T) {
	r := resolveOK(t, Request{GameID: "adventure", Inputs: map[string]any{"quickStart": false}})
	for _, a := range r.Args {
		if a == "--quick-start" {
			t.Fatalf("false boolean must not apply bindings")
		}
	}
	if len(r.AppliedInputs) != 0 {
		t.Fatalf("false boolean must not count as applied: %v", r.AppliedInputs)
	}
}

func TestValueStaysSingleArgvElement(t *testing.T) {
	g := testGame()
	g.LaunchInputs["free"] = config.LaunchInputConfig{Description: "d", Type: "string", Args: []string{"--free", "${value}"}}
	r, rerr := Resolve(snapWith(g), Request{GameID: "adventure",
		Inputs: map[string]any{"free": "two words; $(rm -rf)"}}, Options{InheritedEnv: baseEnv})
	if rerr != nil {
		t.Fatal(rerr)
	}
	last := r.Args[len(r.Args)-1]
	if last != "two words; $(rm -rf)" {
		t.Fatalf("value must remain literal data in one argv element, got %q", last)
	}
}

func TestErrors(t *testing.T) {
	if e := resolveErr(t, Request{GameID: "nope"}); e.Code != "game_not_found" {
		t.Fatalf("want game_not_found, got %s", e.Code)
	}
	if e := resolveErr(t, Request{GameID: "adventure", Profile: "nope"}); e.Code != "profile_not_found" {
		t.Fatalf("want profile_not_found, got %s", e.Code)
	} else if !reflect.DeepEqual(e.Candidates, []string{"combat", "vanilla"}) {
		t.Fatalf("candidates must be sorted: %v", e.Candidates)
	}
	if e := resolveErr(t, Request{GameID: "adventure", Inputs: map[string]any{"bogus": true}}); e.Code != "launch_input_not_declared" {
		t.Fatalf("want launch_input_not_declared, got %s", e.Code)
	}
	// input not applicable to selected profile
	if e := resolveErr(t, Request{GameID: "adventure", Profile: "vanilla", Inputs: map[string]any{"scenario": "arena"}}); e.Code != "launch_input_invalid" {
		t.Fatalf("want launch_input_invalid for inapplicable input, got %s", e.Code)
	}
	// unprofiled game with requested profile
	g := config.GameConfig{ID: "plain", Name: "P", LaunchMode: "DirectPath", Target: "/x"}
	_, e := Resolve(snapWith(g), Request{GameID: "plain", Profile: "p"}, Options{InheritedEnv: baseEnv})
	if e == nil || e.Code != "profiles_not_configured" {
		t.Fatalf("want profiles_not_configured, got %+v", e)
	}
}

func TestInputValueValidation(t *testing.T) {
	cases := []struct {
		name   string
		inputs map[string]any
		code   string
	}{
		{"enum violation", map[string]any{"scenario": "bogus"}, "launch_input_invalid"},
		{"wrong type bool", map[string]any{"quickStart": "yes"}, "launch_input_invalid"},
		{"float rejected", map[string]any{"seed": float64(9007199254740993)}, "launch_input_invalid"},
		{"below minimum", map[string]any{"seed": json.Number("-1")}, "launch_input_invalid"},
		{"non-integral number", map[string]any{"seed": json.Number("1.5")}, "launch_input_invalid"},
		{"maxLength code points", map[string]any{"note": "éééééé"}, "launch_input_invalid"}, // 6 code points > 5
		{"NUL rejected", map[string]any{"note": "a\x00"}, "launch_input_invalid"},
		{"invalid UTF-8", map[string]any{"note": string([]byte{0xff, 0xfe})}, "launch_input_invalid"},
		{"pattern full match", map[string]any{"tag": "abc1"}, "launch_input_invalid"}, // substring "abc" matches, full string must not
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := Request{GameID: "adventure", Inputs: c.inputs}
			if _, ok := c.inputs["scenario"]; ok {
				req.Profile = "combat"
			}
			e := resolveErr(t, req)
			if e.Code != c.code {
				t.Fatalf("want %s, got %s (%s)", c.code, e.Code, e.Message)
			}
		})
	}

	// valid values pass
	r := resolveOK(t, Request{GameID: "adventure", Inputs: map[string]any{
		"seed": json.Number("9007199254740993"), // 2^53+1: exact via json.Number
		"note": "ééééé",                         // exactly 5 code points
		"tag":  "abc",
	}})
	found := false
	for _, a := range r.Args {
		if a == "--seed=9007199254740993" {
			found = true
		}
	}
	if !found {
		t.Fatalf("canonical integer substitution missing: %v", r.Args)
	}
}

func TestLifecycleResolution(t *testing.T) {
	g := testGame()
	g.Lifecycle = &config.LifecycleConfig{
		Status: &config.HookConfig{Command: "checker", Args: []string{"is-running", "${gameId}-${profile}"}},
		Stop:   &config.HookConfig{Command: "stopper", Args: []string{"${profile}"}, TimeoutSeconds: iptr(60)},
	}
	p := g.Profiles["combat"]
	p.Lifecycle = &config.LifecycleConfig{
		Status: &config.HookConfig{Command: "combat-checker", Args: []string{"${profile}"},
			RunningExitCodes: []int{0, 3}, StoppedExitCodes: []int{1}},
	}
	g.Profiles["combat"] = p

	r, rerr := Resolve(snapWith(g), Request{GameID: "adventure", Profile: "combat"}, Options{InheritedEnv: baseEnv})
	if rerr != nil {
		t.Fatal(rerr)
	}
	lc := r.Lifecycle
	if lc == nil {
		t.Fatal("expected resolved lifecycle")
	}
	// profile override replaces the complete hook (no field merge)
	if lc.Status.Command != "combat-checker" || !reflect.DeepEqual(lc.Status.Args, []string{"combat"}) {
		t.Fatalf("profile status override wrong: %+v", lc.Status)
	}
	if !reflect.DeepEqual(lc.Status.RunningExitCodes, []int{0, 3}) || !reflect.DeepEqual(lc.Status.StoppedExitCodes, []int{1}) {
		t.Fatalf("custom exit codes lost: %+v", lc.Status)
	}
	// game-level stop hook inherited with placeholder substitution + defaults
	if lc.Stop.Command != "stopper" || !reflect.DeepEqual(lc.Stop.Args, []string{"combat"}) {
		t.Fatalf("stop hook wrong: %+v", lc.Stop)
	}
	if lc.Stop.TimeoutSeconds != 60 || lc.Stop.VerifyTimeoutSeconds != config.VerifyTimeoutDefault {
		t.Fatalf("stop timeouts wrong: %+v", lc.Stop)
	}
	// status defaults
	if lc.Status.TimeoutSeconds != config.StatusHookTimeoutDefault {
		t.Fatalf("status timeout default wrong: %+v", lc.Status)
	}
	// kill absent -> nil (built-in fallback applies)
	if lc.Kill != nil {
		t.Fatalf("kill must be nil when not configured")
	}
	// ${gameId} substitution in game-level hook for vanilla profile
	r2, _ := Resolve(snapWith(g), Request{GameID: "adventure", Profile: "vanilla"}, Options{InheritedEnv: baseEnv})
	if !reflect.DeepEqual(r2.Lifecycle.Status.Args, []string{"is-running", "adventure-vanilla"}) {
		t.Fatalf("placeholder substitution wrong: %v", r2.Lifecycle.Status.Args)
	}
}

func TestStatusExitCodeDefaults(t *testing.T) {
	g := testGame()
	g.Lifecycle = &config.LifecycleConfig{Status: &config.HookConfig{Command: "c"}}
	r, _ := Resolve(snapWith(g), Request{GameID: "adventure"}, Options{InheritedEnv: baseEnv})
	if !reflect.DeepEqual(r.Lifecycle.Status.RunningExitCodes, []int{0}) ||
		!reflect.DeepEqual(r.Lifecycle.Status.StoppedExitCodes, []int{1}) {
		t.Fatalf("default exit-code sets wrong: %+v", r.Lifecycle.Status)
	}
}

func TestDeepCopyImmutability(t *testing.T) {
	snap := snapWith(testGame())
	r1, _ := Resolve(snap, Request{GameID: "adventure"}, Options{InheritedEnv: baseEnv})
	// mutate everything reachable
	r1.Args[0] = "MUTATED"
	r1.Env["INJECTED"] = "x"
	r2, _ := Resolve(snap, Request{GameID: "adventure"}, Options{InheritedEnv: baseEnv})
	if r2.Args[0] == "MUTATED" {
		t.Fatalf("resolved args must be deep-copied")
	}
	if _, ok := r2.Env["INJECTED"]; ok {
		t.Fatalf("resolved env must be deep-copied")
	}
	// snapshot config untouched
	if snap.Config.Games["adventure"].Args[0] != "--bridge" {
		t.Fatalf("snapshot mutated by resolution")
	}
}

func TestUnprofiledLegacyGame(t *testing.T) {
	g := config.GameConfig{ID: "plain", Name: "P", LaunchMode: "DirectPath", Target: "/x",
		Args: []string{"-a"}, WorkingDir: "/w"}
	r, rerr := Resolve(snapWith(g), Request{GameID: "plain"}, Options{InheritedEnv: baseEnv})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if r.Profile != "" || r.WorkingDir != "/w" || !reflect.DeepEqual(r.Args, []string{"-a"}) {
		t.Fatalf("legacy resolution changed: %+v", r)
	}
	if len(r.ContextEnvKeys) != 0 || len(r.AbsentEnvNames) != 0 {
		t.Fatalf("legacy game must have empty context/absent key lists")
	}
}

func TestFalseBooleanBeforeApplicability(t *testing.T) {
	// A profile-restricted boolean supplied as false while another profile is
	// selected equals omission — it must not fail applicability.
	g := testGame()
	g.LaunchInputs["combatOnly"] = config.LaunchInputConfig{
		Description: "d", Type: "boolean", Profiles: []string{"combat"}, Args: []string{"--combat-only"},
	}
	r, rerr := Resolve(snapWith(g), Request{GameID: "adventure", Profile: "vanilla",
		Inputs: map[string]any{"combatOnly": false}}, Options{InheritedEnv: baseEnv})
	if rerr != nil {
		t.Fatalf("false boolean must equal omission before applicability: %+v", rerr)
	}
	if len(r.AppliedInputs) != 0 {
		t.Fatalf("nothing must apply: %v", r.AppliedInputs)
	}
	// true still fails applicability
	_, rerr = Resolve(snapWith(g), Request{GameID: "adventure", Profile: "vanilla",
		Inputs: map[string]any{"combatOnly": true}}, Options{InheritedEnv: baseEnv})
	if rerr == nil || rerr.Code != "launch_input_invalid" {
		t.Fatalf("true must still fail applicability, got %+v", rerr)
	}
}

func TestForwardingMetadataFoldsOnWindowsSemantics(t *testing.T) {
	// Game-level FOO + profile-level Foo on case-insensitive merge: the
	// forwarding metadata must list exactly one effective name (last-writer
	// spelling), matching envMap semantics.
	g := testGame()
	g.Env = map[string]string{"FOO": "game"}
	g.UnsetEnv = nil
	p := g.Profiles["combat"]
	p.Env = map[string]string{"Foo": "profile"}
	p.UnsetEnv = nil
	g.Profiles["combat"] = p
	r, rerr := Resolve(snapWith(g), Request{GameID: "adventure", Profile: "combat"},
		Options{InheritedEnv: nil, CaseInsensitiveEnv: true})
	if rerr != nil {
		t.Fatal(rerr)
	}
	count := 0
	for _, k := range r.ContextEnvKeys {
		if k == "FOO" || k == "Foo" {
			count++
			if k != "Foo" {
				t.Fatalf("last-writer spelling must win in ContextEnvKeys, got %q", k)
			}
		}
	}
	if count != 1 {
		t.Fatalf("fold-aware metadata must list one variant, got %d in %v", count, r.ContextEnvKeys)
	}
	// absences fold the same way: game unsets BAR, profile unsets bar
	g.UnsetEnv = []string{"BAR"}
	p = g.Profiles["combat"]
	p.UnsetEnv = []string{"bar"}
	g.Profiles["combat"] = p
	r, rerr = Resolve(snapWith(g), Request{GameID: "adventure", Profile: "combat"},
		Options{InheritedEnv: []string{"BAR=x"}, CaseInsensitiveEnv: true})
	if rerr != nil {
		t.Fatal(rerr)
	}
	count = 0
	for _, k := range r.AbsentEnvNames {
		if k == "BAR" || k == "bar" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("fold-aware absences must list one variant, got %d in %v", count, r.AbsentEnvNames)
	}
}
