package process

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
)

// errUnsupportedLink stands in for a filesystem that rejects hard links.
var errUnsupportedLink = errors.New("operation not supported")

func runtimeStatePathForTest(t *testing.T, gameID, configDir string) string {
	t.Helper()
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		t.Fatal(err)
	}
	return cp.GetRuntimeStatePath(gameID)
}

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

func TestClaimLifecycleSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	spec := m2Spec("g-lc")
	spec.StopProcessName = "game-bin"
	spec.Lifecycle = &launch.ResolvedLifecycle{
		Status: &launch.ResolvedHook{
			Command: "/opt/tools/status", Args: []string{"--game", "g-lc"},
			Env: map[string]string{"CTX": "combat"}, UnsetEnv: []string{"NOISY"},
			TimeoutSeconds: 7, RunningExitCodes: []int{0, 3}, StoppedExitCodes: []int{9},
		},
		Stop: &launch.ResolvedHook{
			Command: "/opt/tools/stop", TimeoutSeconds: 45, VerifyTimeoutSeconds: 120,
			WorkingDir: "/srv/g",
		},
	}
	state := NewRuntimeState(spec, RuntimeStateStatusStarting)
	if err := ClaimRuntimeState("g-lc", dir, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadRuntimeState("g-lc", dir)
	if err != nil || loaded == nil {
		t.Fatalf("load: %v %v", loaded, err)
	}
	// The pinned snapshot must survive verbatim: a custom stopped code must
	// not degrade to unknown after a restart or profile edit (design/07),
	// and the built-in fallback stays pinned alongside the hooks.
	lc := loaded.Lifecycle
	if lc == nil || lc.Status == nil || lc.Stop == nil {
		t.Fatalf("lifecycle snapshot missing: %+v", loaded)
	}
	if got := lc.Status.StoppedExitCodes; len(got) != 1 || got[0] != 9 {
		t.Fatalf("custom stopped codes lost: %v", got)
	}
	if got := lc.Status.RunningExitCodes; len(got) != 2 || got[0] != 0 || got[1] != 3 {
		t.Fatalf("running codes lost: %v", got)
	}
	if lc.Status.TimeoutSeconds != 7 || lc.Status.Env["CTX"] != "combat" || len(lc.Status.UnsetEnv) != 1 {
		t.Fatalf("status hook fields lost: %+v", lc.Status)
	}
	if lc.Stop.VerifyTimeoutSeconds != 120 || lc.Stop.WorkingDir != "/srv/g" {
		t.Fatalf("stop hook fields lost: %+v", lc.Stop)
	}
	if loaded.StopProcessName != "game-bin" {
		t.Fatalf("built-in fallback not pinned: %+v", loaded)
	}
}

func TestClaimFallbackWithoutHardlinks(t *testing.T) {
	dir := t.TempDir()
	prevLink := linkRuntimeState
	linkRuntimeState = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errUnsupportedLink}
	}
	t.Cleanup(func() { linkRuntimeState = prevLink })

	state := NewRuntimeState(m2Spec("g-fb"), RuntimeStateStatusStarting)
	if err := ClaimRuntimeState("g-fb", dir, state); err != nil {
		t.Fatalf("fallback claim failed: %v", err)
	}
	loaded, err := LoadRuntimeState("g-fb", dir)
	if err != nil || loaded == nil || loaded.LaunchID != state.LaunchID {
		t.Fatalf("fallback must publish the complete claim: %+v %v", loaded, err)
	}

	// exclusivity is preserved on the fallback path
	if err := ClaimRuntimeState("g-fb", dir, NewRuntimeState(m2Spec("g-fb"), RuntimeStateStatusStarting)); err != ErrRuntimeStateExists {
		t.Fatalf("second fallback claim must fail with ErrRuntimeStateExists, got %v", err)
	}
}

func TestClaimFallbackFailureLeavesNoPartialClaim(t *testing.T) {
	dir := t.TempDir()
	prevLink, prevRename := linkRuntimeState, renameRuntimeState
	linkRuntimeState = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errUnsupportedLink}
	}
	renameRuntimeState = func(string, string) error { return errUnsupportedLink }
	t.Cleanup(func() { linkRuntimeState, renameRuntimeState = prevLink, prevRename })

	if err := ClaimRuntimeState("g-fbf", dir, NewRuntimeState(m2Spec("g-fbf"), RuntimeStateStatusStarting)); err == nil {
		t.Fatalf("claim must fail when publication fails")
	}
	// The empty O_EXCL placeholder must not survive as a blocking claim.
	loaded, err := LoadRuntimeState("g-fbf", dir)
	if err != nil || loaded != nil {
		t.Fatalf("failed fallback must leave nothing behind: %+v %v", loaded, err)
	}
}

func TestLoadTornClaimWaitsForFallbackPublication(t *testing.T) {
	dir := t.TempDir()
	state := NewRuntimeState(m2Spec("g-torn"), RuntimeStateStatusStarting)
	data, err := marshalRuntimeState(state)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the fallback's placeholder window: empty claim file while the
	// transition lock is held; publication completes before release.
	lock, err := AcquireTransitionLock("g-torn", dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cpDir := filepath.Dir(runtimeStatePathForTest(t, "g-torn", dir))
	path := runtimeStatePathForTest(t, "g-torn", dir)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		tmp := filepath.Join(cpDir, ".pub.tmp")
		if err := os.WriteFile(tmp, data, 0o600); err == nil {
			_ = os.Rename(tmp, path)
		}
		lock.Release()
	}()

	loaded, err := LoadRuntimeState("g-torn", dir)
	<-done
	if err != nil || loaded == nil || loaded.LaunchID != state.LaunchID {
		t.Fatalf("reader must wait out the placeholder window via the lock: %+v %v", loaded, err)
	}

	// A permanently torn claim (no lock holder) is an error, never a nil
	// "no claim" that would authorize a start.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeState("g-torn", dir); err == nil {
		t.Fatalf("permanently torn claim must be an error")
	}
}

func TestGameDirIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := ClaimRuntimeState("g-perm", dir, NewRuntimeState(m2Spec("g-perm"), RuntimeStateStatusStarting)); err != nil {
		t.Fatal(err)
	}
	gameDir := filepath.Dir(runtimeStatePathForTest(t, "g-perm", dir))
	fi, err := os.Stat(gameDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("game dir must be 0700, got %v", fi.Mode().Perm())
	}

	// pre-existing loose dirs are tightened
	loose := filepath.Dir(runtimeStatePathForTest(t, "g-loose", dir))
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ClaimRuntimeState("g-loose", dir, NewRuntimeState(m2Spec("g-loose"), RuntimeStateStatusStarting)); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(loose)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("loose game dir must be tightened to 0700, got %v", fi.Mode().Perm())
	}
}
