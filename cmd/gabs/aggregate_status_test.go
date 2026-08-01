package main

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// Finding 6 (round 6): a malformed/unreadable config must NOT be reported as a
// complete, successful summary. Aggregate status surfaces the failure and
// returns nonzero rather than printing "No games configured" and exiting 0.
func TestAggregateStatusSurfacesConfigError(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{ this is not valid json `)
	log := util.NewLogger("error")

	var code int
	errOut := captureStderr(t, func() { code = statusGameCLI(log, "", dir) })
	if code == 0 {
		t.Fatal("a malformed config must make aggregate status return nonzero")
	}
	if !strings.Contains(errOut, "configuration could not be read") {
		t.Fatalf("the config failure must be surfaced on stderr, got: %q", errOut)
	}
}

// Finding 7 (round 6): if any row's runtime claim is unreadable, printOneStatus
// returns 1 — the aggregate must propagate that, not unconditionally return 0.
func TestAggregateStatusPropagatesRowFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file read-permission blocking is a POSIX behavior")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file read permissions")
	}
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true"}}}`)

	// A real claim on disk, then make runtime.json unreadable so the per-row
	// status read fails (not merely "stopped").
	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.LaunchID = "L1"
	st.Phase = process.PhaseActive
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}
	cp, err := config.NewConfigPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	claimPath, err := cp.SafeRuntimeStatePath("g")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(claimPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(claimPath, 0o600) })

	log := util.NewLogger("error")
	var code int
	_ = captureStdout(t, func() { code = statusGameCLI(log, "", dir) })
	if code == 0 {
		t.Fatal("an unreadable per-row claim must make aggregate status return nonzero")
	}
}
