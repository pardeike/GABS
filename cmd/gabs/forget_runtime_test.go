package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/process"
)

// A traversal identifier must never reach a filesystem operation outside the
// config base (round-19 P1): repair ../victim --forget-runtime must not delete
// a sibling of the config dir.
func TestForgetRuntimeClaimRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	victimDir := filepath.Join(root, "victim")
	if err := os.MkdirAll(victimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	victimClaim := filepath.Join(victimDir, "runtime.json")
	if err := os.WriteFile(victimClaim, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	forgetRuntimeClaim("../victim", base, true, strings.NewReader(""), &out)
	if _, err := os.Stat(victimClaim); err != nil {
		t.Fatalf("traversal deleted a file outside the config base: %v (output: %s)", err, out.String())
	}
}

// A symlinked game directory must not redirect removal outside the base
// (round-19 P1 symlink escape).
func TestForgetRuntimeClaimRejectsSymlinkedGameDir(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideClaim := filepath.Join(outside, "runtime.json")
	if err := os.WriteFile(outsideClaim, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "ghost")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	forgetRuntimeClaim("ghost", base, true, strings.NewReader(""), &out)
	if _, err := os.Stat(outsideClaim); err != nil {
		t.Fatalf("a symlinked game dir must not redirect removal outside the base: %v (output: %s)", err, out.String())
	}
}

// A slash-containing game ID (design-legal) must be forgettable end-to-end
// (round-19 P1 regression fix).
func TestForgetRuntimeClaimSlashID(t *testing.T) {
	dir := t.TempDir()
	seedForgetClaim(t, dir, "factory/old")

	var out bytes.Buffer
	if rc := forgetRuntimeClaim("factory/old", dir, false, strings.NewReader("y\n"), &out); rc != 0 {
		t.Fatalf("a slash-ID forget must succeed, rc=%d out=%s", rc, out.String())
	}
	if process.RuntimeClaimExists("factory/old", dir) {
		t.Fatalf("the slash-ID claim must be removed")
	}
}

func seedForgetClaim(t *testing.T, configDir, gameID string) {
	t.Helper()
	spec := process.LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/" + gameID}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	if err := process.ClaimRuntimeState(gameID, configDir, st); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
}

// repair <id> --forget-runtime removes the claim after confirmation (design/07).
func TestForgetRuntimeClaimRemovesAfterConfirmation(t *testing.T) {
	dir := t.TempDir()
	seedForgetClaim(t, dir, "ghost")

	var out bytes.Buffer
	if rc := forgetRuntimeClaim("ghost", dir, false, strings.NewReader("y\n"), &out); rc != 0 {
		t.Fatalf("forget must succeed, rc=%d out=%s", rc, out.String())
	}
	if process.RuntimeClaimExists("ghost", dir) {
		t.Fatalf("the claim must be removed after confirmation")
	}
	if !strings.Contains(out.String(), "Removed the runtime claim") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

// A declined confirmation leaves the claim in place.
func TestForgetRuntimeClaimAbortsOnNo(t *testing.T) {
	dir := t.TempDir()
	seedForgetClaim(t, dir, "ghost")

	var out bytes.Buffer
	if rc := forgetRuntimeClaim("ghost", dir, false, strings.NewReader("n\n"), &out); rc != 1 {
		t.Fatalf("an aborted forget must return 1, rc=%d", rc)
	}
	if !process.RuntimeClaimExists("ghost", dir) {
		t.Fatalf("the claim must survive an aborted forget")
	}
}

// The escape hatch must remove a corrupt/unreadable claim — that is exactly the
// case normal fenced removal cannot handle (design/07:99 + the corrupt-claim
// repair path).
func TestForgetRuntimeClaimRemovesCorruptClaim(t *testing.T) {
	dir := t.TempDir()
	gameDir := filepath.Join(dir, "corrupt-game")
	if err := os.MkdirAll(gameDir, 0o700); err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(gameDir, "runtime.json")
	if err := os.WriteFile(claimPath, []byte("{ this is not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if rc := forgetRuntimeClaim("corrupt-game", dir, true, strings.NewReader(""), &out); rc != 0 {
		t.Fatalf("a corrupt claim must still be forgettable, rc=%d out=%s", rc, out.String())
	}
	if _, err := os.Stat(claimPath); !os.IsNotExist(err) {
		t.Fatalf("the corrupt claim file must be removed")
	}
	if !strings.Contains(out.String(), "corrupt") {
		t.Fatalf("the evidence must note the corrupt claim: %s", out.String())
	}
}
