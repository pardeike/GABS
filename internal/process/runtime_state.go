package process

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
)

// linkRuntimeState and renameRuntimeState are injectable so tests can force
// the link-less claim fallback and its failure cleanup.
var (
	linkRuntimeState   = os.Link
	renameRuntimeState = os.Rename
)

const (
	RuntimeStateStatusStarting = "starting"
	RuntimeStateStatusRunning  = "running"

	// RuntimeSchemaVersion is stamped into every claim at creation — the
	// legacy-migration discriminator (design/07). Endpoint absence is NOT
	// the predicate: a fresh claim before endpoint allocation and an
	// external snapshot both lack endpoints and carry the marker.
	RuntimeSchemaVersion = 2

	PhaseStarting = "starting"
	PhaseActive   = "active"
	PhaseStopping = "stopping"
	PhaseKilling  = "killing"

	SpawnStatePreflight = "preflight"
	SpawnStateSpawning  = "spawning"
	SpawnStateSpawned   = "spawned"
	SpawnStateFailed    = "failed"

	SourceGABS     = "gabs"
	SourceExternal = "external"

	PIDRoleWorkload = "workload"
	PIDRoleHelper   = "helper"

	// AppliedInputsStateUnavailable marks claims whose launch inputs are
	// unknowable (external snapshots) — distinct from an empty list.
	AppliedInputsStateUnavailable = "unavailable"
)

var ErrRuntimeStateExists = errors.New("runtime state already exists")

// ErrNoRuntimeClaim marks a transition that found no claim to mutate — for
// fenced completions this is the claim-was-removed case, handled like a
// fencing violation (the completion is discarded, design/06).
var ErrNoRuntimeClaim = errors.New("no runtime claim exists")

// RuntimeEndpoint is the claim-persisted GABP endpoint: the normal
// attachment source after a CLI start or server restart (bridge.json stays
// diagnostic).
type RuntimeEndpoint struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// RuntimeOperation is the in-flight lifecycle attempt. The executor is
// distinct from the claim or attachment owner: a CLI stop executes without
// taking ownership from the server that owns the live bridge.
type RuntimeOperation struct {
	OperationID          string    `json:"operationId"`
	Action               string    `json:"action"` // start|stop|kill
	ExecutorInstanceID   string    `json:"executorInstanceId,omitempty"`
	ExecutorPID          int       `json:"executorPid,omitempty"`
	ExecutorPIDStartTime int64     `json:"executorPidStartTime,omitempty"`
	AttemptStartedAt     time.Time `json:"attemptStartedAt"`
	Deadline             time.Time `json:"deadline,omitempty"`
}

// RuntimeAttachment is the persisted bridge-attachment record: running
// evidence for other processes only while the lease is fresh and the owner
// fingerprint still matches a live process (design/04).
type RuntimeAttachment struct {
	ConnectionID      string    `json:"connectionId"`
	OwnerInstanceID   string    `json:"ownerInstanceId,omitempty"`
	OwnerPID          int       `json:"ownerPid,omitempty"`
	OwnerPIDStartTime int64     `json:"ownerPidStartTime,omitempty"`
	ObservedAt        time.Time `json:"observedAt"`
	LeaseDeadline     time.Time `json:"leaseDeadline,omitempty"`
}

// RuntimeActionResult is the persisted outcome of the last stop/kill
// attempt — this single field replaces an operation journal (design/06).
// Detail carries non-hook failure facts (a built-in action's OS error, a
// verification summary) that have no exit code or stderr to speak for them.
type RuntimeActionResult struct {
	Action          string    `json:"action"`
	Outcome         string    `json:"outcome"`
	ExitCode        *int      `json:"exitCode,omitempty"`
	StderrTail      string    `json:"stderrTail,omitempty"`
	Detail          string    `json:"detail,omitempty"`
	TreeKillWarning bool      `json:"treeKillWarning,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
}

// RuntimeBuiltinFallback pins the built-in stop/kill contract at claim
// creation alongside stopProcessName and the PID role (design/07): recovery
// and cross-process stops execute the mechanism decided at launch, never
// one re-derived from mutable config.
type RuntimeBuiltinFallback struct {
	GracefulStrategy string `json:"gracefulStrategy"`
	ForceStrategy    string `json:"forceStrategy"`
}

// RuntimeContextDigests are the non-reversible expected-context digests
// pinned at spawn for delayed delivery verification (design/03): a random
// per-launch salt plus salted hashes of the argv payload (excluding
// argv[0]), the canonical cwd, and each forwarded env value.
type RuntimeContextDigests struct {
	Salt       string `json:"salt"`
	ArgvSHA256 string `json:"argvSha256,omitempty"`
	// CwdSHA256 empty with CwdUnverifiable false means the spawn-side
	// canonicalization failed: the channel is unknown, never a guessed
	// digest and never the legacy-relative unverifiable case.
	CwdSHA256 string `json:"cwdSha256,omitempty"`
	// Channel membership is persisted explicitly (managed versus config-
	// context) — the managed layer includes non-prefixed names (SteamAppId,
	// SystemRoot) and prefix inference is not a persistable contract.
	ManagedEnvSHA256 map[string]string `json:"managedEnvSha256,omitempty"`
	ContextEnvSHA256 map[string]string `json:"contextEnvSha256,omitempty"`
	// AbsentEnvNames pins the GABS_ABSENT_ENV names (never values) for the
	// isolation check; CwdUnverifiable marks the contract-level
	// incomparable case (legacy relative workingDir) so verification
	// reports unverifiable instead of a false verdict (design/03).
	AbsentEnvNames  []string `json:"absentEnvNames,omitempty"`
	CwdUnverifiable bool     `json:"cwdUnverifiable,omitempty"`
}

// RuntimeContextDelivery is the persisted per-launch delivery verdict so
// games_status renders it after a restart without re-deriving.
type RuntimeContextDelivery struct {
	Overall  string            `json:"overall"` // verified|partial|unknown
	Channels map[string]string `json:"channels,omitempty"`
	Reasons  map[string]string `json:"reasons,omitempty"`
}

// RuntimeState captures the shared on-disk lifecycle for one game so multiple
// GABS processes can avoid racing the same launch. Field contract:
// design/07-runtime-state.md.
type RuntimeState struct {
	GameID          string    `json:"gameId"`
	Status          string    `json:"status"`
	OwnerPID        int       `json:"ownerPid"`
	OwnerInstanceID string    `json:"ownerInstanceId,omitempty"`
	OwnerLeaseUntil time.Time `json:"ownerLeaseUntil,omitempty"`
	OwnerLastActive time.Time `json:"ownerLastActive,omitempty"`
	GamePID         int       `json:"gamePid,omitempty"`
	StopProcessName string    `json:"stopProcessName,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`

	// Launch-profile lifecycle extensions. SchemaVersion 0 identifies a
	// legacy (pre-profile) claim.
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	LaunchID      string `json:"launchId,omitempty"`   // immutable, minted at claim creation (ABA fence)
	Generation    uint64 `json:"generation,omitempty"` // CAS revision, +1 per transition under the lock
	Phase         string `json:"phase,omitempty"`
	SpawnState    string `json:"spawnState,omitempty"`
	Source        string `json:"source,omitempty"`
	LaunchMode    string `json:"launchMode,omitempty"`
	PIDRole       string `json:"pidRole,omitempty"` // restart recovery never consults config for this
	PIDStartTime  int64  `json:"pidStartTime,omitempty"`
	Adopted       bool   `json:"adopted,omitempty"`

	// NormalizedFromLegacy marks a claim that entered the current schema
	// via the one-time legacy normalization (design/07) — the only claims
	// for which the legacy bridge.json endpoint may still be migrated.
	NormalizedFromLegacy bool `json:"normalizedFromLegacy,omitempty"`

	// HistoryContextHash pins the input-free track-record context hash at
	// claim creation (design/08): delivery, reconnect, stop, recovery, and
	// delayed completions credit THIS launch's context, never a value
	// recomputed from hot-reloaded config (review round 10).
	HistoryContextHash string `json:"historyContextHash,omitempty"`

	Profile           string   `json:"profile,omitempty"`
	AppliedInputNames []string `json:"appliedInputNames,omitempty"` // names only, never values
	// AppliedInputsState distinguishes "known to have used no inputs"
	// (empty) from "unknowable" — external snapshots must serialize
	// appliedLaunchInputsState: unavailable, never an empty list that
	// reads as a GABS launch without inputs (design/07).
	AppliedInputsState string `json:"appliedLaunchInputsState,omitempty"`
	ConfigRevision     string `json:"configRevision,omitempty"`

	// Lifecycle is the resolved hook snapshot pinned at claim creation —
	// every field affecting execution or result interpretation, so a custom
	// stopped code never degrades to unknown after a restart or profile
	// edit, and recovery never consults mutable config (design/07).
	Lifecycle *launch.ResolvedLifecycle `json:"lifecycle,omitempty"`

	// BuiltinFallback is the pinned graceful/force strategy for built-in
	// (hook-less) stop/kill actions (design/07).
	BuiltinFallback *RuntimeBuiltinFallback `json:"builtinFallback,omitempty"`

	Endpoint             *RuntimeEndpoint        `json:"endpoint,omitempty"`
	Operation            *RuntimeOperation       `json:"operation,omitempty"`
	Attachment           *RuntimeAttachment      `json:"attachment,omitempty"`
	LastActionResult     *RuntimeActionResult    `json:"lastActionResult,omitempty"`
	ContextDigests       *RuntimeContextDigests  `json:"contextDigests,omitempty"`
	ContextDelivery      *RuntimeContextDelivery `json:"contextDelivery,omitempty"`
	ProcessStartDeadline time.Time               `json:"processStartDeadline,omitempty"`
	ObservedProfile      string                  `json:"observedProfile,omitempty"` // external snapshots only
}

// NewRuntimeState creates a shared runtime record for the given launch spec:
// the complete pre-spawn snapshot, never a bare marker (design/05 Stage 2).
func NewRuntimeState(spec LaunchSpec, status string) RuntimeState {
	now := time.Now().UTC()
	pidRole := PIDRoleWorkload
	if spec.Mode == "SteamAppId" || spec.Mode == "EpicAppId" {
		// The tracked child is the URL-opener helper, never the workload.
		pidRole = PIDRoleHelper
	}
	return RuntimeState{
		GameID:          spec.GameId,
		Status:          status,
		OwnerPID:        os.Getpid(),
		StopProcessName: spec.StopProcessName,
		OwnerLastActive: now,
		UpdatedAt:       now,

		SchemaVersion:     RuntimeSchemaVersion,
		LaunchID:          NewFencingID(),
		Generation:        1,
		Phase:             PhaseStarting,
		SpawnState:        SpawnStatePreflight,
		Source:            SourceGABS,
		LaunchMode:        spec.Mode,
		PIDRole:           pidRole,
		Profile:           spec.Profile,
		AppliedInputNames: append([]string(nil), spec.AppliedInputs...),
		ConfigRevision:    spec.ConfigRevision,
		Lifecycle:         spec.Lifecycle,
		BuiltinFallback:   pinBuiltinFallback(),
	}
}

// pinBuiltinFallback records the platform's built-in graceful/force
// mechanism at claim creation (design/07): the pin travels with the claim
// so a later stop — after restart, config edit, or from another GABS
// process — executes what the launch decided.
func pinBuiltinFallback() *RuntimeBuiltinFallback {
	graceful, force := builtinStrategiesForPlatform()
	return &RuntimeBuiltinFallback{GracefulStrategy: graceful, ForceStrategy: force}
}

// NewFencingID mints a random 128-bit identity (launchID, operationID,
// connectionID). Domain-scoped fencing compares these, with the generation
// used only as the CAS revision (design/06).
func NewFencingID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is not survivable for identity minting
		panic(fmt.Sprintf("cannot mint fencing ID: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// ClaimRuntimeState creates the shared runtime state file if it does not yet exist.
func ClaimRuntimeState(gameID, configDir string, state RuntimeState) error {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return fmt.Errorf("failed to create config paths: %w", err)
	}
	if err := cp.EnsureGameDir(gameID); err != nil {
		return fmt.Errorf("failed to create game config dir: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	data, err := marshalRuntimeState(state)
	if err != nil {
		return err
	}

	// Publication is atomic WITH its full content: write the complete claim
	// to a same-directory temp file and hard-link it into place. The link
	// fails if a claim exists (preserving create-exclusivity), a failed
	// write publishes nothing, and a lock-free reader can never observe an
	// empty or partially written claim — plain O_EXCL-then-write exposes
	// exactly that window (design/05 Stage 2).
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create runtime temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write runtime state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to sync runtime state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close runtime temp file: %w", err)
	}

	if err := linkRuntimeState(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrRuntimeStateExists
		}
		return claimRuntimeStateWithoutLink(gameID, configDir, path, tmpPath, err)
	}
	return nil
}

// claimRuntimeStateWithoutLink is the fallback for filesystems that reject
// hard links (the primary path never takes it on macOS, Linux, or NTFS).
// Exclusivity still comes from O_EXCL, but the content is published by
// renaming the fully written temp file over the placeholder — all under
// the transition lock. LoadRuntimeState takes the same lock whenever it
// reads a torn or empty claim, so no reader ever acts on the placeholder
// window, and a failure never leaves a partial claim behind.
func claimRuntimeStateWithoutLink(gameID, configDir, path, tmpPath string, linkErr error) error {
	lock, err := AcquireTransitionLock(gameID, configDir, 5*time.Second)
	if err != nil {
		return fmt.Errorf("claim fallback after link failure (%v): %w", linkErr, err)
	}
	defer lock.Release()

	f, ferr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if ferr != nil {
		if errors.Is(ferr, os.ErrExist) {
			return ErrRuntimeStateExists
		}
		return fmt.Errorf("failed to create runtime state: %w", ferr)
	}
	f.Close()
	if err := renameRuntimeState(tmpPath, path); err != nil {
		os.Remove(path)
		return fmt.Errorf("failed to publish runtime state: %w", err)
	}
	return nil
}

// SaveRuntimeState overwrites the shared runtime state file in-place.
func SaveRuntimeState(gameID, configDir string, state RuntimeState) error {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return fmt.Errorf("failed to create config paths: %w", err)
	}
	if err := cp.EnsureGameDir(gameID); err != nil {
		return fmt.Errorf("failed to create game config dir: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	data, err := marshalRuntimeState(state)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create runtime temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write runtime state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to sync runtime state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close runtime temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to publish runtime state: %w", err)
	}
	return nil
}

// LoadRuntimeState reads the shared runtime state file if present.
func LoadRuntimeState(gameID, configDir string) (*RuntimeState, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create config paths: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read runtime state: %w", err)
	}
	// The claim carries the per-launch token: tighten legacy 0644 files.
	// Failure to tighten surfaces — the token must not stay world-readable.
	if fi, statErr := os.Stat(path); statErr == nil && fi.Mode().Perm()&0o077 != 0 {
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
			return nil, fmt.Errorf("runtime state %s has loose permissions (%v) that cannot be tightened: %w", path, fi.Mode().Perm(), chmodErr)
		}
	}

	state, perr := parseRuntimeState(data)
	if perr != nil {
		// A torn or empty claim can only be the link-less fallback mid-
		// publication (design/05: readers never act on a partial claim).
		// The transition lock brackets that window: take it and re-read.
		lock, lerr := AcquireTransitionLock(gameID, configDir, 2*time.Second)
		if lerr != nil {
			return nil, fmt.Errorf("failed to parse runtime state (and could not take the transition lock to re-read): %w", perr)
		}
		data, err = os.ReadFile(path)
		lock.Release()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("failed to read runtime state: %w", err)
		}
		if state, perr = parseRuntimeState(data); perr != nil {
			return nil, fmt.Errorf("failed to parse runtime state: %w", perr)
		}
	}
	return state, nil
}

func parseRuntimeState(data []byte) (*RuntimeState, error) {
	if len(data) == 0 {
		return nil, errors.New("empty claim file")
	}
	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// RemoveRuntimeState removes the shared runtime state file for a game.
func RemoveRuntimeState(gameID, configDir string) error {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return fmt.Errorf("failed to create config paths: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove runtime state: %w", err)
	}

	return nil
}

// ResolveRuntimeStateStatus returns the currently observable status for a runtime state.
func ResolveRuntimeStateStatus(state *RuntimeState) string {
	if state == nil {
		return ""
	}

	if state.GamePID > 0 && isProcessAlive(state.GamePID) {
		if state.Status == RuntimeStateStatusStarting {
			return RuntimeStateStatusStarting
		}
		return RuntimeStateStatusRunning
	}

	if state.StopProcessName != "" {
		pids, err := findProcessesByNameFunc(state.StopProcessName)
		if err == nil && len(pids) > 0 {
			return RuntimeStateStatusRunning
		}
	}

	if state.Status == RuntimeStateStatusStarting && state.OwnerPID > 0 && isProcessAlive(state.OwnerPID) {
		return RuntimeStateStatusStarting
	}

	return ""
}

// RuntimeStateOwnedByAnotherLiveOwner reports whether a different live GABS
// owner still holds the shared runtime state.
func RuntimeStateOwnedByAnotherLiveOwner(state *RuntimeState, currentPID int, currentInstanceID string) bool {
	if state == nil || state.OwnerPID <= 0 {
		return false
	}
	if state.OwnerPID == currentPID && (state.OwnerInstanceID == "" || state.OwnerInstanceID == currentInstanceID) {
		return false
	}

	return isProcessAlive(state.OwnerPID)
}

// RuntimeStateOwnedByAnotherActiveOwner reports whether another live GABS
// owner still holds an unexpired runtime lease.
func RuntimeStateOwnedByAnotherActiveOwner(state *RuntimeState, currentPID int, currentInstanceID string, leaseDuration time.Duration, now time.Time) bool {
	if !RuntimeStateOwnedByAnotherLiveOwner(state, currentPID, currentInstanceID) {
		return false
	}
	return RuntimeOwnerLeaseActive(state, leaseDuration, now)
}

// RefreshRuntimeOwnerLease updates the runtime owner and extends its activity lease.
func RefreshRuntimeOwnerLease(state RuntimeState, ownerPID int, ownerInstanceID string, leaseDuration time.Duration, now time.Time) RuntimeState {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	state.OwnerPID = ownerPID
	state.OwnerInstanceID = ownerInstanceID
	state.OwnerLastActive = now
	if leaseDuration > 0 {
		state.OwnerLeaseUntil = now.Add(leaseDuration)
	} else {
		state.OwnerLeaseUntil = time.Time{}
	}
	return state
}

// RuntimeOwnerLeaseActive reports whether the current owner lease should still
// be treated as active. Older runtime.json files without explicit lease fields
// fall back to UpdatedAt plus the configured lease duration.
func RuntimeOwnerLeaseActive(state *RuntimeState, leaseDuration time.Duration, now time.Time) bool {
	if state == nil {
		return false
	}
	expiresAt := RuntimeOwnerLeaseExpiresAt(state, leaseDuration)
	if expiresAt.IsZero() {
		return true
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	return now.Before(expiresAt)
}

// RuntimeOwnerLeaseExpiresAt resolves the effective lease expiration time for
// both current and pre-lease runtime.json files.
func RuntimeOwnerLeaseExpiresAt(state *RuntimeState, leaseDuration time.Duration) time.Time {
	if state == nil {
		return time.Time{}
	}
	if !state.OwnerLeaseUntil.IsZero() {
		return state.OwnerLeaseUntil
	}
	if leaseDuration <= 0 {
		return time.Time{}
	}
	base := state.OwnerLastActive
	if base.IsZero() {
		base = state.UpdatedAt
	}
	if base.IsZero() {
		return time.Time{}
	}
	return base.Add(leaseDuration)
}

func marshalRuntimeState(state RuntimeState) ([]byte, error) {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal runtime state: %w", err)
	}
	return data, nil
}

// TransitionRuntimeState performs one read-decide-persist step under the
// per-game transition lock, bumping the CAS generation (design/06). The
// mutate callback sees the current claim; returning an error abandons the
// transition. The lock is never held while a hook runs or anything waits —
// callers do slow work outside and call this for the state write alone.
func TransitionRuntimeState(gameID, configDir string, lockTimeout time.Duration, mutate func(*RuntimeState) error) (*RuntimeState, error) {
	lock, err := AcquireTransitionLock(gameID, configDir, lockTimeout)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	state, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("%w for %s", ErrNoRuntimeClaim, gameID)
	}
	if err := mutate(state); err != nil {
		return nil, err
	}
	state.Generation++
	if err := SaveRuntimeState(gameID, configDir, *state); err != nil {
		return nil, err
	}
	return state, nil
}
