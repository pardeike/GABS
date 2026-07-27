package process

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
)

// safeExactClaimParent resolves gameID's claim directory through exact
// on-disk path components. The config root itself may be symlinked, but a
// game ID must never address another game's directory through an in-root
// symlink or a case-insensitive spelling alias, so every component below the
// resolved root must be an exact-spelling, non-symlink directory. Traversing
// a symlinked component would target another location, so it is rejected even
// for removal.
func safeExactClaimParent(gameID, configDir string) (string, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return "", err
	}
	if _, err := cp.SafeRuntimeStatePath(gameID); err != nil {
		return "", err // unsafe ID: lexical or symlink-ancestor escape
	}
	current, err := filepath.EvalSymlinks(cp.GetBaseDir())
	if err != nil {
		return "", err
	}
	for _, component := range strings.Split(gameID, "/") {
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", err
		}
		exact := false
		for _, entry := range entries {
			if entry.Name() == component {
				exact = true
				break
			}
		}
		if !exact {
			// On a case-insensitive filesystem the component may exist under a
			// different spelling; an inexact spelling must not address another
			// ID's directory.
			return "", os.ErrNotExist
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("runtime claim path component %s is a symlink", current)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("runtime claim path component %s is not a directory", current)
		}
	}
	return current, nil
}

// errNonRegularClaim marks a runtime.json that exists but is not a regular
// file (a symlink or special file). Such a leaf is never read through; explicit
// repair may still unlink the exact entry.
var errNonRegularClaim = errors.New("runtime claim is not a regular file")

// regularClaimPath is safeExactClaimParent extended to the leaf: the claim
// file itself must be a non-symlink regular file before any read. Content is
// deliberately not inspected — a corrupt claim stays addressable so repair and
// runtime-only status can surface it.
func regularClaimPath(gameID, configDir string) (string, os.FileInfo, error) {
	parent, err := safeExactClaimParent(gameID, configDir)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(parent, "runtime.json")
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %s", errNonRegularClaim, path)
	}
	return path, info, nil
}

// errClaimReplaced marks a pathname whose file was swapped between the
// regularity gate and the open. The legitimate writer replaces the claim by
// rename on every publish, so callers treat it as a transient race and retry.
var errClaimReplaced = errors.New("runtime claim was replaced while opening")

// readRegularClaim reads the gated claim through an open handle proven (via
// SameFile) to be the exact regular file the gate inspected — a pathname
// swapped to a symlink after the gate is never read through — and tightens
// legacy permissions through that same handle, never the pathname.
func readRegularClaim(gameID, configDir string) ([]byte, error) {
	path, gateInfo, err := regularClaimPath(gameID, configDir)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	handleInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !handleInfo.Mode().IsRegular() || !os.SameFile(gateInfo, handleInfo) {
		return nil, fmt.Errorf("%w: %s", errClaimReplaced, path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if err := tightenLegacyClaimHandle(f, handleInfo); err != nil {
		return nil, err
	}
	return data, nil
}

// CheckRuntimeClaim reports whether gameID has an addressable regular
// runtime.json — true even for corrupt/unreadable content, so a
// removed-but-claimed game stays addressable by ID (design/07) — while
// preserving errors: the answer is authoritative only when err is nil. An
// unsafe ID and a cleanly absent path are authoritatively absent. Anything
// else — an unreadable directory, a symlinked component, a non-regular leaf,
// a replacement race that outlives the bounded retry — returns an error,
// because a caller whose fallback would address a DIFFERENT referent must
// never take that fallback when absence could not be established.
func CheckRuntimeClaim(gameID, configDir string) (bool, error) {
	for attempt := 0; ; attempt++ {
		_, _, err := regularClaimPath(gameID, configDir)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if config.ValidateGameID(gameID) != nil {
			return false, nil // an unsafe ID can never address a claim
		}
		if isTransientClaimReadError(err) && attempt < maxClaimReadAttempts {
			time.Sleep(claimReadRetryDelay)
			continue
		}
		return false, err
	}
}

// RuntimeClaimExists is CheckRuntimeClaim for callers that need only the
// positive answer; every failure reads as "not addressable".
func RuntimeClaimExists(gameID, configDir string) bool {
	exists, err := CheckRuntimeClaim(gameID, configDir)
	return err == nil && exists
}

// ListRuntimeClaimIDs returns, sorted, every game ID that has a persisted
// runtime.json under configDir — the runtime surface that no-argument
// games_status unions with the configured entries (design/07): a game removed
// from config but still holding a claim must remain discoverable. The scan
// never follows symlinks and includes only entries the regular-claim resolver
// would address, so symlinked leaves and malformed storage aliases are
// ignored while a corrupt-but-regular claim still surfaces (its status
// resolves to unknown, with the repair path). An unreadable individual entry
// is skipped, never failing the whole scan; only a failure to scan the base
// directory itself is returned.
func ListRuntimeClaimIDs(configDir string) ([]string, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, err
	}
	// The config root itself may be a symlink; resolve it once so WalkDir
	// (which never descends symlinked directories) still walks the real tree.
	base, err := filepath.EvalSymlinks(cp.GetBaseDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	// A nested `/` game ID maps to a nested directory, so discovery walks the
	// tree for runtime.json at any depth and decodes the storage key — the
	// runtime.json's parent directory relative to base, normalized to `/` — back
	// to the exact game ID. A single unreadable subtree is skipped, never failing
	// the whole scan (the corrupt-claim repair path).
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// The ROOT itself failing (an unreadable config base) is a real scan
			// failure, not a skippable corrupt subtree: returning SkipDir here
			// would report a falsely-complete empty scan. Surface it; a
			// non-existent base is still degraded to "no claims" below.
			if filepath.Clean(path) == filepath.Clean(base) {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Name() != "runtime.json" {
			return nil
		}
		rel, relErr := filepath.Rel(base, filepath.Dir(path))
		if relErr != nil || rel == "." {
			return nil
		}
		id := filepath.ToSlash(rel)
		// Only what the resolver would address counts as a claim: this drops a
		// symlink merely named runtime.json and any storage path that is not a
		// valid game ID.
		if _, _, resolveErr := regularClaimPath(id, configDir); resolveErr != nil {
			return nil
		}
		ids = append(ids, id)
		return nil
	})
	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			return nil, nil
		}
		return nil, walkErr
	}
	sort.Strings(ids)
	return ids, nil
}

// Bounded retry for a claim read racing a concurrent write/remove — the write
// window is one tmp+rename, so a few dozen short retries (~200ms) cover it.
const (
	maxClaimReadAttempts = 40
	claimReadRetryDelay  = 5 * time.Millisecond
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
	// recomputed from hot-reloaded config.
	HistoryContextHash string `json:"historyContextHash,omitempty"`

	// HistorySuccess pins the snapshot + input-bucket identity needed to
	// record this launch's Stage 4 verified start from the claim alone, so a
	// promotion that happens asynchronously (passive status, attachment,
	// recovery) or after a crash-and-restart still credits the start with the
	// right lastGood and bucket. Pinned at publication.
	HistorySuccess *HistorySuccessIdentity `json:"historySuccess,omitempty"`

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
	// PendingCleanStops and PendingDeliveries are verified history events whose
	// cleanStops/deliveriesVerified credit has not yet landed.
	// Each entry is SELF-CONTAINED — it carries the event's own immutable
	// identity and history coordinates — so reconciliation credits the exact
	// event as a pure function of the entry, independent of the claim's current
	// Operation or Attachment (both of which are replaced/cleared by ordinary
	// lifecycle). Every claim deleter reconciles BOTH lists before removal and
	// aborts if any credit write fails, so no verified event is ever lost with
	// the claim or misassociated with a successor's operation/connection.
	PendingCleanStops []PendingCredit `json:"pendingCleanStops,omitempty"`
	PendingDeliveries []PendingCredit `json:"pendingDeliveries,omitempty"`
}

// PendingCredit is a self-contained record of a verified history event awaiting
// its idempotent credit. ID is the operationID (clean stop) or
// connectionID (verified delivery); Profile and ContextHash pin which history
// entry it credits, captured when the event was observed — never re-read from
// the mutable claim at reconciliation time.
type PendingCredit struct {
	ID          string    `json:"id"`
	Profile     string    `json:"profile"`
	ContextHash string    `json:"contextHash"`
	At          time.Time `json:"at,omitempty"`
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
	if err := config.ValidateGameID(gameID); err != nil {
		return err // never create a game dir for a traversal ID
	}
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
// saveRuntimeStateFailHook forces SaveRuntimeState to fail — test-only, to
// exercise the record-first ordering (a runtime-save failure after
// a credit must replay to exactly-one via the idempotent credit, never losing
// or double-counting the event).
var saveRuntimeStateFailHook func() error

// SetSaveRuntimeStateFailHookForTesting installs a hook whose non-nil error
// makes the next SaveRuntimeState calls fail. Returns a restore func.
func SetSaveRuntimeStateFailHookForTesting(fn func() error) func() {
	prev := saveRuntimeStateFailHook
	saveRuntimeStateFailHook = fn
	return func() { saveRuntimeStateFailHook = prev }
}

func SaveRuntimeState(gameID, configDir string, state RuntimeState) error {
	if saveRuntimeStateFailHook != nil {
		if err := saveRuntimeStateFailHook(); err != nil {
			return err
		}
	}
	if err := config.ValidateGameID(gameID); err != nil {
		return err // never create a game dir for a traversal ID
	}
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

// LoadRuntimeState reads the shared runtime state file if present. The claim
// is addressed through exact on-disk components and must be a regular file: a
// symlink is never read through, so an unsafe path or non-regular leaf returns
// an error and the claim's status resolves to unknown with the repair path.
func LoadRuntimeState(gameID, configDir string) (*RuntimeState, error) {
	data, err := readClaimBytesRetry(gameID, configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read runtime state: %w", err)
	}

	state, perr := parseRuntimeState(data)
	if perr != nil {
		// A torn or empty claim can only be the link-less fallback mid-
		// publication (design/05: readers never act on a partial claim).
		// The transition lock brackets that window: take it and re-read —
		// through a fresh gate and handle, so a pathname swapped to a symlink
		// while this reader waited on the lock is never read through.
		lock, lerr := AcquireTransitionLock(gameID, configDir, 2*time.Second)
		if lerr != nil {
			return nil, fmt.Errorf("failed to parse runtime state (and could not take the transition lock to re-read): %w", perr)
		}
		state, rerr := loadRuntimeStateLocked(gameID, configDir)
		lock.Release()
		return state, rerr
	}
	return state, nil
}

// loadRuntimeStateLocked is LoadRuntimeState for a caller that already holds
// the game's transition lock: the torn-claim recovery needs no re-lock — the
// lock holder excludes the link-less publication window a torn read could
// race — and re-acquiring would only stall on the caller's own lock before
// failing. A claim that still fails to parse under the lock is genuinely
// corrupt.
func loadRuntimeStateLocked(gameID, configDir string) (*RuntimeState, error) {
	data, err := readClaimBytesRetry(gameID, configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read runtime state: %w", err)
	}
	state, perr := parseRuntimeState(data)
	if perr != nil {
		return nil, fmt.Errorf("failed to parse runtime state: %w", perr)
	}
	return state, nil
}

// readClaimBytesRetry reads the gated claim under a bounded retry: on Windows
// a concurrent writer's rename/replace briefly holds runtime.json without
// read-sharing (a sharing violation), and on every OS a legitimate publish
// swaps the inode between the gate and the open (errClaimReplaced). Both are
// transient — the write window is a single tmp+rename — so retry rather than
// surface a spurious hard error. A removed file surfaces as os.ErrNotExist; a
// non-regular leaf and a legitimate loose-permission file that cannot be
// tightened still surface as errors (the token must not stay world-readable).
func readClaimBytesRetry(gameID, configDir string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		data, rerr := readRegularClaim(gameID, configDir)
		if rerr != nil {
			if (errors.Is(rerr, errClaimReplaced) || isTransientClaimReadError(rerr)) && !errors.Is(rerr, os.ErrNotExist) && attempt < maxClaimReadAttempts {
				time.Sleep(claimReadRetryDelay)
				continue
			}
			return nil, rerr
		}
		return data, nil
	}
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
// removeRuntimeStateFailHook forces RemoveRuntimeState to fail — test-only, to
// exercise the record-first clean-stop ordering (a claim-removal
// failure after the cleanStop credit must replay to exactly-one, never losing
// or double-counting the stop event).
var removeRuntimeStateFailHook func() error

// SetRemoveRuntimeStateFailHookForTesting installs a hook whose non-nil error
// makes the next RemoveRuntimeState calls fail. Returns a restore func.
func SetRemoveRuntimeStateFailHookForTesting(fn func() error) func() {
	prev := removeRuntimeStateFailHook
	removeRuntimeStateFailHook = fn
	return func() { removeRuntimeStateFailHook = prev }
}

func RemoveRuntimeState(gameID, configDir string) error {
	if removeRuntimeStateFailHook != nil {
		if err := removeRuntimeStateFailHook(); err != nil {
			return err
		}
	}
	parent, err := safeExactClaimParent(gameID, configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // no addressable claim directory: nothing to remove
		}
		return err
	}
	// The leaf is unlinked exactly, never followed: repair must be able to
	// remove a malformed (symlinked or special) claim without touching its
	// target, so leaf regularity is deliberately not required here. Symlinked
	// intermediate directories were still rejected above — unlinking through
	// one would target another location.
	if err := os.Remove(filepath.Join(parent, "runtime.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
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
		pids, err := callFindProcessesByName(state.StopProcessName)
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
	return TransitionRuntimeStateWithCredit(gameID, configDir, lockTimeout, mutate, nil)
}

// TransitionRuntimeStateWithCredit is TransitionRuntimeState plus a credit hook
// that runs UNDER THE LOCK, BEFORE runtime.json is saved, and can ABORT the
// transition by returning an error. The history counters belong
// here — recorded FIRST (idempotent by event ID), so the runtime commit is
// gated on the credit committing:
//   - a history-write failure aborts the runtime save; the caller retries and
//     the idempotent credit counts at most once — no event is ever lost. (An
//     earlier afterCommit ordering ran the credit AFTER the save and
//     could permanently drop an event whose history write failed once the
//     runtime commit had already consumed the trigger.)
//   - a runtime-save failure after a successful credit replays the same way:
//     the credit no-ops by event ID, the runtime save is retried.
//   - a crash between the two writes replays: the runtime state is unchanged,
//     so the operation re-runs and the credit no-ops.
//
// A corrupt/unreadable history degrades to an empty track record inside
// LoadHistory (design/30) and still credits+repairs, so the degradation rule
// never blocks the lifecycle. Still fenced by the same lock, so a stale caller
// can never touch a successor.
func TransitionRuntimeStateWithCredit(gameID, configDir string, lockTimeout time.Duration, mutate func(*RuntimeState) error, credit func(*RuntimeState) error) (*RuntimeState, error) {
	lock, err := AcquireTransitionLock(gameID, configDir, lockTimeout)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	// The locked loader: this function already holds the transition lock, so
	// LoadRuntimeState's corrupt-claim recovery would stall on our own lock.
	state, err := loadRuntimeStateLocked(gameID, configDir)
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
	if credit != nil {
		if err := credit(state); err != nil {
			return nil, err
		}
	}
	if err := SaveRuntimeState(gameID, configDir, *state); err != nil {
		return nil, err
	}
	return state, nil
}
