package config

import (
	"strings"
	"testing"
)

// helper: build a minimal profiled game
func profiledGame() GameConfig {
	return GameConfig{
		ID:             "adventure",
		Name:           "Adventure Game",
		LaunchMode:     "DirectPath",
		Target:         "/opt/example/adventure",
		DefaultProfile: "vanilla",
		Profiles: map[string]ProfileConfig{
			"vanilla":     {Description: "Untouched user data", Args: []string{"--data-root", "/srv/a/vanilla"}},
			"combat-test": {Description: "Combat test", Args: []string{"--data-root", "/srv/a/combat"}, Env: map[string]string{"CONTENT_SET": "combat"}, WorkingDir: "/srv/a/combat"},
		},
	}
}

func validateGame(t *testing.T, g GameConfig, opts ValidationOptions) (errs, warns []ConfigIssue) {
	t.Helper()
	return ValidateGameExtensions(g.ID, &g, opts)
}

func requireIssue(t *testing.T, issues []ConfigIssue, pathFrag, msgFrag string) {
	t.Helper()
	for _, is := range issues {
		if strings.Contains(is.Path, pathFrag) && strings.Contains(is.Message, msgFrag) {
			return
		}
	}
	t.Fatalf("expected issue with path containing %q and message containing %q, got: %v", pathFrag, msgFrag, issues)
}

func requireNoErrors(t *testing.T, errs []ConfigIssue) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got: %v", errs)
	}
}

func TestProfiledGameValid(t *testing.T) {
	errs, warns := validateGame(t, profiledGame(), ValidationOptions{})
	requireNoErrors(t, errs)
	if len(warns) != 0 {
		t.Fatalf("expected no warnings, got %v", warns)
	}
}

func TestLegacyGameNoNewValidation(t *testing.T) {
	g := GameConfig{ID: "factory", Name: "F", LaunchMode: "DirectPath", Target: "/x"}
	errs, warns := validateGame(t, g, ValidationOptions{})
	requireNoErrors(t, errs)
	if len(warns) != 0 {
		t.Fatalf("legacy game must be warning-free, got %v", warns)
	}
}

func TestDefaultProfileRequired(t *testing.T) {
	g := profiledGame()
	g.DefaultProfile = ""
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/defaultProfile", "required")
}

func TestDefaultProfileMustExist(t *testing.T) {
	g := profiledGame()
	g.DefaultProfile = "missing"
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/defaultProfile", "missing")
}

func TestDefaultProfileWithoutProfiles(t *testing.T) {
	g := profiledGame()
	g.Profiles = nil
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/defaultProfile", "profiles")
}

func TestProfileNameGrammar(t *testing.T) {
	g := profiledGame()
	g.Profiles["bad name!"] = ProfileConfig{}
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/profiles/bad name!", "name")
}

func TestEnvKeyGrammar(t *testing.T) {
	g := profiledGame()
	g.Env = map[string]string{"FEATURE,MODE": "x"}
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/env", "portable")
}

func TestReservedEnvPrefixes(t *testing.T) {
	for _, key := range []string{"GABS_THING", "gabp_thing", "GaBs_X"} {
		g := profiledGame()
		p := g.Profiles["vanilla"]
		p.Env = map[string]string{key: "v"}
		g.Profiles["vanilla"] = p
		errs, _ := validateGame(t, g, ValidationOptions{})
		requireIssue(t, errs, "/profiles/vanilla/env", "reserved")
	}
}

func TestUnsetEnvConflictSameLayer(t *testing.T) {
	g := profiledGame()
	g.Env = map[string]string{"FOO": "1"}
	g.UnsetEnv = []string{"FOO"}
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/unsetEnv", "both")
}

func TestUnsetEnvDuplicates(t *testing.T) {
	g := profiledGame()
	g.UnsetEnv = []string{"FOO", "FOO"}
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/unsetEnv", "duplicate")
}

func TestEnvCaseFoldCollision(t *testing.T) {
	g := profiledGame()
	g.Env = map[string]string{"Foo": "1", "FOO": "2"}
	errs, _ := validateGame(t, g, ValidationOptions{CaseInsensitiveEnv: true})
	requireIssue(t, errs, "/env", "case")
	// without case folding this is legal
	errs, _ = validateGame(t, g, ValidationOptions{CaseInsensitiveEnv: false})
	requireNoErrors(t, errs)
}

func TestNULInEnvValue(t *testing.T) {
	g := profiledGame()
	g.Env = map[string]string{"FOO": "a\x00b"}
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/env/FOO", "NUL")
}

func TestProfileWorkingDirMustBeAbsolute(t *testing.T) {
	g := profiledGame()
	p := g.Profiles["vanilla"]
	p.WorkingDir = "relative/path"
	g.Profiles["vanilla"] = p
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/profiles/vanilla/workingDir", "absolute")
}

// --- launch inputs ---

func inputGame(inputs map[string]LaunchInputConfig) GameConfig {
	g := profiledGame()
	g.LaunchInputs = inputs
	return g
}

func TestValidInputs(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{
		"quickStart": {Description: "d", Type: "boolean", Args: []string{"--quick-start"}},
		"scenario":   {Description: "d", Type: "string", Enum: []string{"arena", "tutorial"}, Profiles: []string{"combat-test"}, Args: []string{"--scenario", "${value}"}},
		"seed":       {Description: "d", Type: "integer", Minimum: i64(0), Maximum: i64(999999), Args: []string{"--seed", "${value}"}},
	})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireNoErrors(t, errs)
}

func i64(v int64) *int64 { return &v }
func iptr(v int) *int    { return &v }

func TestInputDescriptionRequired(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{"x": {Type: "boolean", Args: []string{"--x"}}})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/description", "required")
}

func TestInputTypeValidation(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "float", Args: []string{"${value}"}}})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/type", "boolean")
}

func TestInputRequiresBinding(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "boolean"}})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x", "binding")
}

func TestBooleanMustNotUseValuePlaceholder(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "boolean", Args: []string{"--x", "${value}"}}})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x", "${value}")
}

func TestStringMustUseValuePlaceholder(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "string", Args: []string{"--x"}}})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x", "${value}")
}

func TestStringConstraints(t *testing.T) {
	// invalid pattern
	g := inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "string", Pattern: "([", Args: []string{"${value}"}}})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/pattern", "pattern")

	// maxLength range
	g = inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "string", MaxLength: iptr(70000), Args: []string{"${value}"}}})
	errs, _ = validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/maxLength", "65536")

	// enum on integer is invalid
	g = inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "integer", Enum: []string{"1"}, Args: []string{"${value}"}}})
	errs, _ = validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/enum", "string")

	// min > max
	g = inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "integer", Minimum: i64(10), Maximum: i64(5), Args: []string{"${value}"}}})
	errs, _ = validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x", "minimum")

	// min/max on string is invalid
	g = inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "string", Minimum: i64(1), Args: []string{"${value}"}}})
	errs, _ = validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/minimum", "integer")
}

func TestInputProfileReferences(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "boolean", Profiles: []string{"nope"}, Args: []string{"--x"}}})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/profiles", "nope")

	g = inputGame(map[string]LaunchInputConfig{"x": {Description: "d", Type: "boolean", Profiles: []string{"vanilla", "vanilla"}, Args: []string{"--x"}}})
	errs, _ = validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs/x/profiles", "duplicate")
}

func TestInputEnvConflict(t *testing.T) {
	// both applicable everywhere -> conflict
	g := inputGame(map[string]LaunchInputConfig{
		"a": {Description: "d", Type: "boolean", Env: map[string]string{"SHARED": "1"}},
		"b": {Description: "d", Type: "boolean", Env: map[string]string{"SHARED": "2"}},
	})
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs", "SHARED")

	// disjoint profile applicability -> no conflict
	g = inputGame(map[string]LaunchInputConfig{
		"a": {Description: "d", Type: "boolean", Profiles: []string{"vanilla"}, Env: map[string]string{"SHARED": "1"}},
		"b": {Description: "d", Type: "boolean", Profiles: []string{"combat-test"}, Env: map[string]string{"SHARED": "2"}},
	})
	errs, _ = validateGame(t, g, ValidationOptions{})
	requireNoErrors(t, errs)
}

// --- URL modes ---

func TestURLModesRejectContext(t *testing.T) {
	g := profiledGame()
	g.LaunchMode = "SteamAppId"
	g.Target = "123456"
	g.StopProcessName = "Game.exe"
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/profiles", "SteamAppId")

	g2 := GameConfig{ID: "e", Name: "E", LaunchMode: "EpicAppId", Target: "x", StopProcessName: "E.exe",
		Env: map[string]string{"FOO": "1"}}
	errs, _ = validateGame(t, g2, ValidationOptions{})
	requireIssue(t, errs, "/env", "EpicAppId")

	g3 := GameConfig{ID: "e", Name: "E", LaunchMode: "EpicAppId", Target: "x", StopProcessName: "E.exe",
		UnsetEnv: []string{"FOO"}}
	errs, _ = validateGame(t, g3, ValidationOptions{})
	requireIssue(t, errs, "/unsetEnv", "EpicAppId")

	g4 := GameConfig{ID: "e", Name: "E", LaunchMode: "SteamAppId", Target: "1", StopProcessName: "E.exe",
		LaunchInputs: map[string]LaunchInputConfig{"x": {Description: "d", Type: "boolean", Args: []string{"--x"}}}}
	errs, _ = validateGame(t, g4, ValidationOptions{})
	requireIssue(t, errs, "/launchInputs", "SteamAppId")
}

// --- lifecycle feature gate (M1) ---

func TestLifecycleFeatureGate(t *testing.T) {
	g := profiledGame()
	g.Lifecycle = &LifecycleConfig{Status: &HookConfig{Command: "adventure-status", Args: []string{"${profile}"}}}
	errs, _ := validateGame(t, g, ValidationOptions{AllowLifecycle: false})
	requireIssue(t, errs, "/lifecycle", "not yet supported")

	// profile-level lifecycle is gated too
	g = profiledGame()
	p := g.Profiles["vanilla"]
	p.Lifecycle = &LifecycleConfig{Stop: &HookConfig{Command: "stopper"}}
	g.Profiles["vanilla"] = p
	errs, _ = validateGame(t, g, ValidationOptions{AllowLifecycle: false})
	requireIssue(t, errs, "/profiles/vanilla/lifecycle", "not yet supported")
}

// --- lifecycle validation (unit-level, gate lifted) ---

func TestHookValidation(t *testing.T) {
	mk := func(h HookConfig, slot string) GameConfig {
		g := profiledGame()
		lc := &LifecycleConfig{}
		switch slot {
		case "status":
			lc.Status = &h
		case "stop":
			lc.Stop = &h
		case "kill":
			lc.Kill = &h
		}
		g.Lifecycle = lc
		return g
	}
	opts := ValidationOptions{AllowLifecycle: true}

	// valid status hook with placeholders
	errs, _ := validateGame(t, mk(HookConfig{Command: "checker", Args: []string{"${gameId}", "${profile}"}}, "status"), opts)
	requireNoErrors(t, errs)

	// command required
	errs, _ = validateGame(t, mk(HookConfig{Args: []string{"x"}}, "stop"), opts)
	requireIssue(t, errs, "/lifecycle/stop/command", "required")

	// unknown placeholder
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", Args: []string{"${bogus}"}}, "stop"), opts)
	requireIssue(t, errs, "/lifecycle/stop/args/0", "placeholder")

	// ${profile} invalid without profiles
	g := mk(HookConfig{Command: "c", Args: []string{"${profile}"}}, "status")
	g.Profiles = nil
	g.DefaultProfile = ""
	errs, _ = validateGame(t, g, opts)
	requireIssue(t, errs, "/lifecycle/status/args/0", "profile")

	// status timeout range 1-60
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", TimeoutSeconds: iptr(61)}, "status"), opts)
	requireIssue(t, errs, "/lifecycle/status/timeoutSeconds", "60")

	// stop timeout range 1-600
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", TimeoutSeconds: iptr(601)}, "stop"), opts)
	requireIssue(t, errs, "/lifecycle/stop/timeoutSeconds", "600")

	// verifyTimeoutSeconds only on stop/kill
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", VerifyTimeoutSeconds: iptr(15)}, "status"), opts)
	requireIssue(t, errs, "/lifecycle/status/verifyTimeoutSeconds", "status")

	// exit-code sets only on status
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", RunningExitCodes: []int{0}}, "kill"), opts)
	requireIssue(t, errs, "/lifecycle/kill/runningExitCodes", "status")

	// running/stopped sets must be disjoint and non-empty
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", RunningExitCodes: []int{0, 1}, StoppedExitCodes: []int{1}}, "status"), opts)
	requireIssue(t, errs, "/lifecycle/status", "disjoint")
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", RunningExitCodes: []int{}}, "status"), opts)
	requireIssue(t, errs, "/lifecycle/status/runningExitCodes", "empty")

	// hook workingDir absolute
	errs, _ = validateGame(t, mk(HookConfig{Command: "c", WorkingDir: "rel"}, "stop"), opts)
	requireIssue(t, errs, "/lifecycle/stop/workingDir", "absolute")

	// profile-level hook override is a complete hook (command required)
	g = profiledGame()
	p := g.Profiles["vanilla"]
	p.Lifecycle = &LifecycleConfig{Status: &HookConfig{}}
	g.Profiles["vanilla"] = p
	errs, _ = validateGame(t, g, opts)
	requireIssue(t, errs, "/profiles/vanilla/lifecycle/status/command", "required")
}

func TestAddGameRunsExtensionValidation(t *testing.T) {
	c := &GamesConfig{Version: "1.0"}
	g := profiledGame()
	g.DefaultProfile = "" // invalid: profiles without defaultProfile
	if err := c.AddGame(g); err == nil {
		t.Fatalf("AddGame must reject invalid extension config")
	}
	g = profiledGame()
	if err := c.AddGame(g); err != nil {
		t.Fatalf("valid profiled game must be addable: %v", err)
	}
}

func TestHookWorkingDirRelativeWithPlaceholder(t *testing.T) {
	g := profiledGame()
	g.Lifecycle = &LifecycleConfig{Stop: &HookConfig{Command: "c", WorkingDir: "relative/${profile}"}}
	errs, _ := validateGame(t, g, ValidationOptions{AllowLifecycle: true})
	requireIssue(t, errs, "/lifecycle/stop/workingDir", "absolute")
	// absolute literal with placeholder suffix stays valid
	g.Lifecycle = &LifecycleConfig{Stop: &HookConfig{Command: "c", WorkingDir: "/srv/${profile}"}}
	errs, _ = validateGame(t, g, ValidationOptions{AllowLifecycle: true})
	requireNoErrors(t, errs)
}

func TestInputEnvCaseFoldCollision(t *testing.T) {
	g := inputGame(map[string]LaunchInputConfig{
		"x": {Description: "d", Type: "boolean", Env: map[string]string{"Foo": "1", "FOO": "2"}},
	})
	errs, _ := validateGame(t, g, ValidationOptions{CaseInsensitiveEnv: true})
	requireIssue(t, errs, "/launchInputs/x/env", "case")
	errs, _ = validateGame(t, g, ValidationOptions{CaseInsensitiveEnv: false})
	requireNoErrors(t, errs)
}

func TestURLModeHookRelaxation(t *testing.T) {
	mk := func(lc *LifecycleConfig, stopName string) GameConfig {
		return GameConfig{ID: "u", Name: "U", LaunchMode: "SteamAppId", Target: "1",
			StopProcessName: stopName, Lifecycle: lc}
	}
	opts := ValidationOptions{AllowLifecycle: true}
	status := &HookConfig{Command: "checker"}
	stop := &HookConfig{Command: "stopper"}

	// status + stop hooks satisfy the requirement without stopProcessName
	errs, _ := validateGame(t, mk(&LifecycleConfig{Status: status, Stop: stop}, ""), opts)
	requireNoErrors(t, errs)

	// status-only is insufficient
	errs, _ = validateGame(t, mk(&LifecycleConfig{Status: status}, ""), opts)
	requireIssue(t, errs, "/games/u", "stopProcessName")

	// stop-only is insufficient (no observation mechanism)
	errs, _ = validateGame(t, mk(&LifecycleConfig{Stop: stop}, ""), opts)
	requireIssue(t, errs, "/games/u", "stopProcessName")

	// stopProcessName alone remains sufficient
	errs, _ = validateGame(t, mk(&LifecycleConfig{Status: status}, "Game.exe"), opts)
	requireNoErrors(t, errs)

	// legacy Validate() accepts the hook alternative too
	g := mk(&LifecycleConfig{Status: status, Kill: &HookConfig{Command: "k"}}, "")
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate must accept status+kill hooks in place of stopProcessName: %v", err)
	}
	g = mk(nil, "")
	if err := g.Validate(); err == nil {
		t.Fatalf("Validate must still require stopProcessName without hooks")
	}
}

func TestExactEnvIssuePaths(t *testing.T) {
	g := profiledGame()
	g.Env = map[string]string{"FEATURE,MODE": "x"}
	errs, _ := validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/env/FEATURE,MODE", "portable")

	g = profiledGame()
	g.UnsetEnv = []string{"OK_KEY", "GABS_BAD"}
	errs, _ = validateGame(t, g, ValidationOptions{})
	requireIssue(t, errs, "/unsetEnv/1", "reserved")
}

func TestWindowsScriptHooksRejected(t *testing.T) {
	prev := hookValidationGOOS
	hookValidationGOOS = "windows"
	defer func() { hookValidationGOOS = prev }()

	g := profiledGame()
	g.Lifecycle = &LifecycleConfig{Status: &HookConfig{Command: `C:\tools\status.bat`}}
	errs, _ := validateGame(t, g, ValidationOptions{AllowLifecycle: true})
	requireIssue(t, errs, "/lifecycle/status/command", "cmd.exe")

	// explicit interpreter form is the supported spelling
	g.Lifecycle = &LifecycleConfig{Status: &HookConfig{Command: "cmd.exe", Args: []string{"/c", `C:\tools\status.bat`}}}
	errs, _ = validateGame(t, g, ValidationOptions{AllowLifecycle: true})
	requireNoErrors(t, errs)

	// non-Windows platforms run scripts directly; no rejection
	hookValidationGOOS = "linux"
	g.Lifecycle = &LifecycleConfig{Status: &HookConfig{Command: "/opt/tools/status.sh"}}
	errs, _ = validateGame(t, g, ValidationOptions{AllowLifecycle: true})
	requireNoErrors(t, errs)
}
