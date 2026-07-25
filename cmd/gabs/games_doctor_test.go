package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func TestConflationWarning(t *testing.T) {
	if conflationWarning(&launch.ResolvedHook{Command: "docker"}) == "" {
		t.Fatal("a docker status hook must produce a conflation advisory")
	}
	if conflationWarning(&launch.ResolvedHook{Command: "/usr/local/bin/podman"}) == "" {
		t.Fatal("podman conflates the same way and must warn")
	}
	if conflationWarning(&launch.ResolvedHook{Command: "/opt/status-wrapper.sh"}) != "" {
		t.Fatal("a wrapper script (non-conflating basename) must not warn")
	}
	if conflationWarning(nil) != "" {
		t.Fatal("a nil hook must not warn")
	}
}

// doctor is profile-aware: it validates and reports every launchable context,
// and flags a docker-style conflating status hook (advisory) — design/11.
func TestDoctorProfileAwareAndConflationLint(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","defaultProfile":"fast","lifecycle":{"status":{"command":"docker","args":["inspect","g"]}},"profiles":{"fast":{"args":["--fast"]},"slow":{"args":["--slow"]}}}}}`)
	log := util.NewLogger("error")

	out := captureStdout(t, func() { runDoctor(log, "g", dir, false) })
	// Assert on basename-robust signals: the resolver may expand "docker" to an
	// absolute path (e.g. /usr/bin/docker) on a host that has it installed, so
	// the raw command line is environment-dependent, but the conflation lint is
	// basename-matched and stable.
	for _, want := range []string{`profile "fast"`, `profile "slow"`, "default context", "status hook:", "conflates 'stopped'"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// An invalid config is itself a finding (its ValidationError names the JSON
// path), and doctor still prints the track record — no early return (design/11).
func TestDoctorInvalidConfigStillReportsTrackRecord(t *testing.T) {
	dir := t.TempDir()
	// profiles configured without the required defaultProfile.
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","profiles":{"a":{}}}}}`)
	log := util.NewLogger("error")

	var code int
	out := captureStdout(t, func() { code = runDoctor(log, "g", dir, false) })
	if code != 1 {
		t.Fatalf("an invalid config must make doctor exit 1, got %d", code)
	}
	if !strings.Contains(out, "Configuration: invalid") || !strings.Contains(out, "defaultProfile") {
		t.Fatalf("doctor must name the config validation problem:\n%s", out)
	}
	if !strings.Contains(out, "Track record") {
		t.Fatalf("doctor must still print the track record after a config finding:\n%s", out)
	}
}

func TestDoctorBroadlyReadableWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are meaningless on Windows (NTFS ACLs govern)")
	}
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true"}}}`)
	// writeCLIConfig writes 0600; loosen it so the warning fires.
	if err := os.Chmod(filepath.Join(dir, "config.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := util.NewLogger("error")
	out := captureStdout(t, func() { runDoctor(log, "g", dir, false) })
	if !strings.Contains(out, "broadly readable") {
		t.Fatalf("a 0644 config must trigger the broadly-readable warning:\n%s", out)
	}
}

// doctor prints the full track record and --show-last-good prints the proven
// context after a verified start (design/08, design/11).
func TestDoctorTrackRecordAndLastGood(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleeper; the cmd lifecycle tests run on the unix CI lanes")
	}
	dir, gameID := setupSleeperGame(t)
	log := util.NewLogger("error")

	if code := startGameCLI(log, gameID, dir, "", nil); code != 0 {
		t.Fatalf("start exit = %d", code)
	}
	claim, _ := process.LoadRuntimeState(gameID, dir)
	if claim != nil {
		registerClaimCleanup(t, claim)
	}

	out := captureStdout(t, func() { runDoctor(log, gameID, dir, true) })
	if !strings.Contains(out, "Track record:") || !strings.Contains(out, "started 1") {
		t.Fatalf("doctor must show the verified start in the track record:\n%s", out)
	}
	if !strings.Contains(out, "Last known good:") || !strings.Contains(out, "context hash:") {
		t.Fatalf("--show-last-good must print the proven context:\n%s", out)
	}

	if code := stopGameCLI(log, gameID, dir, process.OperationActionStop); code != 0 {
		t.Fatalf("stop exit = %d", code)
	}
}
