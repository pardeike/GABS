package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// ProfileConfig is a named launch-context overlay. It may vary launch
// context (args, env, unsetEnv, workingDir, lifecycle hooks) but never
// identity or transport, which stay at game level.
type ProfileConfig struct {
	Description string            `json:"description,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	UnsetEnv    []string          `json:"unsetEnv,omitempty"`
	WorkingDir  string            `json:"workingDir,omitempty"`
	Lifecycle   *LifecycleConfig  `json:"lifecycle,omitempty"`
}

// LaunchInputConfig declares a named, typed, caller-suppliable launch input.
type LaunchInputConfig struct {
	Description string            `json:"description"`
	Type        string            `json:"type"` // boolean|string|integer
	Enum        []string          `json:"enum,omitempty"`
	Minimum     *int64            `json:"minimum,omitempty"`
	Maximum     *int64            `json:"maximum,omitempty"`
	MaxLength   *int              `json:"maxLength,omitempty"`
	Pattern     string            `json:"pattern,omitempty"`
	Profiles    []string          `json:"profiles,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// LifecycleConfig groups the three lifecycle hooks.
type LifecycleConfig struct {
	Status *HookConfig `json:"status,omitempty"`
	Stop   *HookConfig `json:"stop,omitempty"`
	Kill   *HookConfig `json:"kill,omitempty"`
}

// hookValidationGOOS gates platform-specific hook validation; injectable so
// the Windows rules are testable everywhere.
var hookValidationGOOS = runtime.GOOS

// HookConfig is one lifecycle command: exact executable-plus-argv, never a shell.
type HookConfig struct {
	Command              string            `json:"command"`
	Args                 []string          `json:"args,omitempty"`
	WorkingDir           string            `json:"workingDir,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
	UnsetEnv             []string          `json:"unsetEnv,omitempty"`
	TimeoutSeconds       *int              `json:"timeoutSeconds,omitempty"`
	VerifyTimeoutSeconds *int              `json:"verifyTimeoutSeconds,omitempty"`
	RunningExitCodes     []int             `json:"runningExitCodes,omitempty"`
	StoppedExitCodes     []int             `json:"stoppedExitCodes,omitempty"`
}

// Hook timing bounds and defaults (integral seconds).
const (
	StatusHookTimeoutDefault = 5
	StatusHookTimeoutMax     = 60
	StopHookTimeoutDefault   = 30
	KillHookTimeoutDefault   = 10
	ActionHookTimeoutMax     = 600
	VerifyTimeoutDefault     = 15
	VerifyTimeoutMax         = 600
	InputMaxLengthDefault    = 1024
	InputMaxLengthMax        = 65536
)

// IssueCodeModeIncompatible marks issues where the launch mode rejects
// profiles/inputs/env — games_start maps them to the stable Stage 1 code
// launch_mode_incompatible instead of the generic config_invalid (design/05).
const IssueCodeModeIncompatible = "mode_incompatible"

// ConfigIssue is one validation finding with an RFC 6901 JSON pointer path.
// Code optionally classifies the issue for stable-outcome mapping.
type ConfigIssue struct {
	Path    string
	Message string
	Code    string
}

func (i ConfigIssue) String() string { return i.Path + ": " + i.Message }

// ValidationError aggregates config issues into one error.
type ValidationError struct {
	Issues []ConfigIssue
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, is := range e.Issues {
		parts = append(parts, "  "+is.String())
	}
	return "invalid configuration:\n" + strings.Join(parts, "\n")
}

// ValidationOptions controls extension validation.
type ValidationOptions struct {
	// AllowLifecycle lifts the milestone-1 feature gate. Until the lifecycle
	// runtime lands, configs must not validate against semantics that do not
	// execute yet.
	AllowLifecycle bool
	// CaseInsensitiveEnv rejects env keys that collide after ASCII case
	// folding (Windows environment semantics).
	CaseInsensitiveEnv bool
}

// DefaultValidationOptions returns the options used on the load path.
func DefaultValidationOptions() ValidationOptions {
	return ValidationOptions{
		AllowLifecycle:     false,
		CaseInsensitiveEnv: runtime.GOOS == "windows",
	}
}

var (
	profileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	envKeyRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholderRe = regexp.MustCompile(`\$\{([^}]*)\}`)
)

const urlModeHint = "launches via a launcher URL and cannot deliver launch context to the game; use SteamManaged, DirectPath, or CustomCommand"

// escapePointerToken escapes one JSON pointer token per RFC 6901.
func escapePointerToken(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

func hasNUL(s string) bool { return strings.ContainsRune(s, 0) }

func isURLLaunchMode(mode string) bool {
	return mode == "SteamAppId" || mode == "EpicAppId"
}

// ValidateGameExtensions validates the profile/launch-input/lifecycle
// extension fields of one game entry. Legacy entries without extension
// fields produce no issues at all (compatibility promise: warning-free).
func ValidateGameExtensions(gameID string, g *GameConfig, opts ValidationOptions) (errs, warns []ConfigIssue) {
	base := "/games/" + escapePointerToken(gameID)

	hasExtensions := len(g.Env) > 0 || len(g.UnsetEnv) > 0 || g.DefaultProfile != "" ||
		len(g.Profiles) > 0 || len(g.LaunchInputs) > 0 || g.Lifecycle != nil
	hasProfileLifecycle := false
	for _, p := range g.Profiles {
		if p.Lifecycle != nil {
			hasProfileLifecycle = true
		}
	}
	if !hasExtensions && !hasProfileLifecycle {
		return nil, nil
	}

	addErr := func(path, msg string) { errs = append(errs, ConfigIssue{Path: path, Message: msg}) }

	// URL launch modes cannot deliver args/env/cwd to the game: context
	// fields must not validate silently. Lifecycle hooks remain valid.
	if isURLLaunchMode(g.LaunchMode) {
		addModeErr := func(path, msg string) {
			errs = append(errs, ConfigIssue{Path: path, Message: msg, Code: IssueCodeModeIncompatible})
		}
		if len(g.Profiles) > 0 || g.DefaultProfile != "" {
			addModeErr(base+"/profiles", g.LaunchMode+" "+urlModeHint)
		}
		if len(g.LaunchInputs) > 0 {
			addModeErr(base+"/launchInputs", g.LaunchMode+" "+urlModeHint)
		}
		if len(g.Env) > 0 {
			addModeErr(base+"/env", g.LaunchMode+" "+urlModeHint)
		}
		if len(g.UnsetEnv) > 0 {
			addModeErr(base+"/unsetEnv", g.LaunchMode+" "+urlModeHint)
		}
		errs = append(errs, validateLifecycleSlot(base, g.Lifecycle, len(g.Profiles) > 0, opts)...)
		// One observation/control mechanism stays mandatory for URL modes:
		// the URL-opener helper PID proves nothing about the workload. Hooks
		// (status + stop-or-kill) may satisfy it in place of stopProcessName.
		if opts.AllowLifecycle && g.StopProcessName == "" && !g.hasURLHookAlternative() {
			addErr(base, g.LaunchMode+" games must declare stopProcessName, or a game-level status hook plus a stop or kill hook")
		}
		return errs, warns
	}

	// Game-level env layer.
	errs = append(errs, validateEnvLayer(base, g.Env, g.UnsetEnv, opts.CaseInsensitiveEnv)...)

	// Profiles.
	profileNames := make([]string, 0, len(g.Profiles))
	for name := range g.Profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	hasProfiles := len(g.Profiles) > 0
	for _, name := range profileNames {
		p := g.Profiles[name]
		ppath := base + "/profiles/" + escapePointerToken(name)
		if !profileNameRe.MatchString(name) {
			addErr(ppath, "invalid profile name (must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$)")
		}
		errs = append(errs, validateEnvLayer(ppath, p.Env, p.UnsetEnv, opts.CaseInsensitiveEnv)...)
		for i, a := range p.Args {
			if hasNUL(a) {
				addErr(fmt.Sprintf("%s/args/%d", ppath, i), "argument must not contain NUL")
			}
		}
		if p.WorkingDir != "" {
			if hasNUL(p.WorkingDir) {
				addErr(ppath+"/workingDir", "working directory must not contain NUL")
			} else if !filepath.IsAbs(p.WorkingDir) {
				addErr(ppath+"/workingDir", "working directory must be an absolute path")
			}
		}
		errs = append(errs, validateLifecycleSlot(ppath, p.Lifecycle, true, opts)...)
	}

	// defaultProfile rules.
	switch {
	case hasProfiles && g.DefaultProfile == "":
		addErr(base+"/defaultProfile", "defaultProfile is required when profiles are configured")
	case g.DefaultProfile != "" && !hasProfiles:
		addErr(base+"/defaultProfile", "defaultProfile is set but no profiles are configured")
	case g.DefaultProfile != "" && hasProfiles:
		if _, ok := g.Profiles[g.DefaultProfile]; !ok {
			addErr(base+"/defaultProfile", fmt.Sprintf("defaultProfile %q does not name a configured profile", g.DefaultProfile))
		}
	}

	// Launch inputs.
	inputNames := make([]string, 0, len(g.LaunchInputs))
	for name := range g.LaunchInputs {
		inputNames = append(inputNames, name)
	}
	sort.Strings(inputNames)
	for _, name := range inputNames {
		in := g.LaunchInputs[name]
		ipath := base + "/launchInputs/" + escapePointerToken(name)
		if !profileNameRe.MatchString(name) {
			addErr(ipath, "invalid launch input name (must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$)")
		}
		errs = append(errs, validateLaunchInput(ipath, name, &in, g.Profiles, opts.CaseInsensitiveEnv)...)
	}

	// Cross-input env conflicts: two inputs that could both apply to the same
	// launch must not write the same env key.
	errs = append(errs, validateInputEnvConflicts(base, g.LaunchInputs, opts.CaseInsensitiveEnv)...)

	// Game-level lifecycle.
	errs = append(errs, validateLifecycleSlot(base, g.Lifecycle, hasProfiles, opts)...)

	return errs, warns
}

func validateEnvLayer(base string, env map[string]string, unset []string, caseInsensitive bool) []ConfigIssue {
	var issues []ConfigIssue
	add := func(path, msg string) { issues = append(issues, ConfigIssue{Path: path, Message: msg}) }

	fold := func(k string) string {
		if caseInsensitive {
			return strings.ToUpper(k)
		}
		return k
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	seenFolded := map[string]string{}
	for _, k := range keys {
		kpath := base + "/env/" + escapePointerToken(k)
		issues = append(issues, validateEnvKey(kpath, k)...)
		if hasNUL(env[k]) {
			add(kpath, "environment value must not contain NUL")
		}
		if prev, ok := seenFolded[fold(k)]; ok {
			add(kpath, fmt.Sprintf("environment keys %q and %q collide after case folding", prev, k))
		} else {
			seenFolded[fold(k)] = k
		}
	}

	seenUnset := map[string]string{}
	for i, k := range unset {
		upath := fmt.Sprintf("%s/unsetEnv/%d", base, i)
		issues = append(issues, validateEnvKey(upath, k)...)
		if prev, ok := seenUnset[fold(k)]; ok {
			if prev == k {
				add(upath, fmt.Sprintf("duplicate unsetEnv entry %q", k))
			} else {
				add(upath, fmt.Sprintf("unsetEnv entries %q and %q collide after case folding", prev, k))
			}
			continue
		}
		seenUnset[fold(k)] = k
		if _, ok := seenFolded[fold(k)]; ok {
			add(upath, fmt.Sprintf("key %q appears in both env and unsetEnv of the same layer", k))
		}
	}
	return issues
}

// validateEnvKey reports issues at the exact RFC 6901 member/index path.
func validateEnvKey(memberPath, key string) []ConfigIssue {
	if !envKeyRe.MatchString(key) {
		return []ConfigIssue{{Path: memberPath, Message: fmt.Sprintf("environment key %q must match the portable identifier grammar ^[A-Za-z_][A-Za-z0-9_]*$", key)}}
	}
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "GABS_") || strings.HasPrefix(upper, "GABP_") {
		return []ConfigIssue{{Path: memberPath, Message: "environment keys with the reserved prefixes GABS_/GABP_ are not allowed"}}
	}
	return nil
}

// hasURLHookAlternative reports whether lifecycle hooks satisfy the URL-mode
// observation/control requirement in place of stopProcessName: a game-level
// status hook plus a stop or kill hook.
func (g *GameConfig) hasURLHookAlternative() bool {
	return g.Lifecycle != nil && g.Lifecycle.Status != nil &&
		(g.Lifecycle.Stop != nil || g.Lifecycle.Kill != nil)
}

func countValuePlaceholders(in *LaunchInputConfig) int {
	n := 0
	for _, a := range in.Args {
		n += strings.Count(a, "${value}")
	}
	for _, v := range in.Env {
		n += strings.Count(v, "${value}")
	}
	return n
}

func validateLaunchInput(ipath, name string, in *LaunchInputConfig, profiles map[string]ProfileConfig, caseInsensitive bool) []ConfigIssue {
	var issues []ConfigIssue
	add := func(path, msg string) { issues = append(issues, ConfigIssue{Path: path, Message: msg}) }

	if strings.TrimSpace(in.Description) == "" {
		add(ipath+"/description", "description is required")
	}

	switch in.Type {
	case "boolean", "string", "integer":
	default:
		add(ipath+"/type", "type must be one of boolean, string, integer")
		return issues // constraint checks below depend on a valid type
	}

	if len(in.Args) == 0 && len(in.Env) == 0 {
		add(ipath, "launch input must declare at least one args or env binding")
	}

	valueCount := countValuePlaceholders(in)
	switch in.Type {
	case "boolean":
		if valueCount > 0 {
			add(ipath, "boolean inputs must not use ${value} in their bindings")
		}
	default:
		if valueCount == 0 && (len(in.Args) > 0 || len(in.Env) > 0) {
			add(ipath, in.Type+" inputs must use ${value} at least once in their bindings")
		}
	}

	// Constraint applicability.
	if len(in.Enum) > 0 && in.Type != "string" {
		add(ipath+"/enum", "enum is only valid for string inputs")
	}
	if (in.Minimum != nil || in.Maximum != nil) && in.Type != "integer" {
		add(ipath+"/minimum", "minimum/maximum are only valid for integer inputs")
	}
	if in.MaxLength != nil && in.Type != "string" {
		add(ipath+"/maxLength", "maxLength is only valid for string inputs")
	}
	if in.Pattern != "" && in.Type != "string" {
		add(ipath+"/pattern", "pattern is only valid for string inputs")
	}

	if in.Minimum != nil && in.Maximum != nil && *in.Minimum > *in.Maximum {
		add(ipath, "minimum must not exceed maximum")
	}
	if in.MaxLength != nil && (*in.MaxLength < 1 || *in.MaxLength > InputMaxLengthMax) {
		add(ipath+"/maxLength", fmt.Sprintf("maxLength must be between 1 and %d", InputMaxLengthMax))
	}
	if in.Pattern != "" && in.Type == "string" {
		if _, err := regexp.Compile("^(?:" + in.Pattern + ")$"); err != nil {
			add(ipath+"/pattern", fmt.Sprintf("invalid pattern (RE2, matched against the entire value): %v", err))
		}
	}
	if in.Type == "string" {
		seen := map[string]bool{}
		for i, e := range in.Enum {
			if hasNUL(e) {
				add(fmt.Sprintf("%s/enum/%d", ipath, i), "enum value must not contain NUL")
			}
			if seen[e] {
				add(fmt.Sprintf("%s/enum/%d", ipath, i), fmt.Sprintf("duplicate enum value %q", e))
			}
			seen[e] = true
		}
	}

	for i, a := range in.Args {
		if hasNUL(a) {
			add(fmt.Sprintf("%s/args/%d", ipath, i), "argument must not contain NUL")
		}
	}
	issues = append(issues, validateEnvLayer(ipath, in.Env, nil, caseInsensitive)...)

	// Profile applicability references.
	seenProf := map[string]bool{}
	for i, p := range in.Profiles {
		if seenProf[p] {
			add(fmt.Sprintf("%s/profiles/%d", ipath, i), fmt.Sprintf("duplicate profile reference %q", p))
			continue
		}
		seenProf[p] = true
		if _, ok := profiles[p]; !ok {
			add(fmt.Sprintf("%s/profiles/%d", ipath, i), fmt.Sprintf("references unknown profile %q", p))
		}
	}
	return issues
}

// validateInputEnvConflicts rejects configurations where two simultaneously
// applicable inputs could write the same environment key.
func validateInputEnvConflicts(base string, inputs map[string]LaunchInputConfig, caseInsensitive bool) []ConfigIssue {
	var issues []ConfigIssue
	fold := func(k string) string {
		if caseInsensitive {
			return strings.ToUpper(k)
		}
		return k
	}
	applicableOverlap := func(a, b []string) bool {
		if len(a) == 0 || len(b) == 0 {
			return true // empty applicability = every profile
		}
		set := map[string]bool{}
		for _, p := range a {
			set[p] = true
		}
		for _, p := range b {
			if set[p] {
				return true
			}
		}
		return false
	}

	names := sortedKeys(inputs)
	for i, a := range names {
		for _, b := range names[i+1:] {
			ia, ib := inputs[a], inputs[b]
			if !applicableOverlap(ia.Profiles, ib.Profiles) {
				continue
			}
			for _, k := range sortedKeys(ia.Env) {
				for _, k2 := range sortedKeys(ib.Env) {
					if fold(k) == fold(k2) {
						issues = append(issues, ConfigIssue{
							Path:    base + "/launchInputs",
							Message: fmt.Sprintf("inputs %q and %q can both set environment key %q", a, b, k),
						})
					}
				}
			}
		}
	}
	return issues
}

func validateLifecycleSlot(base string, lc *LifecycleConfig, hasProfiles bool, opts ValidationOptions) []ConfigIssue {
	if lc == nil {
		return nil
	}
	if !opts.AllowLifecycle {
		return []ConfigIssue{{
			Path:    base + "/lifecycle",
			Message: "lifecycle hooks are not yet supported by this build",
		}}
	}
	var issues []ConfigIssue
	if lc.Status != nil {
		issues = append(issues, validateHook(base+"/lifecycle/status", lc.Status, "status", hasProfiles, opts)...)
	}
	if lc.Stop != nil {
		issues = append(issues, validateHook(base+"/lifecycle/stop", lc.Stop, "stop", hasProfiles, opts)...)
	}
	if lc.Kill != nil {
		issues = append(issues, validateHook(base+"/lifecycle/kill", lc.Kill, "kill", hasProfiles, opts)...)
	}
	return issues
}

func validateHook(hpath string, h *HookConfig, kind string, hasProfiles bool, opts ValidationOptions) []ConfigIssue {
	var issues []ConfigIssue
	add := func(path, msg string) { issues = append(issues, ConfigIssue{Path: path, Message: msg}) }

	if strings.TrimSpace(h.Command) == "" {
		add(hpath+"/command", "command is required")
	} else if hasNUL(h.Command) {
		add(hpath+"/command", "command must not contain NUL")
	}
	if placeholderRe.MatchString(h.Command) {
		add(hpath+"/command", "command does not support placeholders")
	}
	// Windows would implicitly wrap script commands in cmd.exe, whose argv
	// quoting is injectable; scripts must be configured explicitly (design/20).
	if hookValidationGOOS == "windows" {
		switch strings.ToLower(filepath.Ext(h.Command)) {
		case ".bat", ".cmd", ".ps1", ".vbs", ".js":
			add(hpath+"/command", "script hooks are not run directly on Windows; set command to the interpreter (e.g. cmd.exe) and pass the script via args (e.g. /c, script path)")
		}
	}

	if h.TimeoutSeconds != nil {
		limit := ActionHookTimeoutMax
		if kind == "status" {
			limit = StatusHookTimeoutMax
		}
		if *h.TimeoutSeconds < 1 || *h.TimeoutSeconds > limit {
			add(hpath+"/timeoutSeconds", fmt.Sprintf("timeoutSeconds must be between 1 and %d for %s hooks", limit, kind))
		}
	}
	if h.VerifyTimeoutSeconds != nil {
		if kind == "status" {
			add(hpath+"/verifyTimeoutSeconds", "verifyTimeoutSeconds is not valid for status hooks")
		} else if *h.VerifyTimeoutSeconds < 1 || *h.VerifyTimeoutSeconds > VerifyTimeoutMax {
			add(hpath+"/verifyTimeoutSeconds", fmt.Sprintf("verifyTimeoutSeconds must be between 1 and %d", VerifyTimeoutMax))
		}
	}

	checkSet := func(field string, set []int) map[int]bool {
		vals := map[int]bool{}
		if set == nil {
			return vals
		}
		if kind != "status" {
			add(hpath+"/"+field, field+" is only valid for status hooks")
			return vals
		}
		if len(set) == 0 {
			add(hpath+"/"+field, field+" must not be empty when specified")
		}
		for _, c := range set {
			if vals[c] {
				add(hpath+"/"+field, fmt.Sprintf("duplicate exit code %d", c))
			}
			vals[c] = true
		}
		return vals
	}
	running := checkSet("runningExitCodes", h.RunningExitCodes)
	stopped := checkSet("stoppedExitCodes", h.StoppedExitCodes)
	for c := range running {
		if stopped[c] {
			add(hpath, fmt.Sprintf("runningExitCodes and stoppedExitCodes must be disjoint (both contain %d)", c))
		}
	}

	if h.WorkingDir != "" {
		if hasNUL(h.WorkingDir) {
			add(hpath+"/workingDir", "working directory must not contain NUL")
		} else {
			// Placeholders cannot make a path absolute (their values contain
			// no separators), so probe with substituted dummies: a literal
			// like "relative/${profile}" must not bypass the check.
			probe := placeholderRe.ReplaceAllString(h.WorkingDir, "x")
			if !filepath.IsAbs(probe) {
				add(hpath+"/workingDir", "working directory must be an absolute path (after placeholder substitution)")
			}
		}
		issues = append(issues, validatePlaceholders(hpath+"/workingDir", h.WorkingDir, hasProfiles)...)
	}
	for i, a := range h.Args {
		if hasNUL(a) {
			add(fmt.Sprintf("%s/args/%d", hpath, i), "argument must not contain NUL")
		}
		issues = append(issues, validatePlaceholders(fmt.Sprintf("%s/args/%d", hpath, i), a, hasProfiles)...)
	}
	issues = append(issues, validateEnvLayer(hpath, h.Env, h.UnsetEnv, opts.CaseInsensitiveEnv)...)
	for _, k := range sortedKeys(h.Env) {
		issues = append(issues, validatePlaceholders(hpath+"/env/"+escapePointerToken(k), h.Env[k], hasProfiles)...)
	}
	return issues
}

func validatePlaceholders(path, s string, hasProfiles bool) []ConfigIssue {
	var issues []ConfigIssue
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		switch m[1] {
		case "gameId":
		case "profile":
			if !hasProfiles {
				issues = append(issues, ConfigIssue{Path: path, Message: "${profile} placeholder requires configured profiles"})
			}
		default:
			issues = append(issues, ConfigIssue{Path: path, Message: fmt.Sprintf("unknown placeholder ${%s}", m[1])})
		}
	}
	return issues
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
