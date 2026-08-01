package process

import (
	"testing"
	"time"
)

// legacyClaim writes a pre-profile (schema-0) runtime record like the ones
// pre-upgrade GABS versions produced: ownership + PID + name, nothing else.
func legacyClaim(t *testing.T, gameID, dir string, gamePID int, stopName string) {
	t.Helper()
	st := RuntimeState{
		GameID:          gameID,
		Status:          RuntimeStateStatusRunning,
		OwnerPID:        12345,
		GamePID:         gamePID,
		StopProcessName: stopName,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := SaveRuntimeState(gameID, dir, st); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeLegacyClaimFullContract(t *testing.T) {
	dir := t.TempDir()
	legacyClaim(t, "g1", dir, 4242, "legacy-name")

	normalized, err := NormalizeLegacyClaim("g1", dir, "SteamAppId", "sha256:rev12345678")
	if err != nil {
		t.Fatalf("normalization failed: %v", err)
	}
	if normalized.SchemaVersion != RuntimeSchemaVersion {
		t.Fatalf("schema marker must be stamped: %+v", normalized)
	}
	if len(normalized.LaunchID) < 16 || normalized.Generation == 0 {
		t.Fatalf("launch ID and generation must be minted (fencing valid from now on): %+v", normalized)
	}
	if normalized.Phase != PhaseActive || normalized.Profile != "" || normalized.Source != SourceGABS {
		t.Fatalf("phase active + unprofiled expected: %+v", normalized)
	}
	// The single recorded exception to never-consult-config: launch mode
	// and PID role come from the current entry.
	if normalized.LaunchMode != "SteamAppId" || normalized.PIDRole != PIDRoleHelper {
		t.Fatalf("mode/role must come from the current config entry: %+v", normalized)
	}
	// The built-in fallback pins from the legacy claim's own values.
	if normalized.StopProcessName != "legacy-name" || normalized.GamePID != 4242 {
		t.Fatalf("fallback must pin from the old claim: %+v", normalized)
	}
	if normalized.PIDStartTime != 0 {
		t.Fatalf("no fingerprint exists for a legacy PID; it stays weak evidence: %+v", normalized)
	}
	if normalized.BuiltinFallback == nil {
		t.Fatalf("the graceful/force strategy must be pinned: %+v", normalized)
	}
	if !normalized.NormalizedFromLegacy || normalized.ConfigRevision != "sha256:rev12345678" {
		t.Fatalf("normalizedFromLegacy + revision must be recorded: %+v", normalized)
	}
	if normalized.AppliedInputsState != "" || len(normalized.AppliedInputNames) != 0 {
		t.Fatalf("a pre-profile launch used a known empty input set, never 'unavailable': %+v", normalized)
	}

	// Persisted, not just returned.
	loaded, _ := LoadRuntimeState("g1", dir)
	if loaded == nil || loaded.LaunchID != normalized.LaunchID || !loaded.NormalizedFromLegacy {
		t.Fatalf("normalization must persist: %+v", loaded)
	}
}

func TestNormalizeLegacyClaimIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	legacyClaim(t, "g1", dir, 0, "")

	first, err := NormalizeLegacyClaim("g1", dir, "DirectPath", "sha256:rev1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeLegacyClaim("g1", dir, "DirectPath", "sha256:rev2")
	if err != nil {
		t.Fatal(err)
	}
	if second.LaunchID != first.LaunchID {
		t.Fatalf("a second touch must not re-mint the launch identity: %q vs %q", second.LaunchID, first.LaunchID)
	}
	if second.ConfigRevision != first.ConfigRevision {
		t.Fatalf("a second touch must not rewrite the recorded revision: %+v", second)
	}
}

func TestNormalizeLegacyClaimLeavesCurrentSchemaAlone(t *testing.T) {
	dir := t.TempDir()
	st := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	publishClaim(t, dir, st)

	got, err := NormalizeLegacyClaim("g1", dir, "SteamAppId", "sha256:other")
	if err != nil {
		t.Fatal(err)
	}
	if got.LaunchID != st.LaunchID || got.NormalizedFromLegacy || got.LaunchMode != "DirectPath" {
		t.Fatalf("a marker-stamped claim must never enter the migration path: %+v", got)
	}
}
