package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestGamesStartStampsBridgeDiagnosticsAtSpawn closes the loop through the
// real start handler: a games_start that SPAWNS stamps bridge.json with the
// resolved profile, config revision, and startedAt (design/20, at spawn). The
// flash target spawns (then exits immediately), so the spawn boundary is
// crossed and the diagnostics are written.
func TestGamesStartStampsBridgeDiagnosticsAtSpawn(t *testing.T) {
	s := newProfiledServer(t)
	callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "profile": "combat", "timeout": 1,
	})

	cp, err := config.NewConfigPaths(s.configDir)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(cp.GetBridgeConfigPath("adventure"))
	if err != nil {
		t.Fatalf("bridge.json must exist after a start: %v", err)
	}
	var b config.BridgeJSON
	if err := json.Unmarshal(blob, &b); err != nil {
		t.Fatal(err)
	}
	if b.Profile != "combat" {
		t.Fatalf("bridge.json must record the resolved profile, got %q", b.Profile)
	}
	if b.ConfigRevision == "" || b.StartedAt == "" {
		t.Fatalf("bridge.json must record the config revision and startedAt: %+v", b)
	}
	if _, perr := time.Parse(time.RFC3339, b.StartedAt); perr != nil {
		t.Fatalf("startedAt must be RFC3339: %v", perr)
	}
}

// TestPreSpawnFailurePublishesNoDiagnostics is the P2-8 lock: a start that
// fails BEFORE OS process creation (spec_too_large) must leave bridge.json
// with the endpoint but NO profile/revision/startedAt — nothing was spawned,
// so nothing may claim to have started.
func TestPreSpawnFailurePublishesNoDiagnostics(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	game := config.GameConfig{
		ID: "big", Name: "Big", LaunchMode: "DirectPath", Target: exe,
		Args: definitelyOversizedExecArgs(),
	}
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0", Games: map[string]config.GameConfig{game.ID: game},
	}, 0, 0)

	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": game.ID, "timeout": 1})
	if structured["code"] != "spec_too_large" {
		t.Fatalf("setup: expected spec_too_large, got %v", structured["code"])
	}

	cp, _ := config.NewConfigPaths(dir)
	blob, err := os.ReadFile(cp.GetBridgeConfigPath("big"))
	if err != nil {
		t.Fatalf("endpoint bridge.json should still exist: %v", err)
	}
	var b config.BridgeJSON
	if err := json.Unmarshal(blob, &b); err != nil {
		t.Fatal(err)
	}
	if b.Profile != "" || b.ConfigRevision != "" || b.StartedAt != "" {
		t.Fatalf("a pre-spawn failure must publish NO diagnostics: %+v", b)
	}
}

// definitelyOversizedExecArgs exceeds every supported platform's combined
// hard limit without relying on Linux's page-size-dependent per-string cap:
// Darwin is 1 MiB, Linux is capped at 6 MiB, and Windows is far smaller.
func definitelyOversizedExecArgs() []string {
	args := make([]string, 65)
	for i := range args {
		args[i] = strings.Repeat("x", 100*1024)
	}
	return args
}

// TestBridgeJSONDiagnosticsNeverReachLivePath is the M2.11 env-only lock
// (design/03 §"Files are diagnostic, never live handoff"). bridge.json now
// carries profile/configRevision/startedAt, but the LIVE contract stays
// env-only: every live decision (status, attach, attribution) reads the
// CLAIM, never the file's profile/configRevision/startedAt fields. This
// passes today because nothing reads them — which is the point: it fails the
// moment a future change wires a bridge.json diagnostic field into a live path.
func TestBridgeJSONDiagnosticsNeverReachLivePath(t *testing.T) {
	s := newProfiledServer(t)
	dir := s.configDir

	// A running claim whose profile/revision are the source of truth.
	spec := process.LaunchSpec{
		GameId: "adventure", Mode: "DirectPath", PathOrId: "/opt/game",
		Profile: "combat", ConfigRevision: "claim-rev-7",
	}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.GamePID = os.Getpid()
	if start, err := process.ProcessStartTime(os.Getpid()); err == nil {
		st.PIDStartTime = start
	}
	if err := process.ClaimRuntimeState("adventure", dir, st); err != nil {
		t.Fatal(err)
	}

	// A bridge.json whose diagnostic fields deliberately disagree with the
	// claim. If any live path reads them, the bogus markers leak.
	cp, err := config.NewConfigPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	bogus := config.BridgeJSON{
		Port: 55555, Token: "tok", GameId: "adventure",
		Profile: "BOGUS-PROFILE-XYZ", ConfigRevision: "BOGUS-REV-XYZ", StartedAt: "BOGUS-TIME",
	}
	blob, _ := json.MarshalIndent(bogus, "", "  ")
	if err := os.WriteFile(cp.GetBridgeConfigPath("adventure"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	raw, structured := callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})

	// The live path reports the CLAIM's profile and revision...
	if structured["activeProfile"] != "combat" {
		t.Fatalf("activeProfile must come from the claim, got %#v", structured["activeProfile"])
	}
	if structured["activeConfigRevision"] != "claim-rev-7" {
		t.Fatalf("activeConfigRevision must come from the claim, got %#v", structured["activeConfigRevision"])
	}
	// ...and no bridge.json diagnostic field ever reaches the result.
	for _, marker := range []string{"BOGUS-PROFILE-XYZ", "BOGUS-REV-XYZ", "BOGUS-TIME"} {
		if strings.Contains(raw, marker) {
			t.Fatalf("bridge.json diagnostics must never reach a live path (%s leaked): %s", marker, raw)
		}
	}
}
