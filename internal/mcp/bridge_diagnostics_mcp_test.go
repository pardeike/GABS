package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
)

// TestGamesStartStampsBridgeDiagnostics closes the loop through the real
// start handler: a games_start writes bridge.json with the RESOLVED profile
// and config revision (design/03). The endpoint file is written before spawn,
// so it carries the diagnostics even when the flash target exits immediately.
func TestGamesStartStampsBridgeDiagnostics(t *testing.T) {
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
	if b.ConfigRevision == "" || b.StartTime == "" {
		t.Fatalf("bridge.json must record the config revision and start time: %+v", b)
	}
}

// TestBridgeJSONDiagnosticsNeverReachLivePath is the M2.11 env-only lock
// (design/03 §"Files are diagnostic, never live handoff"). bridge.json now
// carries profile/configRevision/startTime, but the LIVE contract stays
// env-only: every live decision (status, attach, attribution) reads the
// CLAIM, never the file's diagnostic fields. This passes today because
// nothing reads them — which is the point: it fails the moment a future
// change wires a bridge.json diagnostic field into a live path.
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
		Profile: "BOGUS-PROFILE-XYZ", ConfigRevision: "BOGUS-REV-XYZ", StartTime: "BOGUS-TIME",
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
