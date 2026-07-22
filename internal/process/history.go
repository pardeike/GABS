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

// ContextSnapshot is the last-known-good launch context, printed only by
// doctor --show-last-good (design/08). It may hold env VALUES, so it lives
// only in the 0600 history file and never reaches an MCP result.
type ContextSnapshot struct {
	Target     string            `json:"target,omitempty"`
	Mode       string            `json:"mode,omitempty"`
	Args       []string          `json:"args,omitempty"`
	ConfigEnv  map[string]string `json:"configEnv,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
}

// HistoryFailure is the last recorded failure for a context (design/08).
type HistoryFailure struct {
	Outcome    string    `json:"outcome"`
	Class      string    `json:"class"`
	At         time.Time `json:"at"`
	InputNames []string  `json:"inputNames,omitempty"`
}

// HistoryBucket records a proven supplied-input combination: the inputs'
// declaration hash plus a keyed digest of their supplied values, so
// scenario=arena and scenario=tutorial are distinct (design/08).
type HistoryBucket struct {
	DeclHash    string    `json:"declHash"`
	ValueDigest string    `json:"valueDigest"`
	Count       uint64    `json:"count"`
	LastAt      time.Time `json:"lastAt"`
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

// recordBucket adds or refreshes a value bucket, capped LRU per input set.
func (e *HistoryEntry) recordBucket(declHash, valueDigest string, at time.Time) {
	for i := range e.Buckets {
		if e.Buckets[i].DeclHash == declHash && e.Buckets[i].ValueDigest == valueDigest {
			e.Buckets[i].Count++
			e.Buckets[i].LastAt = at
			return
		}
	}
	e.Buckets = append(e.Buckets, HistoryBucket{DeclHash: declHash, ValueDigest: valueDigest, Count: 1, LastAt: at})
	// Evict LRU within this declaration set if over the cap.
	var idxs []int
	for i := range e.Buckets {
		if e.Buckets[i].DeclHash == declHash {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) <= bucketValueCap {
		return
	}
	sort.Slice(idxs, func(a, b int) bool {
		return e.Buckets[idxs[a]].LastAt.Before(e.Buckets[idxs[b]].LastAt)
	})
	evict := idxs[0]
	e.Buckets = append(e.Buckets[:evict], e.Buckets[evict+1:]...)
}

// HistoryLastGood is a refreshable last-known-good pointer per profile.
type HistoryLastGood struct {
	ContextHash   string          `json:"contextHash"`
	EntrySnapshot ContextSnapshot `json:"entrySnapshot"`
	At            time.Time       `json:"at"`
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
}

// contextHashInput is the input-FREE view of a launch context, hashed for
// the track record (design/08, design/20): target, mode, base argv, the
// config-declared env layer, cwd, and resolved hooks — launch inputs and
// managed env excluded, so caller-chosen inputs and unrelated edits never
// reset proof.
type contextHashInput struct {
	Target     string                    `json:"target"`
	Mode       string                    `json:"mode"`
	Argv       []string                  `json:"argv"`
	ConfigEnv  map[string]string         `json:"configEnv"`
	UnsetEnv   []string                  `json:"unsetEnv"`
	WorkingDir string                    `json:"workingDir"`
	Lifecycle  *launch.ResolvedLifecycle `json:"lifecycle"`
}

// ContextHash computes the input-free context hash for one profile. It is
// composed from config directly (never from a post-input Resolved), so two
// launches of the same profile with different supplied inputs share one
// hash and become distinct buckets, not distinct contexts (design/08).
func ContextHash(game config.GameConfig, profile string, lifecycle *launch.ResolvedLifecycle) string {
	argv := append([]string(nil), game.Args...)
	env := map[string]string{}
	applyConfigLayer(env, game.UnsetEnv, game.Env)
	workingDir := game.WorkingDir
	if profile != "" {
		if p, ok := game.Profiles[profile]; ok {
			argv = append(argv, p.Args...)
			applyConfigLayer(env, p.UnsetEnv, p.Env)
			if p.WorkingDir != "" {
				workingDir = p.WorkingDir
			}
		}
	}
	unset := configUnsetNames(game, profile)

	in := contextHashInput{
		Target:     game.Target,
		Mode:       game.LaunchMode,
		Argv:       argv,
		ConfigEnv:  env,
		UnsetEnv:   unset,
		WorkingDir: workingDir,
		Lifecycle:  lifecycle,
	}
	data, err := json.Marshal(in) // Go sorts map keys — canonical enough
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func applyConfigLayer(env map[string]string, unset []string, layer map[string]string) {
	for _, k := range unset {
		delete(env, k)
	}
	for k, v := range layer {
		env[k] = v
	}
}

func configUnsetNames(game config.GameConfig, profile string) []string {
	seen := map[string]bool{}
	for _, k := range game.UnsetEnv {
		seen[k] = true
	}
	if profile != "" {
		if p, ok := game.Profiles[profile]; ok {
			for _, k := range p.UnsetEnv {
				seen[k] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// entryForContext returns the profile entry for the given context hash,
// resetting it (counters zeroed, buckets dropped) when the stored hash
// differs — the context-level reset (design/08). The reset caller is
// responsible for the lastGood snapshot and edit notice.
func (h *GameHistory) entryForContext(profile, contextHash string) *HistoryEntry {
	e := h.Profiles[profile]
	if e == nil || e.ContextHash != contextHash {
		e = &HistoryEntry{ContextHash: contextHash}
		h.Profiles[profile] = e
	}
	return e
}

// RecordWorkloadStart is the Stage 4 verified point: workloadStarts++,
// consecutiveFailures reset, lastGood refreshed, and the supplied-input
// bucket recorded (design/20).
func RecordWorkloadStart(gameID, configDir, profile, contextHash string, snap ContextSnapshot, inputNames []string, declHash, valueDigest string, at time.Time) error {
	return mutateHistory(gameID, configDir, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		e.WorkloadStarts++
		e.ConsecutiveFailures = 0
		e.LastSuccessAt = at
		e.recordBucket(declHash, valueDigest, at)
		if h.LastGood == nil {
			h.LastGood = map[string]*HistoryLastGood{}
		}
		h.LastGood[profile] = &HistoryLastGood{ContextHash: contextHash, EntrySnapshot: snap, At: at}
	})
}

// RecordBridgeConnect is the Stage 5 connected point: bridgeConnects++.
func RecordBridgeConnect(gameID, configDir, profile, contextHash string, at time.Time) error {
	return mutateHistory(gameID, configDir, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).BridgeConnects++
	})
}

// RecordDeliveryVerified is the fully-verified welcome point:
// deliveriesVerified++.
func RecordDeliveryVerified(gameID, configDir, profile, contextHash string, at time.Time) error {
	return mutateHistory(gameID, configDir, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).DeliveriesVerified++
	})
}

// RecordCleanStop is the verified-stop point: cleanStops++.
func RecordCleanStop(gameID, configDir, profile, contextHash string, at time.Time) error {
	return mutateHistory(gameID, configDir, func(h *GameHistory) {
		h.entryForContext(profile, contextHash).CleanStops++
	})
}

// RecordFailure records a terminal failure of an accepted attempt with a
// resolved context (design/20): lastFailure + consecutiveFailures++. Only
// accepted-attempt terminal failures call this — call-class and
// config_invalid never mutate history.
func RecordFailure(gameID, configDir, profile, contextHash, outcome, class string, inputNames []string, at time.Time) error {
	return mutateHistory(gameID, configDir, func(h *GameHistory) {
		e := h.entryForContext(profile, contextHash)
		e.ConsecutiveFailures++
		e.LastFailure = &HistoryFailure{Outcome: outcome, Class: class, At: at, InputNames: append([]string(nil), inputNames...)}
	})
}

// ResetInputBuckets drops only the buckets for a changed input declaration,
// leaving base counters and other buckets intact (design/08: an
// input-declaration edit resets exactly that input's buckets).
func ResetInputBuckets(gameID, configDir, profile, declHash string) error {
	return mutateHistory(gameID, configDir, func(h *GameHistory) {
		e := h.Profiles[profile]
		if e == nil {
			return
		}
		kept := e.Buckets[:0]
		for _, b := range e.Buckets {
			if b.DeclHash != declHash {
				kept = append(kept, b)
			}
		}
		e.Buckets = kept
	})
}

// EditNotice decides whether the one-line edit-visibility notice should
// fire for a profile whose CURRENT context hash is newHash, and marks it
// shown so it fires once per edit (design/08). It returns the notice text,
// or "" when the conditions are not met: the stored context differs, was
// proven, its last failure was non-config, and the notice has not already
// been shown for newHash.
func EditNotice(gameID, configDir, profile, newHash string) (string, error) {
	var notice string
	err := mutateHistory(gameID, configDir, func(h *GameHistory) {
		prev := h.Profiles[profile]
		if prev == nil || prev.ContextHash == newHash {
			return // no prior context, or unchanged — not an edit
		}
		if prev.WorkloadStarts == 0 {
			return // the old context was never proven
		}
		if prev.LastFailure == nil || prev.LastFailure.Class == CauseConfig {
			return // only a non-config-class last failure arms the notice
		}
		if h.NoticeShownForHash == nil {
			h.NoticeShownForHash = map[string]string{}
		}
		if h.NoticeShownForHash[profile] == newHash {
			return // already shown once for this edit
		}
		h.NoticeShownForHash[profile] = newHash
		notice = fmt.Sprintf(
			"configuration changed after a %s-class failure; the previous context had %d successful starts — verify this edit was intended",
			prev.LastFailure.Class, prev.WorkloadStarts)
	})
	return notice, err
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
