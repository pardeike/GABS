package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func modeIncompatConfigError(issues ...ConfigIssue) *ConfigError {
	return &ConfigError{Err: &ValidationError{Issues: issues}}
}

// ModeIncompatibleIssues gates the stable launch_mode_incompatible outcome:
// it may fire only when the validation failure is PURELY the selected game's
// launch-mode incompatibility. Anything else anywhere in the config is a
// mixed failure — reporting only the selected game's issues would hide the
// other validation error behind the wrong stable code.
func TestModeIncompatibleIssuesPureFailure(t *testing.T) {
	issues := ModeIncompatibleIssues(modeIncompatConfigError(
		ConfigIssue{Path: "/games/u/profiles", Message: "URL mode rejects profiles", Code: IssueCodeModeIncompatible},
		ConfigIssue{Path: "/games/u/env", Message: "URL mode rejects env", Code: IssueCodeModeIncompatible},
	), "u")
	if len(issues) != 2 {
		t.Fatalf("a purely mode-incompatible failure must return its issues, got %v", issues)
	}
}

func TestModeIncompatibleIssuesMixedWithOtherGame(t *testing.T) {
	issues := ModeIncompatibleIssues(modeIncompatConfigError(
		ConfigIssue{Path: "/games/u/profiles", Message: "URL mode rejects profiles", Code: IssueCodeModeIncompatible},
		ConfigIssue{Path: "/games/v/lifecycle/status/command", Message: "hook command is required"},
	), "u")
	if issues != nil {
		t.Fatalf("another game's validation issue makes this a mixed failure, got %v", issues)
	}
}

func TestModeIncompatibleIssuesMixedWithinSameGame(t *testing.T) {
	issues := ModeIncompatibleIssues(modeIncompatConfigError(
		ConfigIssue{Path: "/games/u/profiles", Message: "URL mode rejects profiles", Code: IssueCodeModeIncompatible},
		ConfigIssue{Path: "/games/u/launchInputs/seed", Message: "input must bind args or env"},
	), "u")
	if issues != nil {
		t.Fatalf("a non-mode issue on the same game makes this a mixed failure, got %v", issues)
	}
}

func TestModeIncompatibleIssuesMixedWithTopLevel(t *testing.T) {
	issues := ModeIncompatibleIssues(modeIncompatConfigError(
		ConfigIssue{Path: "/games/u/profiles", Message: "URL mode rejects profiles", Code: IssueCodeModeIncompatible},
		ConfigIssue{Path: "/timeouts/startup", Message: "processStartSeconds out of range"},
	), "u")
	if issues != nil {
		t.Fatalf("a top-level issue makes this a mixed failure, got %v", issues)
	}
}

// TestEnumMembersValidatedAgainstConstraints pins config/runtime agreement:
// runtime resolution enforces pattern and length on every value including
// enum members, so a member violating them is a choice every start rejects —
// it must fail configuration validation with its exact path.
func TestEnumMembersValidatedAgainstConstraints(t *testing.T) {
	base := `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","defaultProfile":"p","profiles":{"p":{}},"launchInputs":{"mode":{"type":"string","description":"d","args":["${value}"],%s}}}}}`

	badPattern := fmt.Sprintf(base, `"enum":["production"],"pattern":"dev-.*"`)
	if _, err := parseGamesConfig([]byte(badPattern)); err == nil || !strings.Contains(err.Error(), "does not match the declared pattern") {
		t.Fatalf("an enum member violating the pattern must fail validation, got %v", err)
	}

	tooLong := fmt.Sprintf(base, `"enum":["abcdef"],"maxLength":3`)
	if _, err := parseGamesConfig([]byte(tooLong)); err == nil || !strings.Contains(err.Error(), "exceeds maxLength") {
		t.Fatalf("an enum member over maxLength must fail validation, got %v", err)
	}

	consistent := fmt.Sprintf(base, `"enum":["dev-a","dev-b"],"pattern":"dev-.*"`)
	if _, err := parseGamesConfig([]byte(consistent)); err != nil {
		t.Fatalf("consistent enum declarations must load: %v", err)
	}

	// maxLength is defined in Unicode code points (and enforced that way at
	// runtime): "é" is one code point even though it is two UTF-8 bytes.
	unicodeOK := fmt.Sprintf(base, `"enum":["é"],"maxLength":1`)
	if _, err := parseGamesConfig([]byte(unicodeOK)); err != nil {
		t.Fatalf("enum lengths must count code points, not bytes: %v", err)
	}
}

func TestModeIncompatibleIssuesNonValidationError(t *testing.T) {
	if issues := ModeIncompatibleIssues(&ConfigError{Err: errors.New("parse error")}, "u"); issues != nil {
		t.Fatalf("a non-validation config error is never mode-incompatible, got %v", issues)
	}
	if issues := ModeIncompatibleIssues(nil, "u"); issues != nil {
		t.Fatalf("a nil config error is never mode-incompatible, got %v", issues)
	}
}
