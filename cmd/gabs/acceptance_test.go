package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestAcceptanceTwoProfileIsolation is the T-ACC core (design/30): two profiles
// of ONE game, launched sequentially, must isolate argv, env, and cwd, and each
// launch must report its activeProfile in the claim. A neutral recorder target
// writes its actual argv/env/cwd, so the assertion is on what the OS process
// really received — not on config echoing.
func TestAcceptanceTwoProfileIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix recorder script; the cmd acceptance tests run on the unix CI lanes")
	}
	dir := t.TempDir()
	log := util.NewLogger("error")

	// Separate per-profile data directories (the "separate temp data dirs" cell).
	dirA := filepath.Join(dir, "data-alpha")
	dirB := filepath.Join(dir, "data-beta")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	recA := filepath.Join(dir, "alpha.rec")
	recB := filepath.Join(dir, "beta.rec")

	// The recorder writes its real argv/env/cwd, then stays alive.
	rec := filepath.Join(dir, "rec.sh")
	if err := os.WriteFile(rec, []byte("#!/bin/sh\n{ echo \"argv=$*\"; echo \"tag=$ACCEPT_TAG\"; echo \"pwd=$PWD\"; } > \"$REC_OUT\"\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeCLIConfig(t, dir, `{"version":"1.0","games":{"svc":{"id":"svc","name":"Svc","launchMode":"DirectPath","target":"`+rec+`","defaultProfile":"alpha","profiles":{
		"alpha":{"args":["--mode","alpha"],"workingDir":"`+dirA+`","env":{"ACCEPT_TAG":"alpha","REC_OUT":"`+recA+`"}},
		"beta":{"args":["--mode","beta"],"workingDir":"`+dirB+`","env":{"ACCEPT_TAG":"beta","REC_OUT":"`+recB+`"}}
	}}}}`)

	launchProfile := func(profile, recFile string) map[string]string {
		if code := startGameCLI(log, "svc", dir, profile, nil); code != 0 {
			t.Fatalf("start --profile %s exit = %d", profile, code)
		}
		claim, _ := process.LoadRuntimeState("svc", dir)
		if claim == nil {
			t.Fatalf("no claim after start --profile %s", profile)
		}
		registerClaimCleanup(t, claim)
		if claim.Profile != profile {
			t.Fatalf("activeProfile: claim.Profile = %q, want %q", claim.Profile, profile)
		}
		rec := waitForRecord(t, recFile)
		if code := stopGameCLI(log, "svc", dir, process.OperationActionStop); code != 0 {
			t.Fatalf("stop after --profile %s exit = %d", profile, code)
		}
		return rec
	}

	a := launchProfile("alpha", recA)
	b := launchProfile("beta", recB)

	// argv isolation
	if !strings.Contains(a["argv"], "--mode alpha") || !strings.Contains(b["argv"], "--mode beta") {
		t.Fatalf("argv not isolated: alpha=%q beta=%q", a["argv"], b["argv"])
	}
	// env isolation
	if a["tag"] != "alpha" || b["tag"] != "beta" {
		t.Fatalf("env not isolated: alpha tag=%q beta tag=%q", a["tag"], b["tag"])
	}
	// cwd isolation (resolve symlinks — a shell's $PWD is the real path, and on
	// macOS /var is a symlink to /private/var).
	wantA, _ := filepath.EvalSymlinks(dirA)
	wantB, _ := filepath.EvalSymlinks(dirB)
	if a["pwd"] != wantA || b["pwd"] != wantB {
		t.Fatalf("cwd not isolated: alpha pwd=%q (want %q), beta pwd=%q (want %q)", a["pwd"], wantA, b["pwd"], wantB)
	}
}

// TestAcceptanceEarlyCrashSurfacesExitAndTail is the T-ACC early-crash cell: a
// target that exits non-zero with stderr must surface exited_during_start with
// the exit code and the captured output tail, end-to-end through the CLI.
func TestAcceptanceEarlyCrashSurfacesExitAndTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix crash script; the cmd acceptance tests run on the unix CI lanes")
	}
	dir := t.TempDir()
	log := util.NewLogger("error")
	crash := filepath.Join(dir, "crash.sh")
	if err := os.WriteFile(crash, []byte("#!/bin/sh\necho 'boom: could not open data file' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"crash":{"id":"crash","name":"Crash","launchMode":"DirectPath","target":"`+crash+`"}}}`)

	var code int
	errOut := captureStderr(t, func() { code = startGameCLI(log, "crash", dir, "", nil) })
	if code == 0 {
		t.Fatal("a target that exits 3 must fail the start")
	}
	if !strings.Contains(errOut, "exited_during_start") || !strings.Contains(errOut, "exit code 3") {
		t.Fatalf("early crash must surface exited_during_start + exit code 3:\n%s", errOut)
	}
	if !strings.Contains(errOut, "boom: could not open data file") {
		t.Fatalf("the output tail must carry the workload's stderr:\n%s", errOut)
	}
	if process.RuntimeClaimExists("crash", dir) {
		t.Fatal("a crashed start must not leave a runtime claim")
	}
}

// TestAcceptanceRenameProfileWithoutRestart is the T-ACC rename cell: renaming a
// profile on disk and launching the new name must work without restarting GABS
// or the client. Each CLI invocation reads the config fresh (the same
// source-of-truth the server hot-reloads), so the renamed profile launches.
func TestAcceptanceRenameProfileWithoutRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleeper; the cmd acceptance tests run on the unix CI lanes")
	}
	dir := t.TempDir()
	log := util.NewLogger("error")
	sleeper := filepath.Join(dir, "s.sh")
	if err := os.WriteFile(sleeper, []byte("#!/bin/sh\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"svc":{"id":"svc","name":"Svc","launchMode":"DirectPath","target":"`+sleeper+`","defaultProfile":"beta","profiles":{"beta":{"args":["--beta"]}}}}}`)

	// Rename the profile on disk: beta -> gamma. No process is restarted.
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"svc":{"id":"svc","name":"Svc","launchMode":"DirectPath","target":"`+sleeper+`","defaultProfile":"gamma","profiles":{"gamma":{"args":["--gamma"]}}}}}`)

	if code := startGameCLI(log, "svc", dir, "gamma", nil); code != 0 {
		t.Fatalf("the renamed profile must launch without a restart, exit = %d", code)
	}
	claim, _ := process.LoadRuntimeState("svc", dir)
	if claim == nil {
		t.Fatal("no claim after launching the renamed profile")
	}
	registerClaimCleanup(t, claim)
	if claim.Profile != "gamma" {
		t.Fatalf("claim.Profile = %q, want gamma", claim.Profile)
	}
	// The old name is gone: launching it now fails to resolve.
	if code := startGameCLI(log, "svc", dir, "beta", nil); code == 0 {
		t.Fatal("the old profile name must no longer resolve after the rename")
	}
	_ = stopGameCLI(log, "svc", dir, process.OperationActionStop)
}

// --- helpers ---

func waitForRecord(t *testing.T, path string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			out := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					out[k] = v
				}
			}
			if len(out) >= 3 {
				return out
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorder never wrote %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}
