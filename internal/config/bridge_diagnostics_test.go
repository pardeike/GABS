package config

import "testing"

// M2.11 (design/03 §"Files are diagnostic, never live handoff"): bridge.json
// additionally records the selected profile, config revision, and start time
// for diagnostics/doctor only. These are WRITE-only fields — the live bridge
// contract stays env-only (proven separately in the mcp env-only lock test).

func TestPrepareBridgeEndpointStampsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	diag := BridgeDiagnostics{Profile: "combat", ConfigRevision: "rev-1", StartTime: "2026-07-22T10:00:00Z"}
	_, _, path, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false, diag)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	b, err := readBridgeJSONFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if b.Profile != "combat" || b.ConfigRevision != "rev-1" || b.StartTime != "2026-07-22T10:00:00Z" {
		t.Fatalf("diagnostics not stamped: %+v", b)
	}
}

func TestBridgeReuseRestampsFreshDiagnostics(t *testing.T) {
	dir := t.TempDir()
	first := BridgeDiagnostics{Profile: "vanilla", ConfigRevision: "rev-1", StartTime: "t1"}
	p1, tok1, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false, first)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}

	// A second start of the same game reuses the port with a rotated token —
	// and must restamp the CURRENT launch's diagnostics, never leave the
	// previous launch's profile/revision behind (the misleading-diagnostic
	// the fields exist to prevent).
	second := BridgeDiagnostics{Profile: "combat", ConfigRevision: "rev-2", StartTime: "t2"}
	p2, tok2, path, reused, err := PrepareBridgeEndpointForStart("g", dir, nil, false, second)
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if !reused {
		t.Fatalf("expected endpoint reuse")
	}
	if p2 != p1 {
		t.Fatalf("reuse must keep the port: %d != %d", p2, p1)
	}
	if tok2 == tok1 {
		t.Fatalf("reuse must rotate the token (per-launch credential)")
	}
	b, err := readBridgeJSONFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if b.Profile != "combat" || b.ConfigRevision != "rev-2" || b.StartTime != "t2" {
		t.Fatalf("reuse must restamp FRESH diagnostics, not the stale ones: %+v", b)
	}
}

// Diagnostics must never enter the endpoint-reuse decision: a bridge.json is
// reusable on port/token/gameId alone, whatever its diagnostic fields say.
func TestBridgeDiagnosticsDoNotAffectEndpointReuse(t *testing.T) {
	dir := t.TempDir()
	// A bridge with rich diagnostics and one with none must both be reused.
	if _, _, _, _, err := PrepareBridgeEndpointForStart("rich", dir, nil, false,
		BridgeDiagnostics{Profile: "p", ConfigRevision: "r", StartTime: "t"}); err != nil {
		t.Fatalf("rich prepare: %v", err)
	}
	if _, _, _, reused, err := PrepareBridgeEndpointForStart("rich", dir, nil, false, BridgeDiagnostics{}); err != nil || !reused {
		t.Fatalf("a bridge with diagnostics must still be reusable: reused=%v err=%v", reused, err)
	}
}
