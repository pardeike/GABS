package process

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
)

// bucketValueCap bounds distinct supplied-value combinations per input set
// (design/20): LRU eviction keeps history.json from growing unbounded.
const bucketValueCap = 16

// Cause classes (design/08). causeClass on every failure result; the
// classifier maps outcome + evidence + track record to exactly one.
const (
	CauseCall        = "call"
	CauseConfig      = "config"
	CauseEnvironment = "environment"
	CauseGame        = "game"
	CauseState       = "state"
)

// ContextSnapshot is the COMPLETE last-known-good launch context, captured
// at the successful launch so doctor --show-last-good can compare or
// restore after an edit (design/08). It may hold env VALUES, so it lives
// only in the 0600 history file and never reaches an MCP result.
type ContextSnapshot struct {
	Target         string                    `json:"target,omitempty"`
	Mode           string                    `json:"mode,omitempty"`
	Args           []string                  `json:"args,omitempty"`
	ConfigEnv      map[string]string         `json:"configEnv,omitempty"`
	AbsentEnvNames []string                  `json:"absentEnvNames,omitempty"`
	WorkingDir     string                    `json:"workingDir,omitempty"`
	Lifecycle      *launch.ResolvedLifecycle `json:"lifecycle,omitempty"`
}

// HistoryFailure is the last recorded failure for a context (design/08).
type HistoryFailure struct {
	Outcome    string    `json:"outcome"`
	Class      string    `json:"class"`
	At         time.Time `json:"at"`
	InputNames []string  `json:"inputNames,omitempty"`
}

// HistoryBucket records a proven supplied-input combination. It retains the
// sorted input-name set and each input's declaration hash so a
// declaration-only edit can invalidate exactly the buckets involving the
// changed input while retaining bare proof and unrelated input sets
// (design/08). The combined DeclHash + ValueDigest keys the bucket; the cap
// is per input-NAME set so repeated edits cannot accumulate unbounded
// variants.
type HistoryBucket struct {
	InputNames   []string          `json:"inputNames,omitempty"`
	PerInputDecl map[string]string `json:"perInputDecl,omitempty"`
	DeclHash     string            `json:"declHash"`
	ValueDigest  string            `json:"valueDigest"`
	Count        uint64            `json:"count"`
	LastAt       time.Time         `json:"lastAt"`
}

func nameSetKey(names []string) string {
	s := append([]string(nil), names...)
	sort.Strings(s)
	return strings.Join(s, "\x00")
}

// HistoryEntry is one profile's track record for its CURRENT context hash.
// A context change resets the whole entry; an input-declaration change
// drops only the matching buckets (design/08, two-level reset).
type HistoryEntry struct {
	ContextHash         string          `json:"contextHash"`
	WorkloadStarts      uint64          `json:"workloadStarts"`
	BridgeConnects      uint64          `json:"bridgeConnects"`
	DeliveriesVerified  uint64          `json:"deliveriesVerified"`
	CleanStops          uint64          `json:"cleanStops"`
	LastSuccessAt       time.Time       `json:"lastSuccessAt,omitempty"`
	ConsecutiveFailures uint64          `json:"consecutiveFailures"`
	LastFailure         *HistoryFailure `json:"lastFailure,omitempty"`
	Buckets             []HistoryBucket `json:"buckets,omitempty"`
	// CreditedLaunchIDs are the launches whose Stage-4 verified start already
	// counted. Keyed by launchID, the credit is idempotent across both crash
	// directions: the credit is recorded FIRST under the transition lock,
	// before runtime.json is saved, so a runtime-save failure or a crash
	// between the two files replays without double-counting — a retried
	// promotion finds the launchID already credited. LRU-capped.
	CreditedLaunchIDs []string `json:"creditedLaunchIds,omitempty"`
	// CreditedEvents are the RECORD-FIRST non-start events (bridge connect)
	// already counted, keyed "kind:id". Their replay source is TRANSIENT (a
	// reconnect gets a fresh connectionID), so a small LRU window suffices.
	// Delivery/clean-stop are NOT here — their replay source is a durable
	// pending record, so they use CreditedPendingEvents.
	CreditedEvents []string `json:"creditedEvents,omitempty"`
	// CreditedPendingEvents are the delivery/clean-stop events already counted
	// whose replay source is a DURABLE pending record on the claim. Keyed
	// "delivery:connectionID" / "stop:operationID". This set is NOT
	// LRU-capped: it is lifetime-coupled to the pending records. A marker is
	// dropped (gcPendingCreditMarkersLocked) only AFTER the runtime transition
	// that de-references its record — a prune+save or a claim removal — is
	// durable, and only for the exact records that transition drained. So a
	// marker can never be evicted while the record it guards can still replay,
	// and an unrelated reconcile can never drop a marker whose runtime transition
	// has not yet committed. A stale marker left by a GC-write
	// failure is harmless: every id is a fresh 128-bit random (NewFencingID) that
	// a future event can never collide with. That is the per-pending-record
	// "credited" bit, stored history-side for atomicity with the counter.
	CreditedPendingEvents []string `json:"creditedPendingEvents,omitempty"`
}

// creditEventOnce reports whether key is a NEW record-first event for this entry
// (bridge connect), recording it LRU-capped so a recent retry no-ops. Returns
// false when already credited.
func (e *HistoryEntry) creditEventOnce(key string) bool {
	if key == "" {
		return true // no identity to dedup by; credit unconditionally
	}
	for _, k := range e.CreditedEvents {
		if k == key {
			return false
		}
	}
	e.CreditedEvents = append(e.CreditedEvents, key)
	if len(e.CreditedEvents) > creditedEventCap {
		e.CreditedEvents = e.CreditedEvents[len(e.CreditedEvents)-creditedEventCap:]
	}
	return true
}

// markPendingCreditOnce reports whether key is a NEW pending-event credit for
// this entry, recording it WITHOUT an LRU cap: the marker is
// GC'd by dropPendingCreditMarkers only after its pending record's runtime
// transition is durable, so it must never be dropped by recency while the record
// can still replay.
func (e *HistoryEntry) markPendingCreditOnce(key string) bool {
	if key == "" {
		return true
	}
	for _, k := range e.CreditedPendingEvents {
		if k == key {
			return false
		}
	}
	e.CreditedPendingEvents = append(e.CreditedPendingEvents, key)
	return true
}

// dropPendingCreditMarkers removes exactly the markers named in drained — the
// pending records a just-committed prune/removal made durably unreferenced.
// It is SCOPED, never a retain-live sweep: a marker outside drained belongs to
// an event on another path that may be durable in history but not yet pruned
// from its own claim, and dropping it early would let that event replay and
// double-count.
func (e *HistoryEntry) dropPendingCreditMarkers(drained map[string]bool) {
	if len(e.CreditedPendingEvents) == 0 {
		return
	}
	kept := e.CreditedPendingEvents[:0]
	for _, k := range e.CreditedPendingEvents {
		if !drained[k] {
			kept = append(kept, k)
		}
	}
	if len(kept) == 0 {
		e.CreditedPendingEvents = nil
	} else {
		e.CreditedPendingEvents = kept
	}
}

// creditedEventCap bounds the per-entry LRU dedup ring for record-first events.
const creditedEventCap = 32

func (e *HistoryEntry) hasBucket(declHash, valueDigest string) bool {
	for i := range e.Buckets {
		if e.Buckets[i].DeclHash == declHash && e.Buckets[i].ValueDigest == valueDigest {
			return true
		}
	}
	return false
}

// HasBucket reports whether a proven supplied-input combination exists.
func (e *HistoryEntry) HasBucket(declHash, valueDigest string) bool {
	return e.hasBucket(declHash, valueDigest)
}

func (e *HistoryEntry) bucketCount(declHash string) int {
	n := 0
	for i := range e.Buckets {
		if e.Buckets[i].DeclHash == declHash {
			n++
		}
	}
	return n
}

// recordBucket adds or refreshes a value bucket, capped LRU per input-NAME
// set (so repeated declaration edits cannot accumulate unbounded variants).
func (e *HistoryEntry) recordBucket(b HistoryBucket) {
	for i := range e.Buckets {
		if e.Buckets[i].DeclHash == b.DeclHash && e.Buckets[i].ValueDigest == b.ValueDigest {
			e.Buckets[i].Count++
			e.Buckets[i].LastAt = b.LastAt
			return
		}
	}
	e.Buckets = append(e.Buckets, b)
	// Evict LRU within the same input-name set if over the cap.
	key := nameSetKey(b.InputNames)
	var idxs []int
	for i := range e.Buckets {
		if nameSetKey(e.Buckets[i].InputNames) == key {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) <= bucketValueCap {
		return
	}
	sort.Slice(idxs, func(a, c int) bool {
		return e.Buckets[idxs[a]].LastAt.Before(e.Buckets[idxs[c]].LastAt)
	})
	evict := idxs[0]
	e.Buckets = append(e.Buckets[:evict], e.Buckets[evict+1:]...)
}

// bucketNameSetCount counts buckets in one input-name set (for the cap test).
func (e *HistoryEntry) bucketNameSetCount(names []string) int {
	key := nameSetKey(names)
	n := 0
	for i := range e.Buckets {
		if nameSetKey(e.Buckets[i].InputNames) == key {
			n++
		}
	}
	return n
}

// HistoryLastGood is a refreshable last-known-good pointer per profile.
type HistoryLastGood struct {
	ContextHash   string          `json:"contextHash"`
	EntrySnapshot ContextSnapshot `json:"entrySnapshot"`
	At            time.Time       `json:"at"`
}

// PendingEditNotice preserves the old proven context's facts across the
// history reset, so the once-per-edit notice can still fire on the next
// eligible result even though the entry was already replaced.
type PendingEditNotice struct {
	NewHash   string `json:"newHash"`
	OldStarts uint64 `json:"oldStarts"`
	OldClass  string `json:"oldClass"`
}

// GameHistory is the per-game track record (design/08). BucketKey is a
// per-game random key so value digests cannot be correlated across games.
type GameHistory struct {
	Version   int                         `json:"version"`
	BucketKey string                      `json:"bucketKey"`
	Profiles  map[string]*HistoryEntry    `json:"profiles"`
	LastGood  map[string]*HistoryLastGood `json:"lastGood,omitempty"`
	// NoticeShownForHash records, per profile, the context hash whose
	// edit-visibility notice has already been shown — so it fires once per
	// edit, not on every call (design/08).
	NoticeShownForHash map[string]string `json:"noticeShownForHash,omitempty"`
	// Pending preserves the pre-reset old-context facts per profile so the
	// notice survives the entry replacement.
	Pending map[string]*PendingEditNotice `json:"pendingEditNotice,omitempty"`
}

// contextHashInput is the canonical JSON form of the input-free context
// (design/20). Fields are the effective resolver context: target, mode,
// base argv, config-declared env, effective absences, cwd, resolved hooks.
type contextHashInput struct {
	Target     string                    `json:"target"`
	Mode       string                    `json:"mode"`
	Argv       []string                  `json:"argv"`
	ConfigEnv  map[string]string         `json:"configEnv"`
	AbsentEnv  []string                  `json:"absentEnv"`
	WorkingDir string                    `json:"workingDir"`
	Lifecycle  *launch.ResolvedLifecycle `json:"lifecycle"`
}

// ContextHash computes the input-free context hash from the resolver's
// effective base context (design/08). Using the resolver's own merge means
// a no-op unset restored by a later layer, or two case-variant env keys on
// Windows, hash exactly as the real launch resolves — never a second merge
// implementation.
func ContextHash(bc *launch.BaseContext) string {
	if bc == nil {
		return ""
	}
	in := contextHashInput{
		Target:     bc.Target,
		Mode:       bc.Mode,
		Argv:       append([]string(nil), bc.Args...),
		ConfigEnv:  bc.ConfigEnv,
		AbsentEnv:  bc.AbsentEnvNames,
		WorkingDir: bc.WorkingDir,
		Lifecycle:  bc.Lifecycle,
	}
	data, err := json.Marshal(in) // Go sorts map keys — canonical enough
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EnsureBucketKey loads (or mints and persists) the per-game random bucket
// key used to digest supplied input values (design/20). Callers compute the
// value digest with this key before recording, so a first launch and later
// launches key identically.
func EnsureBucketKey(gameID, configDir string) (string, error) {
	var key string
	err := mutateHistory(gameID, configDir, func(h *GameHistory) {
		key = h.BucketKey // mutateHistory already minted it if absent
	})
	return key, err
}

// BucketKeyIfExists returns the per-game bucket key WITHOUT minting it or
// creating the history file — for the read-only failure path,
// which must compare a proven input combination's digest without mutating
// history. Empty when no key has been minted: the combination is genuinely
// unproven.
func BucketKeyIfExists(gameID, configDir string) string {
	h, err := LoadHistory(gameID, configDir)
	if err != nil || h == nil {
		return ""
	}
	return h.BucketKey
}

// LoadHistory reads the game's track record, degrading a missing or corrupt
// file to an empty record rather than failing a lifecycle operation
// (design/08).
func LoadHistory(gameID, configDir string) (*GameHistory, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, err
	}
	// Resolve the read through the SAFE path: a missing/corrupt file degrades to
	// an empty record, but an ID that escapes the config base (`..`, symlink) is
	// an ERROR, not an empty record — the same boundary a runtime-state read
	// enforces (design/07). This blocks `doctor ../victim --show-last-good` from
	// parsing an external history.json.
	histPath, err := cp.SafeHistoryPath(gameID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(histPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyHistory(), nil
		}
		return emptyHistory(), nil // unreadable degrades to no track record
	}
	var h GameHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return emptyHistory(), nil // corrupt degrades to no track record
	}
	if h.Profiles == nil {
		h.Profiles = map[string]*HistoryEntry{}
	}
	return &h, nil
}

func emptyHistory() *GameHistory {
	return &GameHistory{Version: 1, Profiles: map[string]*HistoryEntry{}}
}

// saveHistoryFailHook forces saveHistory to fail — test-only, to exercise the
// record-first ordering (a history-write failure must abort the
// runtime transition so a retry re-credits exactly once, never losing the
// event). Guarded by the caller's transition lock, so no synchronization is
// needed on the var itself.
var saveHistoryFailHook func() error

// SetSaveHistoryFailHookForTesting installs a hook whose non-nil error makes
// the next saveHistory calls fail. Returns a restore func.
func SetSaveHistoryFailHookForTesting(fn func() error) func() {
	prev := saveHistoryFailHook
	saveHistoryFailHook = fn
	return func() { saveHistoryFailHook = prev }
}

func saveHistory(gameID, configDir string, h *GameHistory) error {
	if saveHistoryFailHook != nil {
		if err := saveHistoryFailHook(); err != nil {
			return err
		}
	}
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return err
	}
	if err := cp.EnsureGameDir(gameID); err != nil {
		return err
	}
	path := cp.GetHistoryPath(gameID)
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".history-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// mutateHistory runs one read-modify-write under the per-game transition
// lock, so a server delivery callback and a CLI stop cannot overwrite each
// other's counters (design/20: atomic rename prevents torn reads, not lost
// updates — the lock does). History failures never abort a lifecycle
// operation: callers log and continue.
func mutateHistory(gameID, configDir string, mutate func(*GameHistory)) error {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	return applyHistoryLocked(gameID, configDir, mutate)
}

// applyHistoryLocked runs one history RMW assuming the caller ALREADY holds
// the per-game transition lock (the flock is not re-entrant). Used inside a
// runtime-state transition so history and claim stay consistent under one
// lock acquisition.
func applyHistoryLocked(gameID, configDir string, mutate func(*GameHistory)) error {
	h, err := LoadHistory(gameID, configDir)
	if err != nil {
		return err
	}
	if h.BucketKey == "" {
		h.BucketKey = NewFencingID()
	}
	if h.Profiles == nil {
		h.Profiles = map[string]*HistoryEntry{}
	}
	mutate(h)
	return saveHistory(gameID, configDir, h)
}

// mutateHistoryFenced runs a history RMW under the transition lock, but only
// while the current claim still carries expectLaunchID — a stale event from
// a superseded launch must never reset a successor's history.
// A missing claim or a launch mismatch skips silently.
func mutateHistoryFenced(gameID, configDir, expectLaunchID string, mutate func(*GameHistory)) error {
	if expectLaunchID == "" {
		return nil
	}
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	claim, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return err
	}
	if claim == nil || claim.LaunchID != expectLaunchID {
		return nil // superseded or gone — not our launch anymore
	}
	return applyHistoryLocked(gameID, configDir, mutate)
}

// entryForContext returns the profile entry for the given context hash,
// resetting it (counters zeroed, buckets dropped) when the stored hash
// differs — the context-level reset (design/08). Before replacing a proven
// entry whose last outcome was a non-config failure, it arms the pending
// edit notice so the notice survives the reset.
func (h *GameHistory) entryForContext(profile, contextHash string) *HistoryEntry {
	e := h.Profiles[profile]
	if e == nil || e.ContextHash != contextHash {
		if e != nil && e.ContextHash != contextHash && e.WorkloadStarts > 0 &&
			e.LastFailure != nil && e.LastFailure.Class != CauseConfig &&
			h.NoticeShownForHash[profile] != contextHash {
			if h.Pending == nil {
				h.Pending = map[string]*PendingEditNotice{}
			}
			h.Pending[profile] = &PendingEditNotice{NewHash: contextHash, OldStarts: e.WorkloadStarts, OldClass: e.LastFailure.Class}
		}
		e = &HistoryEntry{ContextHash: contextHash}
		h.Profiles[profile] = e
	}
	return e
}

// SuccessBucket carries a proven supplied-input combination's identity.
type SuccessBucket struct {
	InputNames   []string
	PerInputDecl map[string]string // input name -> its declaration hash
	DeclHash     string            // combined declaration hash
	ValueDigest  string            // per-game-keyed digest of supplied values
}

// HistorySuccessIdentity is the non-secret information (plus the 0600-protected
// lastGood snapshot) pinned in the runtime claim at publication so ANY Stage 4
// promotion path — synchronous start, passive status observation, bridge
// attachment, restart recovery — can record this launch's verified start from
// the claim alone, never a hot-config recompute.
type HistorySuccessIdentity struct {
	Snapshot ContextSnapshot `json:"snapshot"`
	Bucket   SuccessBucket   `json:"bucket"`
}

// RecordWorkloadStart is the Stage 4 verified point: workloadStarts++,
// consecutiveFailures reset, lastGood refreshed, and the supplied-input
// bucket recorded (design/20). Fenced to launchID: a promote whose claim
// was replaced credits nothing.
func RecordWorkloadStart(gameID, configDir, launchID, profile, contextHash string, snap ContextSnapshot, bucket SuccessBucket, at time.Time) error {
	return mutateHistoryFenced(gameID, configDir, launchID, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		e.WorkloadStarts++
		e.ConsecutiveFailures = 0
		e.LastSuccessAt = at
		e.recordBucket(HistoryBucket{
			InputNames:   append([]string(nil), bucket.InputNames...),
			PerInputDecl: bucket.PerInputDecl,
			DeclHash:     bucket.DeclHash,
			ValueDigest:  bucket.ValueDigest,
			Count:        1,
			LastAt:       at,
		})
		if h.LastGood == nil {
			h.LastGood = map[string]*HistoryLastGood{}
		}
		h.LastGood[profile] = &HistoryLastGood{ContextHash: contextHash, EntrySnapshot: snap, At: at}
	})
}

// RecordBridgeConnect is the Stage 5 connected point: bridgeConnects++.
// Fenced to launchID.
func RecordBridgeConnect(gameID, configDir, launchID, profile, contextHash string, at time.Time) error {
	return mutateHistoryFenced(gameID, configDir, launchID, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).BridgeConnects++
	})
}

// RecordDeliveryVerified is the fully-verified welcome point:
// deliveriesVerified++. Fenced to launchID.
func RecordDeliveryVerified(gameID, configDir, launchID, profile, contextHash string, at time.Time) error {
	return mutateHistoryFenced(gameID, configDir, launchID, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).DeliveriesVerified++
	})
}

// applyCleanStop records a verified stop (cleanStops++) using an already held
// transition lock, idempotent by operationID and recorded BEFORE the claim is
// removed — so a history-write failure aborts the removal and a
// retry re-credits at most once. Returns the write error to the caller so the
// removal is gated on the credit committing.
func applyCleanStop(gameID, configDir, profile, contextHash, operationID string, at time.Time) error {
	return applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		if e.creditEventOnce("stop:" + operationID) {
			e.CleanStops++
		}
	})
}

// ApplyBridgeConnectLocked records bridgeConnects++ using an already held
// transition lock, idempotent by connectionID — called INSIDE the attachment-
// publication transition, so it fires exactly once per credential-bound
// attachment across all connect paths and across a retry or crash-replay.
// Returns the write error so the runtime commit is gated on the credit.
func ApplyBridgeConnectLocked(gameID, configDir, profile, contextHash, connectionID string) error {
	return applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		if e.creditEventOnce("connect:" + connectionID) {
			e.BridgeConnects++
		}
	})
}

// ApplyWorkloadStartLocked records a Stage 4 verified start (workloadStarts++,
// consecutiveFailures=0, lastGood refresh, input bucket) using an already held
// transition lock — called INSIDE the fenced transition that flips a completed
// starting claim to active, so EVERY Stage 4 promotion (synchronous start,
// passive status observation, bridge attachment, restart recovery) credits the
// start exactly once. The identity is the one pinned in the
// claim at publication, never recomputed from hot config.
func ApplyWorkloadStartLocked(gameID, configDir, profile, contextHash, launchID string, snap ContextSnapshot, bucket SuccessBucket, at time.Time) error {
	if contextHash == "" {
		return nil
	}
	return applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		// Idempotent by launchID: if this launch's start was
		// already credited (a retried promote after a runtime-save failure, or
		// a passive promotion after a crash between the history and runtime
		// writes), do not double-count.
		if launchID != "" {
			for _, id := range e.CreditedLaunchIDs {
				if id == launchID {
					return
				}
			}
			e.CreditedLaunchIDs = append(e.CreditedLaunchIDs, launchID)
			if len(e.CreditedLaunchIDs) > creditedLaunchCap {
				e.CreditedLaunchIDs = e.CreditedLaunchIDs[len(e.CreditedLaunchIDs)-creditedLaunchCap:]
			}
		}
		e.WorkloadStarts++
		e.ConsecutiveFailures = 0
		e.LastSuccessAt = at
		e.recordBucket(HistoryBucket{
			InputNames:   append([]string(nil), bucket.InputNames...),
			PerInputDecl: bucket.PerInputDecl,
			DeclHash:     bucket.DeclHash,
			ValueDigest:  bucket.ValueDigest,
			Count:        1,
			LastAt:       at,
		})
		if h.LastGood == nil {
			h.LastGood = map[string]*HistoryLastGood{}
		}
		h.LastGood[profile] = &HistoryLastGood{ContextHash: contextHash, EntrySnapshot: snap, At: at}
	})
}

// creditedLaunchCap bounds the per-entry dedup ring — a launch is credited
// once, and only very recent launchIDs can race a double-credit, so a small
// LRU window is sufficient.
const creditedLaunchCap = 16

// ApplyPinnedWorkloadStartLocked records a Stage 4 verified start from the
// identity pinned in the claim, using an already-held runtime-state transition
// lock. No-op for a claim without a pinned identity. The caller
// MUST invoke this only when the transition actually flips starting→active for
// a start (never for a recovered stop/kill, whose workload started earlier).
func ApplyPinnedWorkloadStartLocked(gameID, configDir string, st *RuntimeState, at time.Time) error {
	if st == nil || st.HistorySuccess == nil || st.HistoryContextHash == "" {
		return nil
	}
	return ApplyWorkloadStartLocked(gameID, configDir, EffectiveClaimProfile(st),
		st.HistoryContextHash, st.LaunchID, st.HistorySuccess.Snapshot, st.HistorySuccess.Bucket, at)
}

// ApplyActionFailureLocked records a terminal failure of an accepted attempt
// (a stop/kill action failure, or an unobserved start) using an already held
// transition lock, fenced by the caller's own launch/operation identity — so
// a stale completion can never bump a successor's history. The
// supplied input names are recorded so an input-bearing attempt does not
// serialize as if no inputs were supplied (design/08:20).
func ApplyActionFailureLocked(gameID, configDir, profile, contextHash, outcome, class string, inputNames []string, at time.Time) {
	_ = applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		e.ConsecutiveFailures++
		e.LastFailure = &HistoryFailure{Outcome: outcome, Class: class, InputNames: append([]string(nil), inputNames...), At: at}
	})
}

// RecordFailure records a terminal failure of an accepted attempt with a
// resolved context (design/20): lastFailure + consecutiveFailures++. Only
// accepted-attempt terminal failures call this — call-class and
// config_invalid never mutate history. Fenced to launchID.
func RecordFailure(gameID, configDir, launchID, profile, contextHash, outcome, class string, inputNames []string, at time.Time) error {
	return mutateHistoryFenced(gameID, configDir, launchID, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		e.ConsecutiveFailures++
		e.LastFailure = &HistoryFailure{Outcome: outcome, Class: class, At: at, InputNames: append([]string(nil), inputNames...)}
	})
}

// InvalidateChangedInputDeclarations drops every bucket whose recorded
// per-input declaration hash no longer matches the current declaration for
// that input name, leaving bare proof and unrelated input sets intact
// (design/08: an input-declaration edit resets exactly that input's
// buckets). currentDecls maps input name -> current declaration hash.
func InvalidateChangedInputDeclarations(gameID, configDir, profile string, currentDecls map[string]string) error {
	return mutateHistory(gameID, configDir, func(h *GameHistory) {
		e := h.Profiles[profile]
		if e == nil {
			return
		}
		kept := e.Buckets[:0]
		for _, b := range e.Buckets {
			changed := false
			for name, recordedDecl := range b.PerInputDecl {
				// A declaration that was REMOVED (absent from the current map)
				// invalidates its bucket exactly like a changed hash — else a
				// deleted-then-readded declaration would resurrect proof for
				// values that belonged to the earlier generation.
				cur, ok := currentDecls[name]
				if !ok || cur != recordedDecl {
					changed = true
					break
				}
			}
			if !changed {
				kept = append(kept, b)
			}
		}
		e.Buckets = kept
	})
}

// EditNotice returns the one-line edit-visibility notice for a profile
// whose CURRENT context hash is newHash, once per edit (design/08). It fires
// when the previous proven context (non-config last failure) was replaced —
// whether that replacement was already recorded (a persisted PendingEdit-
// Notice) or is only now observed (the stored entry still carries the old
// hash because no success/failure has reset it yet). Marks the notice shown
// so it never repeats for newHash. Call-class errors never call this.
func EditNotice(gameID, configDir, profile, newHash string) (string, error) {
	var notice string
	err := mutateHistory(gameID, configDir, func(h *GameHistory) {
		if h.NoticeShownForHash == nil {
			h.NoticeShownForHash = map[string]string{}
		}
		alreadyShown := h.NoticeShownForHash[profile] == newHash

		// A pre-armed pending notice (set when a Record* reset the entry).
		if p := h.Pending[profile]; p != nil && p.NewHash == newHash {
			delete(h.Pending, profile)
			if !alreadyShown {
				h.NoticeShownForHash[profile] = newHash
				notice = editNoticeText(p.OldClass, p.OldStarts)
			}
			return
		}

		// A live-detected change: the entry still carries the OLD proven
		// context because nothing has reset it yet.
		prev := h.Profiles[profile]
		if prev == nil || prev.ContextHash == newHash {
			return
		}
		if prev.WorkloadStarts == 0 || prev.LastFailure == nil || prev.LastFailure.Class == CauseConfig {
			return
		}
		if alreadyShown {
			return
		}
		h.NoticeShownForHash[profile] = newHash
		notice = editNoticeText(prev.LastFailure.Class, prev.WorkloadStarts)
	})
	return notice, err
}

func editNoticeText(class string, starts uint64) string {
	return fmt.Sprintf(
		"configuration changed after a %s-class failure; the previous context had %d successful starts — verify this edit was intended",
		class, starts)
}

// BucketValueDigest keys a sorted name=value list with the per-game bucket
// key, so supplied values never persist in the clear and cannot be
// correlated across games (design/20).
func BucketValueDigest(bucketKey string, nameValues map[string]string) string {
	if len(nameValues) == 0 {
		return ""
	}
	names := make([]string, 0, len(nameValues))
	for n := range nameValues {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	h.Write([]byte(bucketKey))
	for _, n := range names {
		h.Write([]byte{0})
		h.Write([]byte(n))
		h.Write([]byte{'='})
		h.Write([]byte(nameValues[n]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// InputDeclHash hashes the declarations (not values) of the supplied input
// names, so editing an input's declaration changes its bucket key
// (design/08).
func InputDeclHash(game config.GameConfig, inputNames []string) string {
	if len(inputNames) == 0 {
		return ""
	}
	names := append([]string(nil), inputNames...)
	sort.Strings(names)
	type declView struct {
		Name string                   `json:"name"`
		Decl config.LaunchInputConfig `json:"decl"`
	}
	views := make([]declView, 0, len(names))
	for _, n := range names {
		views = append(views, declView{Name: n, Decl: game.LaunchInputs[n]})
	}
	data, err := json.Marshal(views)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
