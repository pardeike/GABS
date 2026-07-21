package process

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func m2Spec(gameID string) LaunchSpec {
	return LaunchSpec{
		GameId:         gameID,
		Mode:           "DirectPath",
		PathOrId:       "/opt/game",
		Profile:        "combat",
		AppliedInputs:  []string{"quickStart"},
		ConfigRevision: "sha256:abc123def456",
		Env:            map[string]string{},
	}
}

func TestClaimStampsSchemaAndIdentity(t *testing.T) {
	dir := t.TempDir()
	state := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting)
	if state.SchemaVersion != RuntimeSchemaVersion {
		t.Fatalf("claims must stamp the schema marker, got %d", state.SchemaVersion)
	}
	if len(state.LaunchID) < 16 {
		t.Fatalf("launch ID must be minted at creation, got %q", state.LaunchID)
	}
	if state.Generation != 1 || state.Phase != PhaseStarting || state.SpawnState != SpawnStatePreflight {
		t.Fatalf("initial claim state wrong: %+v", state)
	}
	if state.Source != SourceGABS || state.PIDRole != PIDRoleWorkload {
		t.Fatalf("source/pidRole wrong: %+v", state)
	}
	if state.Profile != "combat" || state.ConfigRevision != "sha256:abc123def456" {
		t.Fatalf("resolved context not stamped: %+v", state)
	}

	// URL modes pin the helper PID role
	spec := m2Spec("g2")
	spec.Mode = "SteamAppId"
	spec.Profile = ""
	if s2 := NewRuntimeState(spec, RuntimeStateStatusStarting); s2.PIDRole != PIDRoleHelper {
		t.Fatalf("URL modes must pin pidRole helper, got %q", s2.PIDRole)
	}

	// two claims mint distinct launch IDs
	if s3 := NewRuntimeState(m2Spec("g1"), RuntimeStateStatusStarting); s3.LaunchID == state.LaunchID {
		t.Fatalf("launch IDs must be unique")
	}
	_ = dir
}

func TestClaimPublishesAtomicallyWithMode0600(t *testing.T) {
	dir := t.TempDir()
	state := NewRuntimeState(m2Spec("atomic"), RuntimeStateStatusStarting)
	if err := ClaimRuntimeState("atomic", dir, state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "atomic", "runtime.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("claim must be 0600, got %v", fi.Mode().Perm())
	}
	// no temp files linger
	entries, _ := os.ReadDir(filepath.Join(dir, "atomic"))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file lingers: %s", e.Name())
		}
	}
	// second claim loses
	if err := ClaimRuntimeState("atomic", dir, state); err != ErrRuntimeStateExists {
		t.Fatalf("second claim must lose with ErrRuntimeStateExists, got %v", err)
	}
}

func TestClaimReadHammer(t *testing.T) {
	// A status read racing initial claim publication never observes
	// empty/partial JSON (design/30 T-FENCE atomic publication).
	dir := t.TempDir()
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			state := NewRuntimeState(m2Spec("hammer"), RuntimeStateStatusStarting)
			_ = ClaimRuntimeState("hammer", dir, state)
			_ = RemoveRuntimeState("hammer", dir)
		}
		close(stop)
	}()

	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					state, err := LoadRuntimeState("hammer", dir)
					if err != nil {
						t.Errorf("reader observed partial/corrupt claim: %v", err)
						return
					}
					if state != nil && state.LaunchID == "" {
						t.Errorf("reader observed incomplete claim: %+v", state)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentClaimsExactlyOneWins(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	wins := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			state := NewRuntimeState(m2Spec("race"), RuntimeStateStatusStarting)
			if err := ClaimRuntimeState("race", dir, state); err == nil {
				wins <- state.LaunchID
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	var winners []string
	for w := range wins {
		winners = append(winners, w)
	}
	if len(winners) != 1 {
		t.Fatalf("exactly one claim must win, got %d", len(winners))
	}
	// the persisted claim is the winner's
	state, err := LoadRuntimeState("race", dir)
	if err != nil || state == nil || state.LaunchID != winners[0] {
		t.Fatalf("persisted claim must match the winner: %+v err=%v", state, err)
	}
}

func TestSaveAtomicAnd0600(t *testing.T) {
	dir := t.TempDir()
	state := NewRuntimeState(m2Spec("save"), RuntimeStateStatusStarting)
	if err := ClaimRuntimeState("save", dir, state); err != nil {
		t.Fatal(err)
	}
	state.Phase = PhaseActive
	if err := SaveRuntimeState("save", dir, state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "save", "runtime.json")
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved claim must be 0600, got %v", fi.Mode().Perm())
	}
	loaded, err := LoadRuntimeState("save", dir)
	if err != nil || loaded.Phase != PhaseActive {
		t.Fatalf("save roundtrip failed: %+v err=%v", loaded, err)
	}
}

func TestLegacyClaimTightenedOnLoad(t *testing.T) {
	dir := t.TempDir()
	gameDir := filepath.Join(dir, "legacy")
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]interface{}{"gameId": "legacy", "status": "running", "ownerPid": 12345}
	data, _ := json.Marshal(legacy)
	path := filepath.Join(gameDir, "runtime.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadRuntimeState("legacy", dir)
	if err != nil {
		t.Fatal(err)
	}
	// legacy discriminator: no schema marker
	if state.SchemaVersion != 0 {
		t.Fatalf("legacy claim must have no schema marker, got %d", state.SchemaVersion)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("legacy 0644 claim must be tightened on load, got %v", fi.Mode().Perm())
	}
}

func TestTransitionBumpsGeneration(t *testing.T) {
	dir := t.TempDir()
	state := NewRuntimeState(m2Spec("gen"), RuntimeStateStatusStarting)
	if err := ClaimRuntimeState("gen", dir, state); err != nil {
		t.Fatal(err)
	}

	updated, err := TransitionRuntimeState("gen", dir, time.Second, func(s *RuntimeState) error {
		s.Phase = PhaseActive
		s.SpawnState = SpawnStateSpawned
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation != 2 || updated.Phase != PhaseActive {
		t.Fatalf("transition must bump generation and persist: %+v", updated)
	}
	if updated.LaunchID != state.LaunchID {
		t.Fatalf("launch ID is immutable across transitions")
	}

	// a second transition bumps again
	updated, err = TransitionRuntimeState("gen", dir, time.Second, func(s *RuntimeState) error {
		s.Phase = PhaseStopping
		return nil
	})
	if err != nil || updated.Generation != 3 {
		t.Fatalf("second transition wrong: %+v err=%v", updated, err)
	}

	// transitioning a missing claim errors
	_ = RemoveRuntimeState("gen", dir)
	if _, err := TransitionRuntimeState("gen", dir, time.Second, func(s *RuntimeState) error { return nil }); err == nil {
		t.Fatalf("transition on a missing claim must error")
	}
}

func TestFencingIDs(t *testing.T) {
	a, b := NewFencingID(), NewFencingID()
	if a == b || len(a) != 32 {
		t.Fatalf("fencing IDs must be unique 128-bit hex: %q %q", a, b)
	}
}
