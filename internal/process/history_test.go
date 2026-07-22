package process

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
)

// bc builds an input-free BaseContext directly (no resolver) for hashing
// tests — the resolver-based extraction is covered at the launch/mcp layer.
func bc(target, mode string, args []string, env map[string]string, absent []string, wd string, lc *launch.ResolvedLifecycle) *launch.BaseContext {
	return &launch.BaseContext{Target: target, Mode: mode, Args: args, ConfigEnv: env, AbsentEnvNames: absent, WorkingDir: wd, Lifecycle: lc}
}

func combatBC(lc *launch.ResolvedLifecycle) *launch.BaseContext {
	return bc("/opt/game", "DirectPath", []string{"-base", "-combat"}, map[string]string{"CONTENT_SET": "combat", "MODE": "arena"}, nil, "", lc)
}

func vanillaBC() *launch.BaseContext {
	return bc("/opt/game", "DirectPath", []string{"-base", "-vanilla"}, map[string]string{"CONTENT_SET": "combat"}, nil, "", nil)
}

func histGame() config.GameConfig {
	return config.GameConfig{
		ID:         "adv",
		LaunchMode: "DirectPath",
		Target:     "/opt/game",
		Args:       []string{"-base"},
		Env:        map[string]string{"CONTENT_SET": "combat"},
		Profiles: map[string]config.ProfileConfig{
			"combat":  {Args: []string{"-combat"}, Env: map[string]string{"MODE": "arena"}},
			"vanilla": {Args: []string{"-vanilla"}},
		},
		LaunchInputs: map[string]config.LaunchInputConfig{
			"scenario": {Type: "string", Args: []string{"-scenario=${value}"}},
		},
	}
}

func TestContextHashIsInputFree(t *testing.T) {
	// The base context is input-free by construction: two launches of the
	// same profile with different supplied inputs produce the same
	// BaseContext and thus the same hash (design/08).
	h1 := ContextHash(combatBC(nil))
	h2 := ContextHash(combatBC(nil))
	if h1 == "" || h1 != h2 {
		t.Fatalf("same context must hash identically: %q %q", h1, h2)
	}
	if ContextHash(vanillaBC()) == h1 {
		t.Fatal("distinct profiles must hash differently")
	}
}

func TestContextHashProfileGranularity(t *testing.T) {
	hCombat := ContextHash(combatBC(nil))
	hVanilla := ContextHash(vanillaBC())

	// Editing profile vanilla's args changes only its hash.
	if ContextHash(bc("/opt/game", "DirectPath", []string{"-base", "-vanilla", "-extra"}, map[string]string{"CONTENT_SET": "combat"}, nil, "", nil)) == hVanilla {
		t.Fatal("editing profile vanilla must change its own hash")
	}
	if ContextHash(combatBC(nil)) != hCombat {
		t.Fatal("combat's hash is independent of vanilla edits")
	}

	// Editing a shared game-level arg changes every profile's effective argv
	// and therefore every hash.
	if ContextHash(bc("/opt/game", "DirectPath", []string{"-base", "-shared-new", "-combat"}, map[string]string{"CONTENT_SET": "combat", "MODE": "arena"}, nil, "", nil)) == hCombat {
		t.Fatal("editing a shared game-level arg must change the hash")
	}
}

func TestContextHashIncludesLifecycle(t *testing.T) {
	base := ContextHash(combatBC(nil))
	lc := &launch.ResolvedLifecycle{Status: &launch.ResolvedHook{Command: "/hooks/status", TimeoutSeconds: 5}}
	withHook := ContextHash(combatBC(lc))
	if withHook == base {
		t.Fatal("the resolved lifecycle is part of the context and must affect the hash")
	}
	if ContextHash(combatBC(lc)) != withHook {
		t.Fatal("an identical resolved lifecycle must hash identically")
	}
}

func TestHistoryCountersAndResetOnSuccess(t *testing.T) {
	dir := t.TempDir()
	lid := seedHistoryClaim(t, "adv", dir)
	hash := ContextHash(combatBC(nil))
	now := time.Now().UTC()

	// A terminal failure with a resolved context records lastFailure and
	// advances consecutiveFailures.
	if err := RecordFailure("adv", dir, lid, "combat", hash, "exited_during_start", CauseGame, []string{"scenario"}, now); err != nil {
		t.Fatal(err)
	}
	if err := RecordFailure("adv", dir, lid, "combat", hash, "exited_during_start", CauseGame, nil, now); err != nil {
		t.Fatal(err)
	}
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if e.ConsecutiveFailures != 2 {
		t.Fatalf("two failures must advance the counter: %+v", e)
	}
	if e.LastFailure == nil || e.LastFailure.Outcome != "exited_during_start" || e.LastFailure.Class != CauseGame {
		t.Fatalf("last failure must persist: %+v", e.LastFailure)
	}

	// A verified workload start increments workloadStarts, resets
	// consecutiveFailures, and refreshes lastGood.
	snap := ContextSnapshot{Target: "/opt/game", Mode: "DirectPath"}
	if err := RecordWorkloadStart("adv", dir, lid, "combat", hash, snap, simpleBucket([]string{"scenario"}, "decl-hash", "value-digest"), now); err != nil {
		t.Fatal(err)
	}
	h, _ = LoadHistory("adv", dir)
	e = h.Profiles["combat"]
	if e.WorkloadStarts != 1 || e.ConsecutiveFailures != 0 {
		t.Fatalf("a success resets consecutiveFailures and counts the start: %+v", e)
	}
	if e.LastSuccessAt.IsZero() {
		t.Fatal("lastSuccessAt must be set")
	}
	lg := h.LastGood["combat"]
	if lg == nil || lg.ContextHash != hash {
		t.Fatalf("lastGood must be refreshed on a verified start: %+v", lg)
	}
}

func TestHistorySplitCounters(t *testing.T) {
	dir := t.TempDir()
	lid := seedHistoryClaim(t, "adv", dir)
	hash := ContextHash(combatBC(nil))
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: "/opt/game", Mode: "DirectPath"}

	for i := 0; i < 14; i++ {
		if err := RecordWorkloadStart("adv", dir, lid, "combat", hash, snap, simpleBucket(nil, "d", "v"), now); err != nil {
			t.Fatal(err)
		}
	}
	// The bridge never connected: the split points game-side, not at config.
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if e.WorkloadStarts != 14 || e.BridgeConnects != 0 {
		t.Fatalf("workload proven but bridge never connected: %+v", e)
	}

	_ = RecordBridgeConnect("adv", dir, lid, "combat", hash, now)
	_ = RecordDeliveryVerified("adv", dir, lid, "combat", hash, now)
	applyCleanStopUnderLock(t, "adv", dir, "combat", hash, now)
	h, _ = LoadHistory("adv", dir)
	e = h.Profiles["combat"]
	if e.BridgeConnects != 1 || e.DeliveriesVerified != 1 || e.CleanStops != 1 {
		t.Fatalf("split counters must each advance at their own point: %+v", e)
	}
}

func TestHistoryInputBucketsDistinctAndCapped(t *testing.T) {
	dir := t.TempDir()
	lid := seedHistoryClaim(t, "adv", dir)
	hash := ContextHash(combatBC(nil))
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: "/opt/game", Mode: "DirectPath"}

	// scenario=arena proven does not mark scenario=tutorial proven.
	_ = RecordWorkloadStart("adv", dir, lid, "combat", hash, snap, simpleBucket([]string{"scenario"}, "scenario-decl", "digest-arena"), now)
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if !e.hasBucket("scenario-decl", "digest-arena") {
		t.Fatal("the proven arena bucket must exist")
	}
	if e.hasBucket("scenario-decl", "digest-tutorial") {
		t.Fatal("a distinct value combination must be its own unproven bucket")
	}

	// The bare set (no inputs) is a separate bucket from any input combination.
	_ = RecordWorkloadStart("adv", dir, lid, "combat", hash, snap, simpleBucket(nil, "", ""), now)
	h, _ = LoadHistory("adv", dir)
	if !h.Profiles["combat"].hasBucket("", "") {
		t.Fatal("proven-bare must be its own bucket")
	}

	// Value variants per input-name set are capped (LRU eviction).
	for i := 0; i < bucketValueCap+5; i++ {
		_ = RecordWorkloadStart("adv", dir, lid, "combat", hash, snap, simpleBucket([]string{"scenario"}, "scenario-decl", "digest-"+string(rune('a'+i))), now.Add(time.Duration(i)*time.Second))
	}
	h, _ = LoadHistory("adv", dir)
	e = h.Profiles["combat"]
	if got := e.bucketNameSetCount([]string{"scenario"}); got > bucketValueCap {
		t.Fatalf("value buckets must be capped at %d, got %d", bucketValueCap, got)
	}
}

func TestHistoryEditResetsOnlyChangedBucketDeclaration(t *testing.T) {
	dir := t.TempDir()
	lid := seedHistoryClaim(t, "adv", dir)
	hash := ContextHash(combatBC(nil))
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: "/opt/game", Mode: "DirectPath"}

	// Prove the bare set and one scenario combination under declaration D1.
	_ = RecordWorkloadStart("adv", dir, lid, "combat", hash, snap, simpleBucket(nil, "", ""), now)
	_ = RecordWorkloadStart("adv", dir, lid, "combat", hash,
		snap, SuccessBucket{InputNames: []string{"scenario"}, PerInputDecl: map[string]string{"scenario": "decl-v1"}, DeclHash: "combo-v1", ValueDigest: "digest-arena"}, now)

	// Editing scenario's DECLARATION (decl-v1 -> decl-v2) drops only the
	// buckets involving scenario; the bare-set proof and base counters live.
	if err := InvalidateChangedInputDeclarations("adv", dir, "combat", map[string]string{"scenario": "decl-v2"}); err != nil {
		t.Fatal(err)
	}
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if e.hasBucket("combo-v1", "digest-arena") {
		t.Fatal("the edited declaration's buckets must be dropped")
	}
	if !e.hasBucket("", "") {
		t.Fatal("the bare-set proof must survive an input-declaration edit")
	}
	if e.WorkloadStarts != 2 {
		t.Fatalf("base counters must survive an input-declaration edit: %+v", e)
	}
}

func TestHistorySurvivesAndDegrades(t *testing.T) {
	dir := t.TempDir()
	// A missing history file degrades to an empty record, never an error.
	h, err := LoadHistory("adv", dir)
	if err != nil || h == nil {
		t.Fatalf("a missing history must be empty, not an error: %v", err)
	}
	if len(h.Profiles) != 0 {
		t.Fatal("empty history has no profiles")
	}

	// Corrupt history degrades to "no track record" without error.
	cp, _ := config.NewConfigPaths(dir)
	_ = cp.EnsureGameDir("adv")
	// LoadHistory tolerates a torn/garbage file.
	if err := writeCorruptHistory(dir, "adv"); err != nil {
		t.Fatal(err)
	}
	h, err = LoadHistory("adv", dir)
	if err != nil || h == nil || len(h.Profiles) != 0 {
		t.Fatalf("corrupt history must degrade to empty: %v %+v", err, h)
	}
}

func TestHistoryDeliveriesVerifiedDefaultsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	lid := seedHistoryClaim(t, "adv", dir)
	hash := ContextHash(combatBC(nil))
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: "/opt/game", Mode: "DirectPath"}
	// An older file without deliveriesVerified defaults it to 0.
	_ = RecordWorkloadStart("adv", dir, lid, "combat", hash, snap, simpleBucket(nil, "", ""), now)
	h, _ := LoadHistory("adv", dir)
	if h.Profiles["combat"].DeliveriesVerified != 0 {
		t.Fatal("deliveriesVerified defaults to 0 until a verified delivery")
	}
}

func TestHistoryConcurrentUpdatesLoseNoIncrements(t *testing.T) {
	dir := t.TempDir()
	lid := seedHistoryClaim(t, "adv", dir)
	hash := ContextHash(combatBC(nil))
	now := time.Now().UTC()

	// A server delivery callback and a bridge-connect interleaved must not
	// overwrite each other's counters (design/20: RMW under the lock).
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = RecordBridgeConnect("adv", dir, lid, "combat", hash, now)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = RecordDeliveryVerified("adv", dir, lid, "combat", hash, now)
		}()
	}
	wg.Wait()
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if e.BridgeConnects != n || e.DeliveriesVerified != n {
		t.Fatalf("no increments may be lost under contention: connects=%d deliveries=%d", e.BridgeConnects, e.DeliveriesVerified)
	}
}

// seedHistoryClaim publishes a minimal claim so launch-fenced history
// recorders see a matching launch. Returns the launch ID.
func seedHistoryClaim(t *testing.T, gameID, dir string) string {
	t.Helper()
	st := NewRuntimeState(m2Spec(gameID), RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	st.SpawnState = SpawnStateSpawned
	if err := ClaimRuntimeState(gameID, dir, st); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	return st.LaunchID
}

func simpleBucket(names []string, declHash, valueDigest string) SuccessBucket {
	return SuccessBucket{InputNames: names, DeclHash: declHash, ValueDigest: valueDigest}
}

func writeCorruptHistory(dir, gameID string) error {
	cp, err := config.NewConfigPaths(dir)
	if err != nil {
		return err
	}
	if err := cp.EnsureGameDir(gameID); err != nil {
		return err
	}
	return os.WriteFile(cp.GetHistoryPath(gameID), []byte("{ this is not valid json"), 0o600)
}

// applyCleanStopUnderLock exercises the lock-free clean-stop path the stop
// pipeline uses (the caller holds the transition lock).
func applyCleanStopUnderLock(t *testing.T, gameID, dir, profile, hash string, at time.Time) {
	t.Helper()
	lock, err := AcquireTransitionLock(gameID, dir, transitionLockGateTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	applyCleanStop(gameID, dir, profile, hash, at)
}
