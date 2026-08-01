package config

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func readRawBridgeJSON(cfgPath string) (map[string]interface{}, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// M2.11 (design/03 §"Files are diagnostic, never live handoff"; design/20:235):
// bridge.json records profile, configRevision, and startedAt (RFC3339) for
// diagnostics/doctor only, STAMPED AT SPAWN (round 11 P2-8) — the endpoint
// preparation writes only port/token. These are write-only fields; the live
// bridge contract stays env-only (proven in the mcp env-only lock test).

func TestPrepareBridgeEndpointWritesNoDiagnostics(t *testing.T) {
	dir := t.TempDir()
	_, _, path, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	b, err := readBridgeJSONFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Endpoint preparation is pre-spawn: no diagnostics may be present yet.
	if b.Profile != "" || b.ConfigRevision != "" || b.StartedAt != "" {
		t.Fatalf("endpoint prep must not stamp diagnostics (they belong at spawn): %+v", b)
	}
}

func TestStampBridgeDiagnosticsUsesBindingKeyName(t *testing.T) {
	dir := t.TempDir()
	port, token, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	started := "2026-07-22T10:00:00Z"
	if err := StampBridgeDiagnostics("g", dir, port, token, BridgeDiagnostics{
		Profile: "combat", ConfigRevision: "rev-1", StartedAt: started,
	}); err != nil {
		t.Fatalf("stamp: %v", err)
	}

	cp, _ := NewConfigPaths(dir)
	b, err := readBridgeJSONFile(cp.GetBridgeConfigPath("g"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if b.Profile != "combat" || b.ConfigRevision != "rev-1" || b.StartedAt != started {
		t.Fatalf("stamp must record the diagnostics: %+v", b)
	}
	// The endpoint survives the stamp.
	if b.Port == 0 || b.Token == "" {
		t.Fatalf("stamp must preserve the endpoint: %+v", b)
	}
	// The value must parse as RFC3339, and the obsolete key spelling must not
	// appear in the raw JSON.
	if _, perr := time.Parse(time.RFC3339, b.StartedAt); perr != nil {
		t.Fatalf("startedAt must be RFC3339: %v", perr)
	}
	raw, err := readRawBridgeJSON(cp.GetBridgeConfigPath("g"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["startedAt"]; !ok {
		t.Fatalf("the binding key must be 'startedAt': %v", raw)
	}
	if _, ok := raw["startTime"]; ok {
		t.Fatalf("the obsolete 'startTime' key must not be written: %v", raw)
	}
}

func TestReuseClearsStaleDiagnosticsUntilRestamped(t *testing.T) {
	dir := t.TempDir()
	// First launch: prepare + stamp.
	p1, tok1, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if err := StampBridgeDiagnostics("g", dir, p1, tok1, BridgeDiagnostics{Profile: "vanilla", ConfigRevision: "rev-1", StartedAt: "2026-07-22T10:00:00Z"}); err != nil {
		t.Fatalf("first stamp: %v", err)
	}

	// Second launch reuses the port with a rotated token — and preparation
	// CLEARS the previous launch's diagnostics, so a pre-spawn failure now
	// would not leave stale attribution behind (round 11 P2-8).
	p2, tok2, path, reused, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
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
		t.Fatalf("reuse must rotate the token")
	}
	b, _ := readBridgeJSONFile(path)
	if b.Profile != "" || b.ConfigRevision != "" || b.StartedAt != "" {
		t.Fatalf("reuse preparation must clear stale diagnostics: %+v", b)
	}

	// Restamping the reused endpoint records the CURRENT launch's values.
	if err := StampBridgeDiagnostics("g", dir, p2, tok2, BridgeDiagnostics{Profile: "combat", ConfigRevision: "rev-2", StartedAt: "2026-07-22T11:00:00Z"}); err != nil {
		t.Fatalf("second stamp: %v", err)
	}
	b, _ = readBridgeJSONFile(path)
	if b.Profile != "combat" || b.ConfigRevision != "rev-2" {
		t.Fatalf("restamp must record fresh diagnostics: %+v", b)
	}
}

// TestStampRefusesRotatedEndpoint is the F10 generation fence: launch A spawns
// and prepares an endpoint; launch B reuses it with a rotated token; A's late
// stamp (with A's stale token) must be REFUSED, never writing A's profile onto
// B's token.
func TestStampRefusesRotatedEndpoint(t *testing.T) {
	dir := t.TempDir()
	aPort, aToken, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
	if err != nil {
		t.Fatalf("A prepare: %v", err)
	}
	_, bToken, _, reused, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
	if err != nil || !reused {
		t.Fatalf("B prepare (reuse): reused=%v err=%v", reused, err)
	}
	if aToken == bToken {
		t.Fatal("the per-launch token must rotate on reuse")
	}
	err = StampBridgeDiagnostics("g", dir, aPort, aToken, BridgeDiagnostics{Profile: "stale-A", ConfigRevision: "revA", StartedAt: "2026-07-22T10:00:00Z"})
	if !errors.Is(err, ErrBridgeEndpointRotated) {
		t.Fatalf("A's stamp on B's rotated endpoint must be refused, got %v", err)
	}
	cp, _ := NewConfigPaths(dir)
	b, _ := readBridgeJSONFile(cp.GetBridgeConfigPath("g"))
	if b.Profile == "stale-A" || b.Token != bToken {
		t.Fatalf("a superseded launch must never stamp the successor's token: %+v", b)
	}
}

// TestStampCannotRestoreRotatedTokenUnderRace is the F1 in-process concurrency
// invariant: a stale launch's stamp racing a successor's endpoint rotation can
// NEVER restore the stale token or land its diagnostics on the successor's
// token. Under the cross-process bridge lock the read→compare→write is atomic,
// so the final token is always the successor's, whatever the interleaving. The
// cross-PROCESS case is covered by TestStampBlocksRotationAcrossProcesses.
func TestStampCannotRestoreRotatedTokenUnderRace(t *testing.T) {
	for iter := 0; iter < 400; iter++ {
		dir := t.TempDir()
		aPort, aToken, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
		if err != nil {
			t.Fatalf("prepare A: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		var bToken string
		go func() {
			defer wg.Done()
			_ = StampBridgeDiagnostics("g", dir, aPort, aToken, BridgeDiagnostics{Profile: "stale-A", StartedAt: "2026-07-22T10:00:00Z"})
		}()
		go func() {
			defer wg.Done()
			_, tok, _, _, _ := PrepareBridgeEndpointForStart("g", dir, nil, false)
			bToken = tok
		}()
		wg.Wait()

		cp, _ := NewConfigPaths(dir)
		b, _ := readBridgeJSONFile(cp.GetBridgeConfigPath("g"))
		if b.Token != bToken {
			t.Fatalf("iter %d: a stale stamp restored the rotated token: final=%q successor=%q", iter, b.Token, bToken)
		}
		if b.Profile == "stale-A" {
			t.Fatalf("iter %d: stale-A diagnostics landed on the successor's endpoint: %+v", iter, b)
		}
	}
}

// TestStampBlocksRotationDeterministically uses the after-read barrier to prove
// the read→compare→write is atomic: while A's stamp holds the lock, B's
// preparation cannot rotate the endpoint until A completes (round 14 F1).
func TestStampBlocksRotationDeterministically(t *testing.T) {
	dir := t.TempDir()
	aPort, aToken, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	rotated := make(chan struct{})
	entered := make(chan struct{})
	bridgeStampAfterReadHook = func() {
		close(entered)
		// Kick off B's rotation; it must BLOCK on the write lock A holds.
		go func() {
			_, _, _, _, _ = PrepareBridgeEndpointForStart("g", dir, nil, false)
			close(rotated)
		}()
		// Give B a chance to run; it must not have rotated yet.
		time.Sleep(30 * time.Millisecond)
		select {
		case <-rotated:
			t.Error("B rotated the endpoint while A held the stamp lock — not atomic")
		default:
		}
	}
	defer func() { bridgeStampAfterReadHook = nil }()

	if err := StampBridgeDiagnostics("g", dir, aPort, aToken, BridgeDiagnostics{Profile: "A"}); err != nil {
		t.Fatalf("A's stamp must succeed (it ran before any rotation): %v", err)
	}
	<-entered
	<-rotated // B unblocks once A released the lock
}

// Diagnostics must never enter the endpoint-reuse decision.
func TestBridgeDiagnosticsDoNotAffectEndpointReuse(t *testing.T) {
	dir := t.TempDir()
	rport, rtoken, _, _, err := PrepareBridgeEndpointForStart("rich", dir, nil, false)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := StampBridgeDiagnostics("rich", dir, rport, rtoken, BridgeDiagnostics{Profile: "p", ConfigRevision: "r", StartedAt: "2026-07-22T10:00:00Z"}); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if _, _, _, reused, err := PrepareBridgeEndpointForStart("rich", dir, nil, false); err != nil || !reused {
		t.Fatalf("a bridge with diagnostics must still be reusable: reused=%v err=%v", reused, err)
	}
}
