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
}

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
// eligible result even though the entry was already replaced (round 10).
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
	// notice survives the entry replacement (round 10).
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

// LoadHistory reads the game's track record, degrading a missing or corrupt
// file to an empty record rather than failing a lifecycle operation
// (design/08).
func LoadHistory(gameID, configDir string) (*GameHistory, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(cp.GetHistoryPath(gameID))
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

func saveHistory(gameID, configDir string, h *GameHistory) error {
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
// lock acquisition (review round 10).
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
// a superseded launch must never reset a successor's history (round 10).
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
// edit notice so the notice survives the reset (round 10).
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

// applyCleanStop records a verified stop (cleanStops++) using an already
// held transition lock — called from the stop completion inside the same
// removal decision, so it cannot race a successor claim (round 10).
func applyCleanStop(gameID, configDir, profile, contextHash string, at time.Time) {
	_ = applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).CleanStops++
	})
}

// ApplyDeliveryVerifiedLocked records deliveriesVerified++ using an already
// held transition lock — called INSIDE the delivery callback's fenced
// transition (launchID + connectionID already validated), so a stale
// callback can never bump a successor's history (round 10).
func ApplyDeliveryVerifiedLocked(gameID, configDir, profile, contextHash string) {
	_ = applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).DeliveriesVerified++
	})
}

// ApplyBridgeConnectLocked records bridgeConnects++ using an already held
// transition lock — called INSIDE the attachment-publication transition, so
// it fires exactly once per credential-bound attachment across all connect
// paths (round 10).
func ApplyBridgeConnectLocked(gameID, configDir, profile, contextHash string) {
	_ = applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).BridgeConnects++
	})
}

// applyActionFailure records a terminal stop/kill action failure using an
// already held transition lock, fenced by the completion's own launch +
// operation identity (round 10).
func applyActionFailure(gameID, configDir, profile, contextHash, outcome, class string, at time.Time) {
	_ = applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		e.ConsecutiveFailures++
		e.LastFailure = &HistoryFailure{Outcome: outcome, Class: class, At: at}
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
				if cur, ok := currentDecls[name]; ok && cur != recordedDecl {
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
