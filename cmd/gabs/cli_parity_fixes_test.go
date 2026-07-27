package main

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/lifecycle"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestStartCLIEmitsLaunchModeIncompatible pins CLI/MCP outcome parity for the
// documented Stage 1 input: an otherwise well-formed config whose URL-mode
// game carries context fields must yield launch_mode_incompatible, never the
// generic config_invalid the MCP handler would not emit for the same file.
func TestStartCLIEmitsLaunchModeIncompatible(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"u":{"id":"u","name":"U","launchMode":"SteamAppId","target":"12345","stopProcessName":"game.exe","defaultProfile":"fast","profiles":{"fast":{"args":["--fast"]}}}}}`)

	errOut := captureStderr(t, func() {
		if rc := startGameCLI(util.NewLogger("error"), "u", dir, "", nil); rc != 1 {
			t.Errorf("mode-incompatible start must exit 1, got %d", rc)
		}
	})
	if !strings.Contains(errOut, "launch_mode_incompatible") {
		t.Fatalf("the CLI must emit the stable mode-incompatible code:\n%s", errOut)
	}
	if strings.Contains(errOut, "config_invalid") {
		t.Fatalf("the CLI must not relabel the outcome config_invalid:\n%s", errOut)
	}
}

// TestStartCLIPrintsWarningsFromFailedAttempt pins that a failed Stages 1–4
// outcome does not swallow the warnings the attempt earned: an unprobeable
// profile warns, and that warning must reach the CLI's stderr alongside the
// failure code exactly as the MCP frontend surfaces it in startWarnings.
func TestStartCLIPrintsWarningsFromFailedAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh for the unmapped-exit status hook")
	}
	dir := t.TempDir()
	// The status hook exits 3 — mapped to neither running nor stopped, so the
	// probe verdict is unknown and Stage 2 records the unprobeable warning.
	// The target exits immediately, so the accepted attempt fails after it.
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/usr/bin/true","defaultProfile":"p","profiles":{"p":{"lifecycle":{"status":{"command":"/bin/sh","args":["-c","exit 3"],"timeoutSeconds":5}}}}}}}`)

	errOut := captureStderr(t, func() {
		if rc := startGameCLI(util.NewLogger("error"), "g", dir, "", nil); rc != 1 {
			t.Errorf("an immediately-exiting workload must fail the start, got rc %d", rc)
		}
	})
	if !strings.Contains(errOut, "warning: could not probe profile(s)") {
		t.Fatalf("the unprobeable-profile warning must survive the failed attempt:\n%s", errOut)
	}
}

// TestCLIStartErrorWarningsExtraction pins the extraction across every typed
// carrier, including the accepted-attempt wrapper whose classification must
// stay visible through Unwrap.
func TestCLIStartErrorWarningsExtraction(t *testing.T) {
	wrapped := &lifecycle.StartAttemptError{
		Err:      &lifecycle.EndpointUnavailableError{GameID: "g", Err: errors.New("ports exhausted")},
		Warnings: []string{"steam advisory"},
	}
	if ws := cliStartErrorWarnings(wrapped); len(ws) != 1 || ws[0] != "steam advisory" {
		t.Fatalf("wrapper warnings must be extracted, got %v", ws)
	}
	var epErr *lifecycle.EndpointUnavailableError
	if !errors.As(wrapped, &epErr) {
		t.Fatal("the wrapper must keep the underlying classification visible")
	}
	if ws := cliStartErrorWarnings(&lifecycle.UnobservedStartError{Warnings: []string{"w"}}); len(ws) != 1 {
		t.Fatalf("unobserved warnings must be extracted, got %v", ws)
	}
	refusal := &lifecycle.StartRefusalError{
		Refusal:  &process.StartRefusal{Code: "operation_in_progress", Message: "superseded"},
		Warnings: []string{"w"},
	}
	if ws := cliStartErrorWarnings(refusal); len(ws) != 1 {
		t.Fatalf("refusal warnings must be extracted, got %v", ws)
	}
	if ws := cliStartErrorWarnings(&lifecycle.ExitedDuringStartError{Warnings: []string{"w"}}); len(ws) != 1 {
		t.Fatalf("exited warnings must be extracted, got %v", ws)
	}
	if ws := cliStartErrorWarnings(errors.New("bare")); ws != nil {
		t.Fatalf("a bare error carries no warnings, got %v", ws)
	}
}

// TestStopCLIPrintsOutcomeWarnings pins that a verified termination whose
// claim removal failed is not reported as an unqualified success: the only
// explanation lives in outcome.Warnings and must reach stderr.
func TestStopCLIPrintsOutcomeWarnings(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true"}}}`)

	// A provably dead workload PID: spawn a short-lived process and wait it out.
	helper := "true"
	if runtime.GOOS == "windows" {
		helper = "cmd"
	}
	path, err := exec.LookPath(helper)
	if err != nil {
		t.Skipf("no %s helper available: %v", helper, err)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command(path, "/c", "exit")
	} else {
		cmd = exec.Command(path)
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.GamePID = cmd.Process.Pid
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	restore := process.SetRemoveRuntimeStateFailHookForTesting(func() error { return errors.New("simulated removal outage") })
	defer restore()

	rc := -1
	errOut := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			rc = stopGameCLI(util.NewLogger("error"), "g", dir, process.OperationActionStop)
		})
	})
	if rc != 0 {
		t.Fatalf("a verified termination stays exit 0, got %d (stderr: %s)", rc, errOut)
	}
	if !strings.Contains(errOut, "warning:") {
		t.Fatalf("the claim-removal warning must reach stderr:\n%s", errOut)
	}
}

// TestManageGamesAddressesDashPrefixedID pins the "--" escape end to end: a
// canonical dash-prefixed game ID, which the config loader accepts, must be
// addressable as a positional argument behind a literal "--".
func TestManageGamesAddressesDashPrefixedID(t *testing.T) {
	dir := t.TempDir()
	rc := -1
	out := captureStdout(t, func() {
		rc = manageGames(context.Background(), util.NewLogger("error"), options{configDir: dir}, []string{"stop", "--", "-dash"})
	})
	if rc != 0 {
		t.Fatalf("`games stop -- -dash` must address the ID, got rc %d (out: %s)", rc, out)
	}
	if !strings.Contains(out, "-dash is not running") {
		t.Fatalf("the dash-prefixed ID must be treated as the positional game ID:\n%s", out)
	}

	// The POSIX spelling with "--" before the action stays supported: the
	// action is the first operand, the dash-prefixed ID the second.
	out = captureStdout(t, func() {
		rc = manageGames(context.Background(), util.NewLogger("error"), options{configDir: dir}, []string{"--", "stop", "-dash"})
	})
	if rc != 0 || !strings.Contains(out, "-dash is not running") {
		t.Fatalf("`games -- stop -dash` must address the ID, got rc %d:\n%s", rc, out)
	}
}

// TestEscapedTokensNeverReenterActionFlagParsers pins the "--" boundary for
// the self-parsing actions: every token after "--" is a positional, so a
// flag-shaped token there must never reach the action's flag parser — a
// re-interpreted --forget-runtime would follow the destructive path without
// its confirmation.
func TestEscapedTokensNeverReenterActionFlagParsers(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true"}}}`)
	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	rc := -1
	errOut := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			rc = manageGames(context.Background(), util.NewLogger("error"), options{configDir: dir},
				[]string{"repair", "g", "--", "--forget-runtime", "--yes"})
		})
	})
	if rc != 2 {
		t.Fatalf("escaped flag-shaped tokens must be rejected loudly, got rc %d (stderr: %s)", rc, errOut)
	}
	if !process.RuntimeClaimExists("g", dir) {
		t.Fatal("the claim must be untouched — the destructive forget path must never run from escaped tokens")
	}
}

// TestEscapedIDFillsThePositionalSlot pins the merge order: with flags before
// "--" and the dash-prefixed ID after it, the ID lands in the positional slot
// and the flags stay flags.
func TestEscapedIDFillsThePositionalSlot(t *testing.T) {
	dir := t.TempDir()
	rc := -1
	errOut := captureStderr(t, func() {
		rc = manageGames(context.Background(), util.NewLogger("error"), options{configDir: dir},
			[]string{"start", "--profile", "fast", "--", "-dash"})
	})
	if rc != 1 || !strings.Contains(errOut, "game_not_found") {
		t.Fatalf("`start --profile fast -- -dash` must treat -dash as the game ID (game_not_found), got rc %d:\n%s", rc, errOut)
	}
}
