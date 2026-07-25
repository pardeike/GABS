package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// --- flag / input parsing (T-CLI: --profile, repeated typed --input, dup name) ---

func TestParseStartFlagsProfileAndInputs(t *testing.T) {
	profile, inputs, err := parseStartFlags([]string{"--profile", "fast", "--input", "seed=42", "--input", "name=alpha"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile != "fast" {
		t.Fatalf("profile = %q, want fast", profile)
	}
	if inputs["seed"] != "42" || inputs["name"] != "alpha" {
		t.Fatalf("inputs = %v", inputs)
	}
}

func TestParseStartFlagsDuplicateInputIsError(t *testing.T) {
	if _, _, err := parseStartFlags([]string{"--input", "seed=1", "--input", "seed=2"}); err == nil {
		t.Fatal("repeating an --input name must be an error")
	}
}

func TestParseStartFlagsRejectsUnknownAndMalformed(t *testing.T) {
	if _, _, err := parseStartFlags([]string{"--bogus"}); err == nil {
		t.Fatal("unknown flag must error")
	}
	if _, _, err := parseStartFlags([]string{"--input", "noequals"}); err == nil {
		t.Fatal("--input without = must error")
	}
	if _, _, err := parseStartFlags([]string{"--profile"}); err == nil {
		t.Fatal("--profile without a value must error")
	}
}

func TestCoerceInputValueTypes(t *testing.T) {
	decl := func(body string) config.LaunchInputConfig {
		var d config.LaunchInputConfig
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatalf("decode decl: %v", err)
		}
		return d
	}
	if v, err := coerceInputValue(decl(`{"type":"boolean"}`), "true"); err != nil || v != true {
		t.Fatalf("boolean coercion: v=%v err=%v", v, err)
	}
	if _, err := coerceInputValue(decl(`{"type":"boolean"}`), "notabool"); err == nil {
		t.Fatal("a non-boolean value must be rejected")
	}
	if v, err := coerceInputValue(decl(`{"type":"integer"}`), "42"); err != nil {
		t.Fatalf("integer coercion err=%v", err)
	} else if _, ok := v.(json.Number); !ok {
		t.Fatalf("integer value must be json.Number, got %T", v)
	}
	if v, err := coerceInputValue(decl(`{"type":"string"}`), "hello"); err != nil || v != "hello" {
		t.Fatalf("string coercion: v=%v err=%v", v, err)
	}
}

// --- end-to-end lifecycle (T-CLI: Stages 1–4, started_attachment_deferred,
// cross-process status/stop from the persisted snapshot) ---

func TestCLIGamesStartStatusStopCycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleeper; the cmd package's lifecycle tests run on the unix CI lanes")
	}
	dir, gameID := setupSleeperGame(t)
	log := util.NewLogger("error")

	out := captureStdout(t, func() {
		if code := startGameCLI(log, gameID, dir, "", nil); code != 0 {
			t.Fatalf("start exit = %d", code)
		}
	})
	if !strings.Contains(out, "started_attachment_deferred") {
		t.Fatalf("start must report started_attachment_deferred, got: %s", out)
	}

	// The claim is left ready for a later server games_connect: phase active,
	// the per-launch endpoint persisted, and NO attachment yet (a one-shot CLI
	// never held the GABP socket). The verified start was credited.
	claim, err := process.LoadRuntimeState(gameID, dir)
	if err != nil || claim == nil {
		t.Fatalf("expected an active claim, err=%v claim=%v", err, claim)
	}
	registerClaimCleanup(t, claim)
	if claim.Phase != process.PhaseActive {
		t.Fatalf("claim phase = %q, want active", claim.Phase)
	}
	if claim.Endpoint == nil || claim.Endpoint.Port == 0 || claim.Endpoint.Token == "" {
		t.Fatalf("claim must carry the bridge endpoint for a later attach, got %+v", claim.Endpoint)
	}
	if claim.Attachment != nil {
		t.Fatalf("a CLI start must defer attachment; claim.Attachment should be nil, got %+v", claim.Attachment)
	}
	if h, herr := process.LoadHistory(gameID, dir); herr != nil || h == nil || h.Profiles[""] == nil || h.Profiles[""].WorkloadStarts < 1 {
		t.Fatalf("the verified start must credit workloadStarts, history=%+v err=%v", h, herr)
	}

	// status works from the persisted snapshot in a FRESH manager (the original
	// CLI process has exited): running via PID-fingerprint evidence.
	if code := statusGameCLI(log, gameID, dir); code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	if v, _ := process.VerifyPIDFingerprint(claim.GamePID, claim.PIDStartTime); v != process.StatusRunning {
		t.Fatalf("workload pid %d should be running after a CLI start", claim.GamePID)
	}

	// stop from the snapshot after the starting process exited.
	if code := stopGameCLI(log, gameID, dir, process.OperationActionStop); code != 0 {
		t.Fatalf("stop exit = %d", code)
	}
	if process.RuntimeClaimExists(gameID, dir) {
		t.Fatal("a verified stop must remove the runtime claim")
	}
	if v, _ := process.VerifyPIDFingerprint(claim.GamePID, claim.PIDStartTime); v == process.StatusRunning {
		t.Fatalf("workload pid %d should be gone after stop", claim.GamePID)
	}
}

func TestCLIGamesKillFromSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleeper; the cmd package's lifecycle tests run on the unix CI lanes")
	}
	dir, gameID := setupSleeperGame(t)
	log := util.NewLogger("error")

	if code := startGameCLI(log, gameID, dir, "", nil); code != 0 {
		t.Fatalf("start exit = %d", code)
	}
	claim, _ := process.LoadRuntimeState(gameID, dir)
	if claim == nil {
		t.Fatal("no claim after start")
	}
	registerClaimCleanup(t, claim)

	if code := stopGameCLI(log, gameID, dir, process.OperationActionKill); code != 0 {
		t.Fatalf("kill exit = %d", code)
	}
	if process.RuntimeClaimExists(gameID, dir) {
		t.Fatal("a verified kill must remove the runtime claim")
	}
	if v, _ := process.VerifyPIDFingerprint(claim.GamePID, claim.PIDStartTime); v == process.StatusRunning {
		t.Fatalf("workload pid %d should be gone after kill", claim.GamePID)
	}
}

func TestCLIGamesStartRejectsBadTypedInput(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","launchInputs":{"seed":{"type":"integer"}}}}}`)
	log := util.NewLogger("error")
	// A non-integer value for an integer input is rejected before any spawn.
	if code := startGameCLI(log, "g", dir, "", map[string]string{"seed": "abc"}); code == 0 {
		t.Fatal("a non-integer value for an integer input must fail the start")
	}
	if process.RuntimeClaimExists("g", dir) {
		t.Fatal("a rejected input must not leave a runtime claim")
	}
}

func TestCLIGamesStatusUnknownGameIsStopped(t *testing.T) {
	dir := t.TempDir()
	log := util.NewLogger("error")
	// No config, no claim: a clean "stopped" (exit 0), never a crash.
	if code := statusGameCLI(log, "ghost", dir); code != 0 {
		t.Fatalf("status of an unknown game should exit 0, got %d", code)
	}
}

// --- helpers ---

func setupSleeperGame(t *testing.T) (configDir, gameID string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "sleeper.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"`+script+`"}}}`)
	return dir, "g"
}

func writeCLIConfig(t *testing.T, dir, jsonBody string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(jsonBody), 0o600); err != nil {
		t.Fatal(err)
	}
}

// registerClaimCleanup kills a still-live workload when a test fails midway, so
// no sleeper leaks across the suite.
func registerClaimCleanup(t *testing.T, claim *process.RuntimeState) {
	t.Helper()
	pid, start := claim.GamePID, claim.PIDStartTime
	t.Cleanup(func() {
		if pid <= 0 {
			return
		}
		if v, _ := process.VerifyPIDFingerprint(pid, start); v == process.StatusRunning {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
