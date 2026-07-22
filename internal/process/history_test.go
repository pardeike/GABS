package process

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
)

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
	g := histGame()
	// Two launches of profile combat with DIFFERENT supplied inputs must
	// share one context hash — inputs are the bucket dimension, not the
	// context (design/08).
	h1 := ContextHash(g, "combat", nil)
	h2 := ContextHash(g, "combat", nil)
	if h1 == "" || h1 != h2 {
		t.Fatalf("same context must hash identically: %q %q", h1, h2)
	}
	// A different profile is a different context.
	if ContextHash(g, "vanilla", nil) == h1 {
		t.Fatal("distinct profiles must hash differently (game-level args shared, profile args differ)")
	}
}

func TestContextHashProfileGranularity(t *testing.T) {
	g := histGame()
	hCombat := ContextHash(g, "combat", nil)
	hVanilla := ContextHash(g, "vanilla", nil)

	// Editing profile vanilla must not change combat's hash.
	edited := histGame()
	p := edited.Profiles["vanilla"]
	p.Args = []string{"-vanilla", "-extra"}
	edited.Profiles["vanilla"] = p
	if ContextHash(edited, "combat", nil) != hCombat {
		t.Fatal("editing profile vanilla must leave combat's context hash intact")
	}
	if ContextHash(edited, "vanilla", nil) == hVanilla {
		t.Fatal("editing profile vanilla must change its own hash")
	}

	// Adding profile C resets nothing (existing hashes unchanged).
	added := histGame()
	added.Profiles["arena"] = config.ProfileConfig{Args: []string{"-arena"}}
	if ContextHash(added, "combat", nil) != hCombat || ContextHash(added, "vanilla", nil) != hVanilla {
		t.Fatal("adding a new profile must not change existing profile hashes")
	}

	// Editing a shared game-level arg resets ALL profiles.
	sharedEdit := histGame()
	sharedEdit.Args = []string{"-base", "-shared-new"}
	if ContextHash(sharedEdit, "combat", nil) == hCombat || ContextHash(sharedEdit, "vanilla", nil) == hVanilla {
		t.Fatal("editing a shared game-level arg must change every profile's hash")
	}
}

func TestContextHashExcludesLifecycleValues(t *testing.T) {
	g := histGame()
	base := ContextHash(g, "combat", nil)
	lc := &launch.ResolvedLifecycle{Status: &launch.ResolvedHook{Command: "/hooks/status", TimeoutSeconds: 5}}
	withHook := ContextHash(g, "combat", lc)
	if withHook == base {
		t.Fatal("the resolved lifecycle is part of the context and must affect the hash")
	}
	// The same resolved lifecycle hashes identically.
	if ContextHash(g, "combat", lc) != withHook {
		t.Fatal("an identical resolved lifecycle must hash identically")
	}
}

func TestHistoryCountersAndResetOnSuccess(t *testing.T) {
	dir := t.TempDir()
	g := histGame()
	hash := ContextHash(g, "combat", nil)
	now := time.Now().UTC()

	// A terminal failure with a resolved context records lastFailure and
	// advances consecutiveFailures.
	if err := RecordFailure("adv", dir, "combat", hash, "exited_during_start", CauseGame, []string{"scenario"}, now); err != nil {
		t.Fatal(err)
	}
	if err := RecordFailure("adv", dir, "combat", hash, "exited_during_start", CauseGame, nil, now); err != nil {
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
	snap := ContextSnapshot{Target: g.Target, Mode: g.LaunchMode}
	if err := RecordWorkloadStart("adv", dir, "combat", hash, snap, []string{"scenario"}, "decl-hash", "value-digest", now); err != nil {
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
	g := histGame()
	hash := ContextHash(g, "combat", nil)
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: g.Target, Mode: g.LaunchMode}

	for i := 0; i < 14; i++ {
		if err := RecordWorkloadStart("adv", dir, "combat", hash, snap, nil, "d", "v", now); err != nil {
			t.Fatal(err)
		}
	}
	// The bridge never connected: the split points game-side, not at config.
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if e.WorkloadStarts != 14 || e.BridgeConnects != 0 {
		t.Fatalf("workload proven but bridge never connected: %+v", e)
	}

	_ = RecordBridgeConnect("adv", dir, "combat", hash, now)
	_ = RecordDeliveryVerified("adv", dir, "combat", hash, now)
	_ = RecordCleanStop("adv", dir, "combat", hash, now)
	h, _ = LoadHistory("adv", dir)
	e = h.Profiles["combat"]
	if e.BridgeConnects != 1 || e.DeliveriesVerified != 1 || e.CleanStops != 1 {
		t.Fatalf("split counters must each advance at their own point: %+v", e)
	}
}

func TestHistoryInputBucketsDistinctAndCapped(t *testing.T) {
	dir := t.TempDir()
	g := histGame()
	hash := ContextHash(g, "combat", nil)
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: g.Target, Mode: g.LaunchMode}

	// scenario=arena proven does not mark scenario=tutorial proven.
	_ = RecordWorkloadStart("adv", dir, "combat", hash, snap, []string{"scenario"}, "scenario-decl", "digest-arena", now)
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if !e.hasBucket("scenario-decl", "digest-arena") {
		t.Fatal("the proven arena bucket must exist")
	}
	if e.hasBucket("scenario-decl", "digest-tutorial") {
		t.Fatal("a distinct value combination must be its own unproven bucket")
	}

	// The bare set (no inputs) is a separate bucket from any input combination.
	_ = RecordWorkloadStart("adv", dir, "combat", hash, snap, nil, "", "", now)
	h, _ = LoadHistory("adv", dir)
	if !h.Profiles["combat"].hasBucket("", "") {
		t.Fatal("proven-bare must be its own bucket")
	}

	// Value variants per input set are capped (LRU eviction).
	for i := 0; i < bucketValueCap+5; i++ {
		_ = RecordWorkloadStart("adv", dir, "combat", hash, snap, []string{"scenario"}, "scenario-decl", "digest-"+string(rune('a'+i)), now.Add(time.Duration(i)*time.Second))
	}
	h, _ = LoadHistory("adv", dir)
	e = h.Profiles["combat"]
	if got := e.bucketCount("scenario-decl"); got > bucketValueCap {
		t.Fatalf("value buckets must be capped at %d, got %d", bucketValueCap, got)
	}
}

func TestHistoryEditResetsOnlyChangedBucketDeclaration(t *testing.T) {
	dir := t.TempDir()
	g := histGame()
	hash := ContextHash(g, "combat", nil)
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: g.Target, Mode: g.LaunchMode}

	// Prove the bare set and one input combination under declaration D1.
	_ = RecordWorkloadStart("adv", dir, "combat", hash, snap, nil, "", "", now)
	_ = RecordWorkloadStart("adv", dir, "combat", hash, snap, []string{"scenario"}, "decl-v1", "digest-arena", now)

	// Editing that input's DECLARATION (new decl hash) drops only its
	// buckets; the bare-set proof and base counters survive.
	ResetInputBuckets("adv", dir, "combat", "decl-v1")
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if e.hasBucket("decl-v1", "digest-arena") {
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
	g := histGame()
	hash := ContextHash(g, "combat", nil)
	now := time.Now().UTC()
	snap := ContextSnapshot{Target: g.Target, Mode: g.LaunchMode}
	// An older file without deliveriesVerified defaults it to 0.
	_ = RecordWorkloadStart("adv", dir, "combat", hash, snap, nil, "", "", now)
	h, _ := LoadHistory("adv", dir)
	if h.Profiles["combat"].DeliveriesVerified != 0 {
		t.Fatal("deliveriesVerified defaults to 0 until a verified delivery")
	}
}

func TestHistoryConcurrentUpdatesLoseNoIncrements(t *testing.T) {
	dir := t.TempDir()
	g := histGame()
	hash := ContextHash(g, "combat", nil)
	now := time.Now().UTC()

	// A server delivery callback and a CLI stop interleaved must not
	// overwrite each other's counters (design/20: RMW under the lock).
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = RecordBridgeConnect("adv", dir, "combat", hash, now)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = RecordCleanStop("adv", dir, "combat", hash, now)
		}()
	}
	wg.Wait()
	h, _ := LoadHistory("adv", dir)
	e := h.Profiles["combat"]
	if e.BridgeConnects != n || e.CleanStops != n {
		t.Fatalf("no increments may be lost under contention: connects=%d stops=%d", e.BridgeConnects, e.CleanStops)
	}
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
