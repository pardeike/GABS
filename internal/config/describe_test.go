package config

import (
	"strings"
	"testing"
)

func profileFixture() GameConfig {
	maxI := int64(65535)
	minI := int64(0)
	return GameConfig{
		ID:             "adventure",
		Name:           "Adventure Game",
		LaunchMode:     "DirectPath",
		Target:         "/opt/example/adventure",
		Args:           []string{"--bridge"},
		DefaultProfile: "vanilla",
		Profiles: map[string]ProfileConfig{
			"vanilla": {
				Description: "Untouched user data",
				Args:        []string{"--data-root", "/srv/vanilla"},
			},
			"combat-test": {
				Description: "Isolated combat-test data",
				Args:        []string{"--data-root", "/srv/combat"},
				Env:         map[string]string{"CONTENT_SET": "combat"},
				UnsetEnv:    []string{"LEGACY_ROOT"},
				WorkingDir:  "/srv/combat",
			},
		},
		LaunchInputs: map[string]LaunchInputConfig{
			"quickStart": {Description: "Skip menus", Type: "boolean"},
			"scenario": {
				Description: "Pick a scenario",
				Type:        "string",
				Enum:        []string{"arena", "tutorial"},
				Profiles:    []string{"combat-test"},
			},
			"seed": {Description: "World seed", Type: "integer", Minimum: &minI, Maximum: &maxI},
		},
	}
}

// TestDescribeLaunchContextsShowsProfiles covers the reporting gap that made the
// whole feature invisible in the human/model-readable text: games_show printed
// only the game-level args, so neither a human nor an agent could learn that
// profiles or launch inputs existed, even though games_start's own description
// says to discover them there.
func TestDescribeLaunchContextsShowsProfiles(t *testing.T) {
	got := DescribeLaunchContexts(profileFixture())

	for _, want := range []string{
		"vanilla",
		"combat-test",
		"Untouched user data",
		"Isolated combat-test data",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description is missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestDescribeLaunchContextsOmitsArgEnvTemplates pins design/10's explicit
// decision: profile arg/env templates are "noise, not secret" and stay out of
// discovery output. `gabs games doctor` is where effective resolution lives.
func TestDescribeLaunchContextsOmitsArgEnvTemplates(t *testing.T) {
	got := DescribeLaunchContexts(profileFixture())

	for _, leaked := range []string{
		"--data-root",
		"/srv/combat",
		"/srv/vanilla",
		"CONTENT_SET",
		"LEGACY_ROOT",
	} {
		if strings.Contains(got, leaked) {
			t.Errorf("arg/env template %q must not appear in discovery output\n--- got ---\n%s", leaked, got)
		}
	}
}

// TestDescribeLaunchContextsMarksDefault matters because a bare start silently
// selects the default profile; which one that is must be visible.
func TestDescribeLaunchContextsMarksDefault(t *testing.T) {
	got := DescribeLaunchContexts(profileFixture())

	if !strings.Contains(got, "Default Profile: vanilla") {
		t.Errorf("the default profile must be named explicitly\n--- got ---\n%s", got)
	}
	if !strings.Contains(got, "vanilla (default)") {
		t.Errorf("the default must also be marked in the profile list\n--- got ---\n%s", got)
	}
	if strings.Contains(got, "combat-test (default)") {
		t.Errorf("only the configured default may carry the marker\n--- got ---\n%s", got)
	}
}

// TestDescribeLaunchContextsShowsInputConstraints keeps every declared bound in
// the text, so a caller can form a valid value without trial calls.
func TestDescribeLaunchContextsShowsInputConstraints(t *testing.T) {
	got := DescribeLaunchContexts(profileFixture())

	for _, want := range []string{
		"quickStart", "boolean", "Skip menus",
		"scenario", "string", "arena", "tutorial",
		"seed", "integer", "0", "65535",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description is missing %q\n--- got ---\n%s", want, got)
		}
	}
	// An input restricted to certain profiles must say so; supplying it
	// elsewhere is an error.
	if !strings.Contains(got, "combat-test") {
		t.Error("an input's profile restriction must be visible")
	}

	// An enum already enumerates every valid value, so a length bound beside it
	// is noise in the text surface.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "scenario:") && strings.Contains(line, "maxLength") {
			t.Errorf("maxLength is redundant next to an enum, got %q", line)
		}
	}

	// A string input WITHOUT an enum still needs its effective bound stated, so
	// a caller can form a valid value without a trial call (design/10).
	unbounded := GameConfig{
		ID:             "g",
		DefaultProfile: "p",
		Profiles:       map[string]ProfileConfig{"p": {}},
		LaunchInputs: map[string]LaunchInputConfig{
			"note": {Description: "Free text", Type: "string"},
		},
	}
	if desc := DescribeLaunchContexts(unbounded); !strings.Contains(desc, "maxLength") {
		t.Errorf("an enum-less string input must state its effective maxLength\n--- got ---\n%s", desc)
	}
}

// TestDescribeLaunchContextsEmptyForLegacyGames protects the compatibility
// promise: a game without profiles must gain no new output at all.
func TestDescribeLaunchContextsEmptyForLegacyGames(t *testing.T) {
	legacy := GameConfig{
		ID:         "legacy",
		LaunchMode: "DirectPath",
		Target:     "/opt/legacy",
		Args:       []string{"--flag"},
	}
	if got := DescribeLaunchContexts(legacy); got != "" {
		t.Errorf("legacy game must produce no launch-context text, got %q", got)
	}
}

// TestArgumentsLabelSignalsProfileAppending covers the actively misleading part
// of the gap: once savedatafolder-style args move into a profile, an
// "Arguments:" line listing only the game-level base reads as the complete
// launch command line when it no longer is.
func TestArgumentsLabelSignalsProfileAppending(t *testing.T) {
	if label := ArgumentsLabel(profileFixture()); !strings.Contains(label, "Base") {
		t.Errorf("with profiles the args label must mark the list as a base, got %q", label)
	}
	legacy := GameConfig{Args: []string{"--flag"}}
	if label := ArgumentsLabel(legacy); label != "Arguments" {
		t.Errorf("without profiles the label must stay %q, got %q", "Arguments", label)
	}
}
