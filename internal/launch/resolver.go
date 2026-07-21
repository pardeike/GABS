// Package launch owns pure launch resolution: profile selection, launch-input
// validation and substitution, argument ordering, environment merging, and
// lifecycle-hook resolution (design/02-launch-resolution.md). It is the only
// place launch context is computed; MCP and CLI both call it. Resolution has
// no side effects and deep-copies everything it returns.
package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pardeike/gabs/internal/config"
)

// Request identifies one launch to resolve.
type Request struct {
	GameID  string
	Profile string         // optional; empty selects defaultProfile (or unprofiled)
	Inputs  map[string]any // declared launch inputs: bool | string | json.Number | int | int64
}

// Options carries resolution environment/platform inputs. InheritedEnv is
// explicit (callers pass os.Environ()) so resolution stays pure and testable.
type Options struct {
	InheritedEnv       []string
	CaseInsensitiveEnv bool // Windows environment-key semantics
}

// Resolved is an immutable launch specification.
type Resolved struct {
	GameID         string
	Profile        string            // "" = unprofiled launch
	AppliedInputs  []string          // sorted names of inputs that actually applied
	Args           []string          // game args + profile args + input arg groups
	Env            map[string]string // config layers over sanitized inherited env (managed layer excluded)
	ContextEnvKeys []string          // config-defined env key names -> GABS_FORWARD_ENV
	AbsentEnvNames []string          // unsetEnv results still absent after merge -> GABS_ABSENT_ENV
	WorkingDir     string
	Lifecycle      *ResolvedLifecycle // nil when no hooks configured
	ConfigRevision string
}

// ResolvedHook is one lifecycle hook after placeholder substitution and
// defaulting — every field that affects execution or result interpretation.
// JSON tags are part of the runtime-claim schema (design/07): the resolved
// snapshot is persisted so stop/status after a restart or profile edit
// never consult mutable config.
type ResolvedHook struct {
	Command              string            `json:"command"`
	Args                 []string          `json:"args,omitempty"`
	WorkingDir           string            `json:"workingDir,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
	UnsetEnv             []string          `json:"unsetEnv,omitempty"`
	TimeoutSeconds       int               `json:"timeoutSeconds"`
	VerifyTimeoutSeconds int               `json:"verifyTimeoutSeconds,omitempty"` // stop/kill only
	RunningExitCodes     []int             `json:"runningExitCodes,omitempty"`     // status only
	StoppedExitCodes     []int             `json:"stoppedExitCodes,omitempty"`     // status only
}

// ResolvedLifecycle groups the resolved hooks; nil slots use built-in behavior.
type ResolvedLifecycle struct {
	Status *ResolvedHook `json:"status,omitempty"`
	Stop   *ResolvedHook `json:"stop,omitempty"`
	Kill   *ResolvedHook `json:"kill,omitempty"`
}

// ResolveError is a structured resolution failure carrying a stable code.
type ResolveError struct {
	Code       string // game_not_found | profiles_not_configured | profile_not_found | launch_input_not_declared | launch_input_invalid
	Message    string
	Candidates []string // sorted, when relevant
}

func (e *ResolveError) Error() string { return e.Code + ": " + e.Message }

// Resolve computes the launch specification for one request against one
// pinned config snapshot.
func Resolve(snap *config.Snapshot, req Request, opts Options) (*Resolved, *ResolveError) {
	game, ok := snap.Config.Games[req.GameID]
	if !ok {
		ids := make([]string, 0, len(snap.Config.Games))
		for id := range snap.Config.Games {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return nil, &ResolveError{Code: "game_not_found",
			Message: fmt.Sprintf("no configured game with ID %q", req.GameID), Candidates: ids}
	}

	// Profile selection.
	profileNames := sortedKeys(game.Profiles)
	profile := ""
	switch {
	case req.Profile != "" && len(game.Profiles) == 0:
		return nil, &ResolveError{Code: "profiles_not_configured",
			Message: fmt.Sprintf("game %q has no profiles configured; edit the config to add them (changes apply without restart)", req.GameID)}
	case req.Profile != "":
		if _, ok := game.Profiles[req.Profile]; !ok {
			return nil, &ResolveError{Code: "profile_not_found",
				Message:    fmt.Sprintf("game %q has no profile %q", req.GameID, req.Profile),
				Candidates: profileNames}
		}
		profile = req.Profile
	case len(game.Profiles) > 0:
		profile = game.DefaultProfile // validation guarantees present + existing
	}

	var selected config.ProfileConfig
	if profile != "" {
		selected = game.Profiles[profile]
	}

	// Launch inputs: validate every supplied value, apply in lexical order.
	suppliedNames := make([]string, 0, len(req.Inputs))
	for name := range req.Inputs {
		suppliedNames = append(suppliedNames, name)
	}
	sort.Strings(suppliedNames)

	type appliedInput struct {
		name string
		args []string
		env  map[string]string
	}
	var applied []appliedInput
	for _, name := range suppliedNames {
		decl, ok := game.LaunchInputs[name]
		if !ok {
			declared := sortedKeys(game.LaunchInputs)
			return nil, &ResolveError{Code: "launch_input_not_declared",
				Message:    fmt.Sprintf("launch input %q is not declared for game %q", name, req.GameID),
				Candidates: declared}
		}
		value, apply, verr := validateInputValue(name, &decl, req.Inputs[name])
		if verr != nil {
			return nil, verr
		}
		if !apply {
			continue // boolean false equals omission — checked before applicability
		}
		if len(decl.Profiles) > 0 {
			applicable := false
			for _, p := range decl.Profiles {
				if p == profile {
					applicable = true
				}
			}
			if !applicable {
				return nil, &ResolveError{Code: "launch_input_invalid",
					Message:    fmt.Sprintf("launch input %q is not applicable to profile %q", name, profile),
					Candidates: append([]string(nil), decl.Profiles...)}
			}
		}
		ai := appliedInput{name: name}
		for _, a := range decl.Args {
			ai.args = append(ai.args, strings.ReplaceAll(a, "${value}", value))
		}
		if len(decl.Env) > 0 {
			ai.env = make(map[string]string, len(decl.Env))
			for k, v := range decl.Env {
				ai.env[k] = strings.ReplaceAll(v, "${value}", value)
			}
		}
		applied = append(applied, ai)
	}

	// Arguments: game -> profile -> input groups (already lexical).
	args := append([]string(nil), game.Args...)
	args = append(args, selected.Args...)
	for _, ai := range applied {
		args = append(args, ai.args...)
	}

	// Environment merge over sanitized inherited env.
	env := newEnvMap(opts.CaseInsensitiveEnv)
	for _, kv := range opts.InheritedEnv {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		if hasReservedPrefix(k) {
			continue // inherited GABS_*/GABP_* stripped
		}
		env.set(k, v)
	}
	// Forwarding metadata must represent the effective (possibly
	// case-insensitive) environment: fold-keyed with last-writer spelling,
	// exactly like envMap, or GABS_FORWARD_ENV/GABS_ABSENT_ENV would list
	// two case variants of one effective key.
	unsetNames := map[string]string{} // folded -> last-writer spelling
	ctxNames := map[string]string{}   // folded -> last-writer spelling
	applyLayer := func(unset []string, layer map[string]string) {
		for _, k := range unset {
			env.unset(k)
			unsetNames[env.fold(k)] = k
		}
		for _, k := range sortedKeysOf(layer) {
			env.set(k, layer[k])
			ctxNames[env.fold(k)] = k
		}
	}
	applyLayer(game.UnsetEnv, game.Env)
	applyLayer(selected.UnsetEnv, selected.Env)
	for _, ai := range applied {
		applyLayer(nil, ai.env)
	}

	// Names expected absent: explicitly unset and not re-set by a later layer.
	var absent []string
	for _, k := range unsetNames {
		if !env.has(k) {
			absent = append(absent, k)
		}
	}
	sort.Strings(absent)

	// Config-defined context keys (game + profile + applied input env keys).
	ctxKeys := make([]string, 0, len(ctxNames))
	for _, k := range ctxNames {
		ctxKeys = append(ctxKeys, k)
	}
	sort.Strings(ctxKeys)

	workingDir := game.WorkingDir
	if selected.WorkingDir != "" {
		workingDir = selected.WorkingDir
	}

	appliedNames := make([]string, 0, len(applied))
	for _, ai := range applied {
		appliedNames = append(appliedNames, ai.name)
	}

	return &Resolved{
		GameID:         req.GameID,
		Profile:        profile,
		AppliedInputs:  appliedNames,
		Args:           args,
		Env:            env.toMap(),
		ContextEnvKeys: ctxKeys,
		AbsentEnvNames: absent,
		WorkingDir:     workingDir,
		Lifecycle:      resolveLifecycle(&game, profile, selected.Lifecycle),
		ConfigRevision: snap.Revision,
	}, nil
}

func invalidInput(name, why string) *ResolveError {
	return &ResolveError{Code: "launch_input_invalid",
		Message: fmt.Sprintf("launch input %q: %s", name, why)}
}

// validateInputValue checks one supplied value against its declaration and
// returns the canonical substitution string plus whether the input applies.
func validateInputValue(name string, decl *config.LaunchInputConfig, val any) (string, bool, *ResolveError) {
	switch decl.Type {
	case "boolean":
		b, ok := val.(bool)
		if !ok {
			return "", false, invalidInput(name, "value must be a boolean")
		}
		return "", b, nil // false equals omission

	case "string":
		s, ok := val.(string)
		if !ok {
			return "", false, invalidInput(name, "value must be a string")
		}
		if strings.ContainsRune(s, 0) {
			return "", false, invalidInput(name, "value must not contain NUL")
		}
		if !utf8.ValidString(s) {
			return "", false, invalidInput(name, "value must be valid UTF-8")
		}
		maxLen := config.InputMaxLengthDefault
		if decl.MaxLength != nil {
			maxLen = *decl.MaxLength
		}
		if n := utf8.RuneCountInString(s); n > maxLen {
			return "", false, invalidInput(name, fmt.Sprintf("value is %d code points, maxLength is %d", n, maxLen))
		}
		if len(decl.Enum) > 0 {
			ok := false
			for _, e := range decl.Enum {
				if e == s {
					ok = true
				}
			}
			if !ok {
				return "", false, &ResolveError{Code: "launch_input_invalid",
					Message:    fmt.Sprintf("launch input %q: value %q is not in the declared enum", name, s),
					Candidates: append([]string(nil), decl.Enum...)}
			}
		}
		if decl.Pattern != "" {
			// RE2, matched against the entire value (full-match anchoring).
			re, err := regexp.Compile("^(?:" + decl.Pattern + ")$")
			if err != nil {
				return "", false, invalidInput(name, "declared pattern does not compile: "+err.Error())
			}
			if !re.MatchString(s) {
				return "", false, invalidInput(name, fmt.Sprintf("value does not match pattern %q (full match)", decl.Pattern))
			}
		}
		return s, true, nil

	case "integer":
		var n int64
		switch v := val.(type) {
		case json.Number:
			parsed, err := strconv.ParseInt(v.String(), 10, 64)
			if err != nil {
				return "", false, invalidInput(name, fmt.Sprintf("value %q is not a 64-bit integer", v.String()))
			}
			n = parsed
		case int:
			n = int64(v)
		case int64:
			n = v
		case float64:
			// A floating intermediary rounds values above 2^53; exact decoding
			// (json.Number) is required end-to-end.
			return "", false, invalidInput(name, "integer values require exact decoding (json.Number); a floating-point value was supplied")
		default:
			return "", false, invalidInput(name, "value must be an integer")
		}
		if decl.Minimum != nil && n < *decl.Minimum {
			return "", false, invalidInput(name, fmt.Sprintf("value %d is below minimum %d", n, *decl.Minimum))
		}
		if decl.Maximum != nil && n > *decl.Maximum {
			return "", false, invalidInput(name, fmt.Sprintf("value %d is above maximum %d", n, *decl.Maximum))
		}
		return strconv.FormatInt(n, 10), true, nil
	}
	return "", false, invalidInput(name, "unknown declared type "+decl.Type)
}

// resolveLifecycle picks per-hook overrides (complete replacement, no field
// merge), substitutes placeholders, and applies defaults.
func resolveLifecycle(game *config.GameConfig, profile string, profileLC *config.LifecycleConfig) *ResolvedLifecycle {
	pick := func(slot func(*config.LifecycleConfig) *config.HookConfig) *config.HookConfig {
		if profileLC != nil {
			if h := slot(profileLC); h != nil {
				return h
			}
		}
		if game.Lifecycle != nil {
			return slot(game.Lifecycle)
		}
		return nil
	}
	status := pick(func(lc *config.LifecycleConfig) *config.HookConfig { return lc.Status })
	stop := pick(func(lc *config.LifecycleConfig) *config.HookConfig { return lc.Stop })
	kill := pick(func(lc *config.LifecycleConfig) *config.HookConfig { return lc.Kill })
	if status == nil && stop == nil && kill == nil {
		return nil
	}

	sub := func(s string) string {
		s = strings.ReplaceAll(s, "${gameId}", game.ID)
		if profile != "" {
			s = strings.ReplaceAll(s, "${profile}", profile)
		}
		return s
	}
	resolveHook := func(h *config.HookConfig, kind string) *ResolvedHook {
		if h == nil {
			return nil
		}
		r := &ResolvedHook{
			// PATH-based commands pin to an absolute path at resolution so
			// restart recovery never depends on a later process having the
			// same PATH (design/20). Resolution failure keeps the literal;
			// static checks and hook execution surface it.
			Command:    pinHookCommand(h.Command),
			WorkingDir: sub(h.WorkingDir),
			UnsetEnv:   append([]string(nil), h.UnsetEnv...),
		}
		for _, a := range h.Args {
			r.Args = append(r.Args, sub(a))
		}
		if len(h.Env) > 0 {
			r.Env = make(map[string]string, len(h.Env))
			for k, v := range h.Env {
				r.Env[k] = sub(v)
			}
		}
		switch kind {
		case "status":
			r.TimeoutSeconds = config.StatusHookTimeoutDefault
			r.RunningExitCodes = []int{0}
			r.StoppedExitCodes = []int{1}
			if len(h.RunningExitCodes) > 0 {
				r.RunningExitCodes = append([]int(nil), h.RunningExitCodes...)
			}
			if len(h.StoppedExitCodes) > 0 {
				r.StoppedExitCodes = append([]int(nil), h.StoppedExitCodes...)
			}
		case "stop":
			r.TimeoutSeconds = config.StopHookTimeoutDefault
			r.VerifyTimeoutSeconds = config.VerifyTimeoutDefault
		case "kill":
			r.TimeoutSeconds = config.KillHookTimeoutDefault
			r.VerifyTimeoutSeconds = config.VerifyTimeoutDefault
		}
		if h.TimeoutSeconds != nil {
			r.TimeoutSeconds = *h.TimeoutSeconds
		}
		if h.VerifyTimeoutSeconds != nil && kind != "status" {
			r.VerifyTimeoutSeconds = *h.VerifyTimeoutSeconds
		}
		return r
	}
	return &ResolvedLifecycle{
		Status: resolveHook(status, "status"),
		Stop:   resolveHook(stop, "stop"),
		Kill:   resolveHook(kill, "kill"),
	}
}

// pinHookCommand resolves a separator-free hook command through PATH to an
// absolute path; path-containing commands pass through unchanged.
func pinHookCommand(command string) string {
	if command == "" || strings.ContainsRune(command, os.PathSeparator) || strings.ContainsRune(command, '/') {
		return command
	}
	if abs, err := exec.LookPath(command); err == nil {
		if resolved, err := filepath.Abs(abs); err == nil {
			return resolved
		}
		return abs
	}
	return command
}

// envMap merges environment layers with optional case-insensitive keys.
type envMap struct {
	caseInsensitive bool
	entries         map[string]envEntry // folded key -> entry
}

type envEntry struct {
	key   string // actual (last-writer) key spelling
	value string
}

func newEnvMap(caseInsensitive bool) *envMap {
	return &envMap{caseInsensitive: caseInsensitive, entries: map[string]envEntry{}}
}

func (m *envMap) fold(k string) string {
	if m.caseInsensitive {
		return strings.ToUpper(k)
	}
	return k
}

func (m *envMap) set(k, v string)   { m.entries[m.fold(k)] = envEntry{key: k, value: v} }
func (m *envMap) unset(k string)    { delete(m.entries, m.fold(k)) }
func (m *envMap) has(k string) bool { _, ok := m.entries[m.fold(k)]; return ok }

func (m *envMap) toMap() map[string]string {
	out := make(map[string]string, len(m.entries))
	for _, e := range m.entries {
		out[e.key] = e.value
	}
	return out
}

func hasReservedPrefix(key string) bool {
	u := strings.ToUpper(key)
	return strings.HasPrefix(u, "GABS_") || strings.HasPrefix(u, "GABP_")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysOf(m map[string]string) []string { return sortedKeys(m) }
