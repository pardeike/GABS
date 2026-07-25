package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/process"
)

// TestCLISubprocessHelper is the portable long-lived "game" that the real gabs
// binary launches during TestCLICrossProcessLifecycle. It only sleeps when GABS
// injected its environment (GABS_GAME_ID is set on a spawned game); under a
// normal `go test` run the guard makes it a no-op. Being the test binary itself
// keeps the game target portable across all three OSes (no /bin/sh, no sleep).
func TestCLISubprocessHelper(t *testing.T) {
	if os.Getenv("GABS_GAME_ID") == "" {
		return
	}
	time.Sleep(120 * time.Second)
}

// TestCLICrossProcessLifecycle is the true cross-OS-process T-CLI: it builds the
// real gabs binary and drives `games start`, `games status`, and `games stop`
// as SEPARATE processes. The claim written by the start process must be read by
// an independent status process and cleared by an independent stop process —
// nothing is shared in memory. Portable: runs on the unix and Windows lanes.
func TestCLICrossProcessLifecycle(t *testing.T) {
	gabs := buildGabs(t)
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// The game target is this test binary, run as the guarded helper.
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":`+jsonString(testBin)+`,"args":["-test.run=TestCLISubprocessHelper"]}}}`)

	run := func(args ...string) (string, int) {
		full := append([]string{"games", "--configDir", dir}, args...)
		cmd := exec.Command(gabs, full...)
		// Do not inherit GABS_GAME_ID from a parent (there is none here), and
		// let the child helper inherit whatever GABS injects.
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("running gabs %v: %v", args, err)
		}
		return string(out), code
	}

	// start (process 1)
	out, code := run("start", "g")
	if code != 0 || !strings.Contains(out, "started_attachment_deferred") {
		t.Fatalf("start: code=%d out=%s", code, out)
	}

	// Ensure the spawned helper is cleaned up even if a later step fails.
	if claim, _ := process.LoadRuntimeState("g", dir); claim != nil {
		pid, start := claim.GamePID, claim.PIDStartTime
		t.Cleanup(func() {
			if v, _ := process.VerifyPIDFingerprint(pid, start); v == process.StatusRunning {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
		})
	}

	// status (process 2, independent) reads the persisted claim -> running
	out, code = run("status", "g")
	if code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("cross-process status must read running: code=%d out=%s", code, out)
	}

	// stop (process 3, independent) clears the claim
	out, code = run("stop", "g")
	if code != 0 {
		t.Fatalf("cross-process stop: code=%d out=%s", code, out)
	}
	if process.RuntimeClaimExists("g", dir) {
		t.Fatalf("stop must remove the claim; out=%s", out)
	}

	// status again -> stopped
	out, code = run("status", "g")
	if code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("post-stop status must read stopped: code=%d out=%s", code, out)
	}
}

func buildGabs(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gabs")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "github.com/pardeike/gabs/cmd/gabs")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building gabs: %v\n%s", err, out)
	}
	return bin
}

// jsonString quotes a path as a JSON string literal (handles Windows
// backslashes) for embedding in the test config.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
