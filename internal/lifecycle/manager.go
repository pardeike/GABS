// Package lifecycle is the typed, frontend-agnostic game lifecycle manager
// shared by both GABS frontends (the MCP server and the CLI). It owns the
// Stage 1–4 start pipeline (claim gating, endpoint preparation, spawn, and
// Stage-4 verification/promotion) plus stop/kill/status over the persisted
// runtime claim — everything keyed by the config directory, so a one-shot CLI
// process and a long-lived server reach identical, cross-process-safe outcomes
// (design/05, design/06, design/11).
//
// Stage 5 (attaching a live GABP bridge and mirroring its tools) is inherently
// a server concern and is NOT part of this package: Manager.Start returns after
// Stage 4 with the live controller and endpoint so the server frontend can
// continue to Stage 5, while the CLI frontend intentionally stops there and
// reports started_attachment_deferred. The bridge-evidence predicate a server
// holds (its in-memory GABP client map) enters the pipeline only through the
// nil-safe BridgeBound callback; a CLI passes nil and the pipeline falls back
// to the authoritative persisted attachment-lease + owner-fingerprint evidence
// (design/04), which is exactly the correct cross-process verdict.
package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// Manager is the shared lifecycle service. It carries only the frontend-
// agnostic state the Stage 1–4 pipeline and its helpers need; the in-memory
// controller registry, GABP client map, and bridge-attachment records stay in
// the server frontend. A Manager value is cheap; frontends may build one per
// operation from their current fields.
type Manager struct {
	log           util.Logger
	configDir     string
	instanceID    string
	gamesConfig   *config.GamesConfig
	ownerLease    time.Duration
	starter       *process.SerializedStarter
	newController func() process.ControllerInterface
}

// NewManager builds a lifecycle Manager. gamesConfig is the startup-pinned
// configuration (never the hot-reload snapshot); ownerLease is the session
// owner-lease duration; starter serializes concurrent starts within one
// process; newController builds the process controller for a start (injectable
// so a test can supply deterministic liveness).
func NewManager(log util.Logger, configDir, instanceID string, gamesConfig *config.GamesConfig, ownerLease time.Duration, starter *process.SerializedStarter, newController func() process.ControllerInterface) *Manager {
	return &Manager{
		log:           log,
		configDir:     configDir,
		instanceID:    instanceID,
		gamesConfig:   gamesConfig,
		ownerLease:    ownerLease,
		starter:       starter,
		newController: newController,
	}
}

// instanceIDCounter guarantees uniqueness even when two IDs are minted within
// the same nanosecond — Windows' clock granularity is coarse, so pid+nano alone
// can collide within one process (as newServerInstanceID also guards against).
var instanceIDCounter uint64

// NewInstanceID mints a fresh fencing owner identity for a one-shot frontend (a
// CLI process): pid distinguishes processes and an atomic sequence guarantees
// per-process uniqueness regardless of clock resolution.
func NewInstanceID() string {
	seq := atomic.AddUint64(&instanceIDCounter, 1)
	return fmt.Sprintf("cli-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), seq)
}

// ConfigDir is the config directory this manager operates against.
func (m *Manager) ConfigDir() string { return m.configDir }

// InstanceID is this manager's fencing owner identity.
func (m *Manager) InstanceID() string { return m.instanceID }

func isURLMode(mode string) bool { return mode == "SteamAppId" || mode == "EpicAppId" }

// isSteamMode is true for both Steam launch modes — both get the store-launcher
// advisory scan (design/05, M2.15), though only SteamManaged runs assistance.
func isSteamMode(mode string) bool { return mode == "SteamAppId" || mode == "SteamManaged" }

// SteamNotRunningAdvisory is the single Stage-2 warning when the Steam client is
// not observable at start (design/05).
const SteamNotRunningAdvisory = "Steam does not appear to be running; the launch may stall at login/startup or the game may relaunch itself through Steam"

// steamAssistSpawnHeadroom is reserved out of the operation deadline for the
// spawn itself, so bounded Steam-client assistance cannot let the accepted
// operation expire before cmd.Start.
const steamAssistSpawnHeadroom = 2 * time.Second

// minStageFourBudget is the smallest remaining operation budget worth spawning
// against (M2.15): below it the deadline is effectively consumed, so the
// operation is treated as supersedable rather than spawning with a uselessly
// tiny — or, if negative, silently full-defaulted — Stage-4 budget.
const minStageFourBudget = 200 * time.Millisecond

// processStartBudget is the Stage 4 verification budget (design/05): URL modes
// get longer because the store launcher sits in the middle.
func processStartBudget(mode string) time.Duration {
	if isURLMode(mode) {
		return 60 * time.Second
	}
	return 10 * time.Second
}

// StartBudgetFor returns the single process-start budget used for BOTH the
// claim's pinned deadlines and the starter's verification wait — a claim
// deadline that outlives (or undercuts) the actual wait enables incorrect
// concurrent gating and supersession. Explicit config wins for all modes.
func (m *Manager) StartBudgetFor(mode string) time.Duration {
	if m.gamesConfig != nil && m.gamesConfig.Timeouts != nil &&
		m.gamesConfig.Timeouts.Startup != nil && m.gamesConfig.Timeouts.Startup.ProcessStartSeconds > 0 {
		return time.Duration(m.gamesConfig.Timeouts.Startup.ProcessStartSeconds) * time.Second
	}
	return processStartBudget(mode)
}

// RuntimeOwnerLeaseDuration is the configured session owner-lease duration, or
// the built-in default when none is set.
func (m *Manager) RuntimeOwnerLeaseDuration() time.Duration {
	if m.ownerLease > 0 {
		return m.ownerLease
	}
	return (&config.GamesConfig{}).GetSessionOwnerLease()
}

// RuntimeOwnerLeaseForOperation returns the owner-lease duration for a claim
// promoted by an operation whose GABP wait is operationTimeout: at least the
// session lease, extended to outlast a long verification wait.
func (m *Manager) RuntimeOwnerLeaseForOperation(operationTimeout time.Duration) time.Duration {
	lease := m.RuntimeOwnerLeaseDuration()
	if operationTimeout <= 0 {
		return lease
	}
	operationLease := operationTimeout + 5*time.Second
	if operationLease > lease {
		return operationLease
	}
	return lease
}

// HistoryContext holds the track-record coordinates for a launch (design/08,
// design/20): the input-free context hash, the last-good snapshot, the per-game
// success bucket identity, and the applied input names. Its fields are exported
// so the frontend presentation code can render attribution from the same value
// the pipeline records against.
type HistoryContext struct {
	LaunchID    string
	Profile     string
	ContextHash string
	Snapshot    process.ContextSnapshot
	Bucket      process.SuccessBucket
	InputNames  []string
}

// ComputeHistoryContext derives the input-free context coordinates and the
// bucket IDENTITY with ZERO history mutation (round 11 P1-1/P1-2): the hash,
// the last-good snapshot, and the per-input declaration identity. It performs
// NO key creation and NO bucket invalidation, so it is safe on a pre-accept
// failure path (launch_spec_unresolvable, resolver/call-class errors) where a
// caller typo must not persist a per-game bucket key or drop buckets. The
// value digest is left empty — it needs the per-game key and belongs only to
// the accepted-start path.
func (m *Manager) ComputeHistoryContext(snap *config.Snapshot, game config.GameConfig, resolved *launch.Resolved, inputs map[string]interface{}) HistoryContext {
	profile := ""
	var inputNames []string
	if resolved != nil {
		profile = resolved.Profile
		inputNames = append([]string(nil), resolved.AppliedInputs...)
	}
	hc := HistoryContext{Profile: profile, InputNames: inputNames}

	base, berr := launch.ResolveBaseContext(snap, game.ID, profile, launch.Options{
		InheritedEnv:       os.Environ(),
		CaseInsensitiveEnv: runtime.GOOS == "windows",
	})
	if berr == nil {
		hc.ContextHash = process.ContextHash(base)
		hc.Snapshot = process.ContextSnapshot{
			Target:         base.Target,
			Mode:           base.Mode,
			Args:           base.Args,
			ConfigEnv:      base.ConfigEnv,
			AbsentEnvNames: base.AbsentEnvNames,
			WorkingDir:     base.WorkingDir,
			Lifecycle:      base.Lifecycle,
		}
	}

	if len(inputNames) > 0 {
		hc.Bucket.InputNames = inputNames
		hc.Bucket.DeclHash = process.InputDeclHash(game, inputNames)
		hc.Bucket.PerInputDecl = perInputDeclHashes(game, inputNames)
		// Compute the value digest READ-ONLY when a bucket key already exists
		// (round 12 F8), so a pre-accept failure (launch_spec_unresolvable) on
		// a previously proven exact input combination is recognized as proven
		// rather than always "first run". If no key exists, the combination is
		// genuinely unproven and the digest stays empty — no key is minted.
		if key := process.BucketKeyIfExists(game.ID, m.configDir); key != "" {
			hc.Bucket.ValueDigest = process.BucketValueDigest(key, appliedInputValues(inputNames, inputs))
		}
	}
	return hc
}

// appliedInputValues maps supplied input names to their string values for the
// per-game-keyed value digest (values never persist in the clear).
func appliedInputValues(inputNames []string, inputs map[string]interface{}) map[string]string {
	applied := map[string]string{}
	for _, n := range inputNames {
		if v, ok := inputs[n]; ok {
			applied[n] = fmt.Sprintf("%v", v)
		}
	}
	return applied
}

// BuildHistoryContext is the ACCEPTED-START context: the pure coordinates plus
// the two mutations only a resolved, accepted attempt may perform — dropping
// buckets whose input declaration changed (reload safety) and creating/reading
// the per-game bucket key to compute this launch's value digest.
func (m *Manager) BuildHistoryContext(snap *config.Snapshot, game config.GameConfig, resolved *launch.Resolved, inputs map[string]interface{}) HistoryContext {
	hc := m.ComputeHistoryContext(snap, game, resolved, inputs)

	// Reload safety (design/08): drop any input-combination bucket whose
	// per-input declaration changed — or was REMOVED — since it was recorded;
	// a value that "worked" under an old declaration is not proof under the
	// new one. Runs even when every input was removed (empty current map), so
	// a re-added declaration never resurrects stale proof (round 11 P2-4).
	currentDecls := make(map[string]string, len(game.LaunchInputs))
	for name := range game.LaunchInputs {
		currentDecls[name] = process.InputDeclHash(game, []string{name})
	}
	if err := process.InvalidateChangedInputDeclarations(game.ID, m.configDir, hc.Profile, currentDecls); err != nil {
		m.log.Warnw("failed to invalidate changed input declarations", "gameId", game.ID, "error", err)
	}

	if len(hc.InputNames) > 0 {
		if key, err := process.EnsureBucketKey(game.ID, m.configDir); err == nil {
			hc.Bucket.ValueDigest = process.BucketValueDigest(key, appliedInputValues(hc.InputNames, inputs))
		}
	}
	return hc
}

func perInputDeclHashes(game config.GameConfig, names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		out[n] = process.InputDeclHash(game, []string{n})
	}
	return out
}

// ApplyPinnedWorkloadStart records this launch's Stage 4 verified start from
// the identity PINNED in the claim (round 11 P1-2), using an already-held
// runtime-state transition lock — so every promotion path (synchronous start,
// passive status observation, attachment, recovery) credits the start exactly
// once, atomically with the phase flip, from the claim alone. No-op for a
// claim without a pinned identity (legacy/external claims). The caller MUST
// invoke this only when the transition actually flips starting→active.
func (m *Manager) ApplyPinnedWorkloadStart(gameID string, st *process.RuntimeState) error {
	return process.ApplyPinnedWorkloadStartLocked(gameID, m.configDir, st, time.Now().UTC())
}

// RecordTerminalStartFailure writes a terminal accepted-attempt failure to
// history while the claim is still alive (called from Start before the deferred
// claim release), fenced to the launch (design/08, design/20; round 10).
// Returns the classification so the caller can carry it to the render step.
// Only design-eligible codes write; the pure classification is always returned.
func (m *Manager) RecordTerminalStartFailure(game config.GameConfig, hc HistoryContext, code string) process.Classification {
	cls := process.Classify(code, process.ClassifyContext{
		Proven:                m.ContextProven(game.ID, hc),
		InputCombinationFresh: !m.InputComboProven(game.ID, hc),
		SuppliedInputs:        hc.InputNames,
	})
	if writeEligibleStartFailure(code) && hc.ContextHash != "" && hc.LaunchID != "" {
		if err := process.RecordFailure(game.ID, m.configDir, hc.LaunchID, hc.Profile, hc.ContextHash, code, cls.Class, hc.InputNames, time.Now().UTC()); err != nil {
			m.log.Warnw("failed to record start failure in history", "gameId", game.ID, "error", err)
		}
	}
	return cls
}

// writeEligibleStartFailure reports whether a start-failure code is a terminal
// failure of an accepted attempt with a resolved context — the only failures
// that mutate history (design/08, design/20). Pre-accept codes (call-class,
// config_invalid, Stage 2 refusals) render but never write.
func writeEligibleStartFailure(code string) bool {
	switch code {
	case "exited_during_start", "spawn_failed", "endpoint_unavailable", "spec_too_large":
		return true
	default:
		return false
	}
}

// ContextProven reports whether the resolved context has a verified workload
// start recorded (design/08).
func (m *Manager) ContextProven(gameID string, hc HistoryContext) bool {
	h, err := process.LoadHistory(gameID, m.configDir)
	if err != nil {
		return false
	}
	e := h.Profiles[hc.Profile]
	return e != nil && e.ContextHash == hc.ContextHash && e.WorkloadStarts > 0
}

// InputComboProven reports whether this exact supplied-input combination has
// been proven on the resolved context (design/08).
func (m *Manager) InputComboProven(gameID string, hc HistoryContext) bool {
	h, err := process.LoadHistory(gameID, m.configDir)
	if err != nil {
		return false
	}
	e := h.Profiles[hc.Profile]
	if e == nil || e.ContextHash != hc.ContextHash {
		return false
	}
	return e.HasBucket(hc.Bucket.DeclHash, hc.Bucket.ValueDigest)
}

// computeSpawnDigests pins the expected launch context from the fully
// materialized spawn state (design/03): the argv payload is the resolved
// argument list (argv[0] excluded by construction), the cwd is the effective
// working directory, and the env values are exactly the names the wrapper
// contract forwards (GABS_FORWARD_ENV, falling back to the managed GABS_*/GABP_*
// variables for legacy specs).
func computeSpawnDigests(spec process.LaunchSpec, controller process.ControllerInterface) *process.RuntimeContextDigests {
	finalEnv := map[string]string{}
	for _, kv := range controller.FinalEnvironment() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			finalEnv[kv[:i]] = kv[i+1:]
		}
	}

	// Channel membership is decided HERE, from the resolved spec — the
	// config-declared context keys are the contextEnv channel; every other
	// forwarded name (GABS_*/GABP_*, SteamAppId/SteamGameId, SystemRoot) is the
	// managed layer (review round 9: prefix guessing is not a persistable
	// contract).
	contextKeys := map[string]bool{}
	for _, k := range spec.ContextEnvKeys {
		contextKeys[k] = true
	}
	managedEnv := map[string]string{}
	contextEnv := map[string]string{}
	classify := func(n, v string) {
		if contextKeys[n] {
			contextEnv[n] = v
		} else {
			managedEnv[n] = v
		}
	}
	if names := strings.TrimSpace(finalEnv["GABS_FORWARD_ENV"]); names != "" {
		for _, n := range strings.Split(names, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if v, ok := finalEnv[n]; ok {
				classify(n, v)
			}
		}
	} else {
		for k, v := range finalEnv {
			if strings.HasPrefix(k, "GABS_") || strings.HasPrefix(k, "GABP_") {
				classify(k, v)
			}
		}
	}

	var absent []string
	if names := strings.TrimSpace(finalEnv["GABS_ABSENT_ENV"]); names != "" {
		for _, n := range strings.Split(names, ",") {
			if n = strings.TrimSpace(n); n != "" {
				absent = append(absent, n)
			}
		}
	}

	cwd := spec.WorkingDir
	unverifiable := false
	switch {
	case cwd == "":
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		} else {
			unverifiable = true
		}
	case !filepath.IsAbs(cwd):
		// The legacy relative workingDir: incomparable by contract (design/03)
		// — unverifiable, never a guessed digest.
		unverifiable = true
	}

	digests, err := process.ComputeContextDigests(process.ArgvPayloadForDigest(spec.PathOrId, spec.Args), cwd, unverifiable, managedEnv, contextEnv, absent)
	if err != nil {
		return nil
	}
	return digests
}

// resolveRuntimeGamePID picks the workload PID to record for a launch: URL
// modes track a named process (the store launches the real game), every other
// mode tracks the controller's direct child.
func resolveRuntimeGamePID(game config.GameConfig, controller process.ControllerInterface) int {
	if controller == nil {
		return 0
	}
	if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
		if game.StopProcessName != "" {
			pids, err := process.FindProcessesByName(game.StopProcessName)
			if err == nil && len(pids) > 0 {
				return pids[0]
			}
		}
		return 0
	}
	return controller.GetPID()
}

// StampSpawnState applies a fenced claim mutation; a fencing violation means
// the claim was superseded mid-start and is only logged — the OS spawn itself
// is not abortable from here.
func (m *Manager) StampSpawnState(gameID, launchID, opID string, mutate func(*process.RuntimeState)) {
	if _, err := process.FencedTransition(gameID, m.configDir, launchID, opID, func(st *process.RuntimeState) error {
		mutate(st)
		return nil
	}); err != nil {
		m.log.Warnw("spawn-state transition not applied", "gameId", gameID, "error", err)
	}
}

// selfConnectionFrom adapts a BridgeBound callback to the removal guards'
// connection-scoped self-liveness signature. A nil BridgeBound (a CLI that
// holds no live bridge) yields a nil guard, and the persisted attachment lease
// governs cross-process liveness instead (design/04).
func selfConnectionFrom(bridgeBound func(launchID, connectionID string) bool, launchID string) func(string) bool {
	if bridgeBound == nil {
		return nil
	}
	return func(connectionID string) bool { return bridgeBound(launchID, connectionID) }
}
