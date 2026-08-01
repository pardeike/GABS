package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestHoistGlobalFlagsPosition covers the defect where a process-wide flag
// written after a subcommand was silently dropped: Go's flag package stops
// parsing at the first non-flag token, so `gabs games list --configDir /path`
// parsed zero flags and operated on the DEFAULT config directory while
// exiting 0. For a configuration-first tool, silently acting on the wrong
// config file is the worst available failure mode.
func TestHoistGlobalFlagsPosition(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		globals []string
		rest    []string
	}{
		{
			name:    "flag before the action still works",
			args:    []string{"--configDir", "/cfg", "list"},
			globals: []string{"--configDir", "/cfg"},
			rest:    []string{"list"},
		},
		{
			name:    "flag after the action is hoisted, not dropped",
			args:    []string{"list", "--configDir", "/cfg"},
			globals: []string{"--configDir", "/cfg"},
			rest:    []string{"list"},
		},
		{
			name:    "flag after action and positional",
			args:    []string{"show", "adventure", "--configDir", "/cfg"},
			globals: []string{"--configDir", "/cfg"},
			rest:    []string{"show", "adventure"},
		},
		{
			name:    "equals form",
			args:    []string{"list", "--configDir=/cfg"},
			globals: []string{"--configDir=/cfg"},
			rest:    []string{"list"},
		},
		{
			name:    "single-dash form is accepted like the flag package does",
			args:    []string{"list", "-configDir", "/cfg"},
			globals: []string{"-configDir", "/cfg"},
			rest:    []string{"list"},
		},
		{
			name:    "subcommand flags are left for the action parser",
			args:    []string{"start", "adventure", "--profile", "combat", "--configDir", "/cfg"},
			globals: []string{"--configDir", "/cfg"},
			rest:    []string{"start", "adventure", "--profile", "combat"},
		},
		{
			name:    "interleaved global and subcommand flags",
			args:    []string{"start", "adventure", "--configDir", "/cfg", "--input", "seed=1"},
			globals: []string{"--configDir", "/cfg"},
			rest:    []string{"start", "adventure", "--input", "seed=1"},
		},
		{
			name:    "multiple globals",
			args:    []string{"doctor", "adventure", "--configDir", "/cfg", "--log-level", "debug"},
			globals: []string{"--configDir", "/cfg", "--log-level", "debug"},
			rest:    []string{"doctor", "adventure"},
		},
		{
			// The marker is passed through, not consumed: the subcommand layer
			// honors it too, so a dash-prefixed game ID stays addressable.
			name:    "a literal -- stops hoisting and is passed through",
			args:    []string{"start", "adventure", "--", "--configDir", "/cfg"},
			globals: nil,
			rest:    []string{"start", "adventure", "--", "--configDir", "/cfg"},
		},
		{
			name:    "no globals present",
			args:    []string{"status"},
			globals: nil,
			rest:    []string{"status"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globals, rest, err := hoistGlobalFlags(tc.args)
			if err != nil {
				t.Fatalf("hoistGlobalFlags(%q) returned error: %v", tc.args, err)
			}
			if len(globals) == 0 && len(tc.globals) == 0 {
				// both empty: reflect.DeepEqual distinguishes nil from empty
			} else if !reflect.DeepEqual(globals, tc.globals) {
				t.Errorf("globals = %q, want %q", globals, tc.globals)
			}
			if !reflect.DeepEqual(rest, tc.rest) {
				t.Errorf("rest = %q, want %q", rest, tc.rest)
			}
		})
	}
}

// TestHoistGlobalFlagsMissingValue keeps a truncated global flag loud instead
// of letting it consume a positional argument.
func TestHoistGlobalFlagsMissingValue(t *testing.T) {
	if _, _, err := hoistGlobalFlags([]string{"list", "--configDir"}); err == nil {
		t.Fatal("expected an error when --configDir has no value")
	}
}

// TestGamesActionsRejectTrailingArgs covers the second half of the same defect:
// actions read a fixed argument position and silently ignored everything after
// it, so a mistyped or misplaced token disappeared without a word.
func TestGamesActionsRejectTrailingArgs(t *testing.T) {
	cases := [][]string{
		{"list", "unexpected"},
		{"show", "adventure", "unexpected"},
		{"status", "adventure", "unexpected"},
		{"stop", "adventure", "unexpected"},
		{"kill", "adventure", "unexpected"},
		{"remove", "adventure", "unexpected"},
	}
	for _, args := range cases {
		t.Run(args[0], func(t *testing.T) {
			if err := checkNoTrailingArgs(args[0], args[1:], nil, trailingAllowanceFor(args[0])); err == nil {
				t.Fatalf("expected %q to reject trailing arguments", args)
			}
		})
	}
}

// TestActionsWithOwnParserAreLeftAlone documents why the generic check defers
// to `start`, `doctor` and `repair`: their flags may take values, and a value
// like the NAME in `--profile NAME` is indistinguishable from a stray
// positional to a generic scanner. Their own parsers reject what they do not
// know, so the generic check must not fire first.
func TestActionsWithOwnParserAreLeftAlone(t *testing.T) {
	cases := [][]string{
		{"start", "adventure", "--profile", "combat"},
		{"start", "adventure", "--input", "seed=1", "--input", "quickStart=true"},
		{"doctor", "adventure", "--show-last-good"},
		{"repair", "adventure", "--forget-runtime", "--yes"},
	}
	for _, args := range cases {
		t.Run(args[0]+"/"+strings.Join(args[1:], "_"), func(t *testing.T) {
			if err := checkNoTrailingArgs(args[0], args[1:], nil, trailingAllowanceFor(args[0])); err != nil {
				t.Fatalf("%q must be left to the action parser, got %v", args, err)
			}
		})
	}
}

// TestUnknownFlagOnSimpleActionIsLoud makes a leftover flag-like token an error
// on actions with no flag surface of their own; globals are hoisted before this
// runs, so anything remaining is genuinely unrecognized.
func TestUnknownFlagOnSimpleActionIsLoud(t *testing.T) {
	if err := checkNoTrailingArgs("list", []string{"--nope"}, nil, trailingAllowanceFor("list")); err == nil {
		t.Fatal("expected an unknown-flag error for `games list --nope`")
	}
}

// TestDoubleDashEscapesDashPrefixedIDs pins the "--" contract: hoisting keeps
// the marker and everything after it — even a token spelled like a global
// flag — and the trailing check accepts escaped tokens as positionals while
// still counting them against the action's arity.
func TestDoubleDashEscapesDashPrefixedIDs(t *testing.T) {
	globals, rest, err := hoistGlobalFlags([]string{"games", "stop", "--", "-grace"})
	if err != nil {
		t.Fatal(err)
	}
	if len(globals) != 0 {
		t.Fatalf("a token behind \"--\" must never be hoisted as a global flag, got %v", globals)
	}
	want := []string{"games", "stop", "--", "-grace"}
	if strings.Join(rest, " ") != strings.Join(want, " ") {
		t.Fatalf("hoisting must pass \"--\" through for the subcommand layer, got %v", rest)
	}

	if err := checkNoTrailingArgs("stop", nil, []string{"-dash"}, 1); err != nil {
		t.Fatalf("an escaped dash-prefixed ID must be accepted as a positional: %v", err)
	}
	if err := checkNoTrailingArgs("stop", []string{"-dash"}, nil, 1); err == nil {
		t.Fatal("an unescaped dash token must stay a loud unknown flag")
	}
	if err := checkNoTrailingArgs("stop", []string{"id"}, []string{"extra"}, 1); err == nil {
		t.Fatal("escaped tokens must still count against the action's arity")
	}
}

// TestGamesActionsAcceptTheirOwnArity guards against the trailing-argument
// check rejecting the documented forms.
func TestGamesActionsAcceptTheirOwnArity(t *testing.T) {
	cases := [][]string{
		{"list"},
		{"status"},
		{"status", "adventure"},
		{"show", "adventure"},
		{"stop", "adventure"},
		{"kill", "adventure"},
	}
	for _, args := range cases {
		t.Run(args[0], func(t *testing.T) {
			if err := checkNoTrailingArgs(args[0], args[1:], nil, trailingAllowanceFor(args[0])); err != nil {
				t.Fatalf("%q should be accepted, got %v", args, err)
			}
		})
	}
}
