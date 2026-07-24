package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/steam"
)

// StartRequest is the input to a Stage 1–4 start. The frontend supplies the
// already-built, runtime-dir-stamped launch spec and the pre-computed history
// context, plus two nil-safe policy callbacks:
//
//   - BridgeBound reports whether the caller holds a live authenticated bridge
//     to a launch. A server passes its in-memory GABP predicate; a one-shot CLI
//     passes nil and the pipeline falls back to the persisted attachment lease.
//   - CheckInProcessActive reports whether the caller's in-memory registry
//     already tracks a running controller for this game (the same-process fast
//     path). A server passes its games-map check; a CLI passes nil.
type StartRequest struct {
	Game                 config.GameConfig
	LaunchSpec           process.LaunchSpec
	Resolved             *launch.Resolved
	ResetEndpoint        bool
	StartupGABPTimeout   time.Duration
	HistoryContext       HistoryContext
	BridgeBound          func(launchID, connectionID string) bool
	CheckInProcessActive func() (status string, active bool)
}

// StartResult is a verified Stage 1–4 start. The workload is running and the
// claim is phase active with workloadStarts credited; the caller decides what
// to do next (a server continues to Stage 5 attach, a CLI reports
// started_attachment_deferred and exits). Controller is the live process
// handle; Port/Token are the per-launch bridge endpoint; RuntimeState is the
// promoted claim; Result carries the process-start verdict and warnings.
type StartResult struct {
	Controller       process.ControllerInterface
	Port             int
	Token            string
	RuntimeState     process.RuntimeState
	Result           *process.ProcessStartResult
	Warnings         []string
	TotalGABPTimeout time.Duration
}

// AssessWorkload evaluates the pinned liveness rule for a game from its
// persisted claim plus the given status hook (design/05) — the server frontend
// uses it in Stage 5 to recheck a workload that looked dead while the bridge
// was still connecting.
func (m *Manager) AssessWorkload(gameID, profile string, statusHook *launch.ResolvedHook) process.LivenessEvidence {
	claim, _ := process.LoadRuntimeState(gameID, m.configDir)
	return process.EvaluateLiveness(process.LivenessInput{
		CallerInstanceID: m.instanceID,
		Claim:            claim,
		StatusHook:       statusHook,
		GameID:           gameID,
		Profile:          profile,
	})
}

// Start runs Stages 1–4 (design/05): configure the controller, gate the claim
// by the liveness rule with a complete pre-spawn claim, run the store-launcher
// advisory, prepare the bridge endpoint, spawn under fenced spawn-state
// transitions, and verify+promote the workload to active with an exactly-once
// workloadStarts credit. It returns after the promote — Stage 5 (bridge attach)
// is the caller's. On failure it returns a typed error (StartRefusalError,
// UnobservedStartError, ExitedDuringStartError, EndpointUnavailableError,
// GameAlreadyActiveError, *launch.SpecSizeIssue, *process.ProcessError, or a
// wrapped internal error) and releases its own fenced claim.
func (m *Manager) Start(req StartRequest) (*StartResult, error) {
	game := req.Game
	launchSpec := req.LaunchSpec
	resolved := req.Resolved
	resetEndpoint := req.ResetEndpoint
	startupGABPTimeout := req.StartupGABPTimeout
	hc := req.HistoryContext

	controller := m.newController()
	if err := controller.Configure(launchSpec); err != nil {
		return nil, fmt.Errorf("failed to configure game launcher for '%s' (mode: %s, target: %s): %w",
			game.ID, game.LaunchMode, game.Target, err)
	}

	// Stage 2 (design/05): claim gating by the liveness rule, complete
	// pre-spawn claim with operation stamping, then all-profile probing +
	// stopProcessName as the lost-claim backstop.
	startBudget := m.StartBudgetFor(game.LaunchMode)
	gateRes, err := process.GateStart(process.StartGate{
		GameID:             game.ID,
		ConfigDir:          m.configDir,
		InstanceID:         m.instanceID,
		RequestedProfile:   launchSpec.Profile,
		BridgeBound:        req.BridgeBound,
		Spec:               launchSpec,
		Budget:             startBudget,
		Probes:             launch.ResolveProfileLifecycles(&game),
		StopProcessName:    game.StopProcessName,
		HistoryContextHash: hc.ContextHash,
		HistorySuccess:     &process.HistorySuccessIdentity{Snapshot: hc.Snapshot, Bucket: hc.Bucket},
	})
	if err != nil {
		return nil, err
	}
	if gateRes.Refusal != nil {
		return nil, &StartRefusalError{Refusal: gateRes.Refusal, Warnings: gateRes.Warnings}
	}
	runtimeState := *gateRes.Claim
	launchID := runtimeState.LaunchID
	opID := runtimeState.Operation.OperationID
	startWarnings := gateRes.Warnings
	hc.LaunchID = launchID

	// Stage 2 store-launcher advisory (design/05, M2.15): for Steam modes, scan
	// once; if the Steam client is not observable, record the single advisory
	// warning — and for SteamManaged only, run bounded best-effort assistance
	// charged against THIS operation's persisted deadline so it cannot expire
	// before spawn (the single-budget rule).
	if isSteamMode(game.LaunchMode) && !steam.ClientRunning() {
		startWarnings = append(startWarnings, SteamNotRunningAdvisory)
		if game.LaunchMode == "SteamManaged" {
			reserve := config.BridgeLockTimeout() + steamAssistSpawnHeadroom
			if budget := time.Until(runtimeState.Operation.Deadline) - reserve; budget > 0 {
				if err := steam.EnsureClientRunningWithin(budget); err != nil {
					m.log.Debugw("steam client assistance did not complete within budget", "gameId", game.ID, "error", err)
				}
			}
		}
	}

	cleanupRuntimeState := true
	// Terminal accepted-attempt failures must be written to history while the
	// claim is still alive and fenced to THIS launch (round 10).
	failureRecorded := false
	var pendingFailCode string
	recordFail := func(code string) {
		if failureRecorded || code == "" {
			return
		}
		failureRecorded = true
		m.RecordTerminalStartFailure(game, hc, code)
	}
	defer func() {
		if !cleanupRuntimeState {
			return
		}
		recordFail(pendingFailCode)
		// Release only OUR claim: fenced by the launch + operation identity
		// this start created — a cleanup that lost a race must never delete a
		// successor claim by bare game ID (design/06).
		if err := process.ReleaseStartClaim(game.ID, m.configDir, m.instanceID, launchID, opID, selfConnectionFrom(req.BridgeBound, launchID)); err != nil &&
			!errors.Is(err, process.ErrFencingViolation) && !errors.Is(err, process.ErrNoRuntimeClaim) {
			m.log.Warnw("failed to release start claim", "gameId", game.ID, "error", err)
		}
	}()

	// In-process fast path: a server whose registry already tracks a running
	// controller refuses (a one-shot CLI passes nil and skips this).
	if req.CheckInProcessActive != nil {
		if status, active := req.CheckInProcessActive(); active {
			return nil, &GameAlreadyActiveError{Status: status}
		}
	}

	// portRanges is startup-only (design/09): endpoint allocation uses the
	// configuration pinned at process start, never the hot-reload snapshot.
	port, token, bridgePath, reusedBridge, err := config.PrepareBridgeEndpointForStart(game.ID, m.configDir, m.gamesConfig, resetEndpoint)
	if err != nil {
		pendingFailCode = "endpoint_unavailable"
		return nil, &EndpointUnavailableError{GameID: game.ID, Err: err}
	}

	if reusedBridge {
		m.log.Infow("reusing GABS endpoint cache", "gameId", game.ID, "port", port, "host", "127.0.0.1", "configPath", bridgePath)
	} else {
		m.log.Infow("created GABS endpoint cache", "gameId", game.ID, "port", port, "host", "127.0.0.1", "configPath", bridgePath, "resetEndpoint", resetEndpoint)
	}

	controller.SetBridgeInfo(port, token)

	// The claim carries the endpoint (port + per-launch token) and the salted
	// expected-context digests, pinned in one transition so a delayed welcome
	// report verifies against what was actually spawned (design/03, design/07).
	spawnDigests := computeSpawnDigests(launchSpec, controller)
	if _, err := process.FencedTransition(game.ID, m.configDir, launchID, opID, func(st *process.RuntimeState) error {
		st.Endpoint = &process.RuntimeEndpoint{Port: port, Token: token}
		st.ContextDigests = spawnDigests
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to persist endpoint into runtime claim for '%s': %w", game.ID, err)
	}

	// Pre-spawn size check on the fully materialized spec: a structured
	// spec_too_large beats an opaque E2BIG/CreateProcess failure (design/03).
	if resolved != nil {
		finalEnv := map[string]string{}
		for _, kv := range controller.FinalEnvironment() {
			if i := strings.IndexByte(kv, '='); i > 0 {
				finalEnv[kv[:i]] = kv[i+1:]
			}
		}
		finalArgv := append([]string{launchSpec.PathOrId}, launchSpec.Args...)
		if iss := launch.CheckProcessSize(finalArgv, finalEnv); iss != nil {
			pendingFailCode = "spec_too_large"
			return nil, iss
		}
	}

	// A diagnostic-stamp write failure surfaced from the spawn observer (F10).
	var stampMu sync.Mutex
	var stampWarnings []string

	// Stage 3: spawnState transitions bracket OS process creation.
	controller.SetSpawnObservers(
		func() error {
			_, terr := process.FencedTransition(game.ID, m.configDir, launchID, opID, func(st *process.RuntimeState) error {
				// The persisted operation deadline is authoritative (M2.15):
				// checking "operation still live?" and marking spawning are ONE
				// atomic step under the transition lock. Once the deadline has
				// passed the operation is supersedable — refuse the spawn.
				if st.Operation == nil || !time.Now().Before(st.Operation.Deadline) {
					return process.ErrFencingViolation
				}
				st.SpawnState = process.SpawnStateSpawning
				return nil
			})
			return terr
		},
		func(pid int, startTime int64, spawnErr error) {
			m.StampSpawnState(game.ID, launchID, opID, func(st *process.RuntimeState) {
				if spawnErr != nil {
					st.SpawnState = process.SpawnStateFailed
					return
				}
				st.SpawnState = process.SpawnStateSpawned
				st.GamePID = pid
				st.PIDStartTime = startTime
			})
			// Stamp the diagnostic-only bridge.json fields at the spawn boundary
			// (design/20), ONLY on a successful spawn and FENCED to this launch's
			// endpoint so a superseded launch never writes onto the successor's
			// rotated token (round 12 F10).
			if spawnErr == nil {
				err := config.StampBridgeDiagnostics(game.ID, m.configDir, port, token, config.BridgeDiagnostics{
					Profile:        launchSpec.Profile,
					ConfigRevision: launchSpec.ConfigRevision,
					StartedAt:      time.Now().UTC().Format(time.RFC3339),
				})
				switch {
				case err == nil:
				case errors.Is(err, config.ErrBridgeEndpointRotated):
					m.log.Debugw("skipped bridge diagnostics stamp on rotated endpoint", "gameId", game.ID)
				default:
					stampMu.Lock()
					stampWarnings = append(stampWarnings, fmt.Sprintf("bridge diagnostics could not be written for this launch: %v", err))
					stampMu.Unlock()
					m.log.Warnw("failed to stamp bridge diagnostics", "gameId", game.ID, "error", err)
				}
			}
		})

	// Charge Stage 4 the REMAINING operation budget, not a fresh full duration
	// (M2.15). If the deadline is already (near) consumed, do not spawn at all.
	remaining := time.Until(runtimeState.Operation.Deadline)
	if remaining < minStageFourBudget {
		return nil, &process.ProcessError{
			Type:    process.ProcessErrorTypeStart,
			Context: fmt.Sprintf("the start budget for %s was consumed before spawn; the operation is now supersedable", game.ID),
			Err:     process.ErrFencingViolation,
		}
	}
	result := m.starter.StartWithVerificationWithTimeouts(controller, nil, game.ID, port, token, remaining, 0)
	stampMu.Lock()
	startWarnings = append(startWarnings, stampWarnings...)
	stampMu.Unlock()
	if result != nil {
		result.StartWarnings = startWarnings
	}

	// assessWorkload runs Stage 4 over the unified, pinned liveness sources.
	assessWorkload := func() process.LivenessEvidence {
		claim, _ := process.LoadRuntimeState(game.ID, m.configDir)
		if claim == nil {
			claim = &runtimeState
		}
		var hook *launch.ResolvedHook
		if launchSpec.Lifecycle != nil {
			hook = launchSpec.Lifecycle.Status
		}
		return process.EvaluateLiveness(process.LivenessInput{
			CallerInstanceID: m.instanceID,
			Claim:            claim,
			StatusHook:       hook,
			GameID:           game.ID,
			Profile:          launchSpec.Profile,
		})
	}
	// keepClaimUnobserved finalizes the Stage 4 unobserved outcome: the claim is
	// KEPT in phase starting; a later observation promotes it (design/05).
	keepClaimUnobserved := func() error {
		cleanupRuntimeState = false
		unobservedClass := process.Classify("unobserved", process.ClassifyContext{Proven: m.ContextProven(game.ID, hc)}).Class
		if _, ferr := process.FencedTransition(game.ID, m.configDir, launchID, opID, func(st *process.RuntimeState) error {
			st.Operation = nil // the attempt is over; the deadline governs reclaim
			if st.HistoryContextHash != "" {
				process.ApplyActionFailureLocked(game.ID, m.configDir, process.EffectiveClaimProfile(st), st.HistoryContextHash, "unobserved", unobservedClass, hc.InputNames, time.Now().UTC())
			}
			return nil
		}); ferr != nil {
			if errors.Is(ferr, process.ErrFencingViolation) || errors.Is(ferr, process.ErrNoRuntimeClaim) {
				return m.SupersededStartRefusal(game.ID)
			}
			return occupiedClaimRefusal(game.ID, "the unobserved outcome could not be persisted", ferr)
		}
		return &UnobservedStartError{Warnings: startWarnings}
	}
	exitedFailure := func(ev *process.LivenessEvidence) error {
		hookStopped := ev != nil && ev.Source == process.LivenessSourceStatusHook
		recordFail("exited_during_start")
		e := &ExitedDuringStartError{
			ExitCode:            controller.ExitCode(),
			Tail:                controller.LaunchLogTail(16 * 1024),
			Warnings:            startWarnings,
			HookReportedStopped: hookStopped,
		}
		if hookStopped {
			e.HookEvidence = ev.Detail
		}
		return e
	}

	if result.Error != nil {
		var procErr *process.ProcessError
		if errors.As(result.Error, &procErr) && procErr.Type == process.ProcessErrorTypeUnobserved {
			ev := assessWorkload()
			switch ev.Verdict {
			case process.StatusRunning:
				result.Error = nil
				result.ProcessStarted = true
				result.GameStillRunning = true
			case process.StatusStopped:
				if ev.Source == process.LivenessSourceStatusHook {
					return nil, exitedFailure(&ev) // stopped-by-hook after spawn
				}
				return nil, keepClaimUnobserved() // absence, not positive evidence
			default:
				return nil, keepClaimUnobserved()
			}
		} else if errors.Is(result.Error, process.ErrFencingViolation) {
			cleanupRuntimeState = false
			return nil, m.SupersededStartRefusal(game.ID)
		} else {
			// Record spawn_failed ONLY when the handler will also render it as
			// spawn_failed (round 10): a Start/Configuration ProcessError.
			if procErr != nil && (procErr.Type == process.ProcessErrorTypeStart || procErr.Type == process.ProcessErrorTypeConfiguration) {
				pendingFailCode = "spawn_failed"
			}
			return nil, fmt.Errorf("failed to start game '%s' (mode: %s, target: %s): %w",
				game.ID, game.LaunchMode, game.Target, result.Error)
		}
	}
	if !result.GameStillRunning {
		ev := assessWorkload()
		switch ev.Verdict {
		case process.StatusRunning:
			result.GameStillRunning = true // adopted: workload observed
		case process.StatusUnknown:
			return nil, keepClaimUnobserved() // nothing observable is not an exit
		default:
			return nil, exitedFailure(&ev)
		}
	}

	// Adoption (design/05 Stage 4): the direct child exited while the workload
	// stays observable — wrappers cross exactly the boundary where injected
	// args/env can be lost.
	adopted := controller.DirectChildExited()
	result.Adopted = adopted

	_, defaultGABPTimeout := m.starter.GetTimeouts()
	totalGABPTimeout := startupGABPTimeout
	if totalGABPTimeout <= 0 {
		totalGABPTimeout = defaultGABPTimeout
	}
	newPID := resolveRuntimeGamePID(game, controller)
	wasStarting := false
	promoted, err := process.FencedTransitionWithCredit(game.ID, m.configDir, launchID, opID, func(st *process.RuntimeState) error {
		wasStarting = st.Phase == process.PhaseStarting
		st.Status = process.RuntimeStateStatusRunning
		st.Phase = process.PhaseActive
		st.GamePID = newPID
		if fp, ferr := process.ProcessStartTime(newPID); ferr == nil {
			st.PIDStartTime = fp
		}
		st.Adopted = adopted
		st.Operation = nil
		st.ProcessStartDeadline = time.Time{}
		*st = process.RefreshRuntimeOwnerLease(*st, os.Getpid(), m.instanceID, m.RuntimeOwnerLeaseForOperation(totalGABPTimeout), time.Now().UTC())
		return nil
	}, func(st *process.RuntimeState) error {
		// Stage 4 verified: credit workloadStarts++ BEFORE the flip commits
		// (round 11 P1-2; round 14 F5), only when this transition actually
		// promoted a starting claim, so the four promotion paths record once.
		if wasStarting {
			return m.ApplyPinnedWorkloadStart(game.ID, st)
		}
		return nil
	})
	if err != nil {
		cleanupRuntimeState = false
		if errors.Is(err, process.ErrFencingViolation) || errors.Is(err, process.ErrNoRuntimeClaim) {
			return nil, m.SupersededStartRefusal(game.ID)
		}
		return nil, occupiedClaimRefusal(game.ID, "the running state could not be persisted", err)
	} else if promoted != nil {
		runtimeState = *promoted
	}
	cleanupRuntimeState = false

	return &StartResult{
		Controller:       controller,
		Port:             port,
		Token:            token,
		RuntimeState:     runtimeState,
		Result:           result,
		Warnings:         startWarnings,
		TotalGABPTimeout: totalGABPTimeout,
	}, nil
}
