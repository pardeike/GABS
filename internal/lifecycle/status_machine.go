package lifecycle

import (
	"errors"
	"time"

	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
)

// boundGABPForClaimHelper derives the GABP-evidence predicate for one claim from
// a nil-safe BridgeBound callback: the binding must match the claim's launch AND
// its persisted attachment connection. A CLI passes nil and this is always false
// (the persisted attachment lease governs instead, design/04).
func boundGABPForClaimHelper(bridgeBound func(launchID, connectionID string) bool, claim *process.RuntimeState) bool {
	if bridgeBound == nil || claim == nil || claim.Attachment == nil {
		return false
	}
	return bridgeBound(claim.LaunchID, claim.Attachment.ConnectionID)
}

// ObserveClaimStatus is the shared claim-status state machine (design/04, /05,
// /06, /07) both frontends drive: restart/dead-operation recovery, passive
// starting->active promotion with an exactly-once workload-start credit, pending
// delivery/clean-stop reconciliation, the completed-unobserved asymmetry, and
// the fenced stopped-claim removal with supersession retry. initialGABPLive is
// the caller's own live-bridge signal for the FIRST pass (a server computes it
// from its in-memory attachment; a CLI passes false); bridgeBound re-derives it
// for a claim reloaded after a lost fence (a CLI passes nil). When a fenced
// write loses to a successor it reloads and re-runs the full evaluation — phase
// is never mapped to status.
func (m *Manager) ObserveClaimStatus(gameID string, claim *process.RuntimeState, initialGABPLive bool, bridgeBound func(launchID, connectionID string) bool) (string, *process.LivenessEvidence) {
	gabpLive := initialGABPLive
	for attempt := 0; attempt < 4; attempt++ {
		status, ev, superseded := m.evaluateClaimStatusOnce(gameID, claim, gabpLive, bridgeBound)
		if !superseded {
			return status, ev
		}
		cur, lerr := process.LoadRuntimeState(gameID, m.configDir)
		if lerr != nil {
			return "unknown", nil
		}
		if cur == nil {
			return "stale-runtime-cleaned", ev
		}
		if cur.SchemaVersion < process.RuntimeSchemaVersion {
			// A legacy successor: the caller's legacy path owns it.
			return "", nil
		}
		// Re-derive the bridge binding for the freshly loaded claim.
		claim = cur
		gabpLive = boundGABPForClaimHelper(bridgeBound, cur)
	}
	// Convergence guard: repeated supersession means the claim is churning;
	// report unknown rather than loop.
	return "unknown", nil
}

// evaluateClaimStatusOnce runs one claim-first evaluation. superseded=true means
// a fenced write lost to a successor and the caller must reload and re-evaluate.
func (m *Manager) evaluateClaimStatusOnce(gameID string, claim *process.RuntimeState, gabpLive bool, bridgeBound func(launchID, connectionID string) bool) (string, *process.LivenessEvidence, bool) {
	var hook *launch.ResolvedHook
	if claim.Lifecycle != nil {
		hook = claim.Lifecycle.Status
	}
	profile := claim.Profile
	if claim.Source == process.SourceExternal && claim.ObservedProfile != "" && claim.ObservedProfile != process.ObservedProfileUnknown {
		profile = claim.ObservedProfile
	}
	now := time.Now().UTC()

	// Restart recovery is lazy and liveness-driven (design/07): a dead bounded
	// attempt — executor provably gone or deadline expired — is normalized on
	// this first observation. A dead attempt never renders as in progress, and
	// the interrupted hook is never replayed.
	if claim.Operation != nil && !process.OperationInFlight(claim.Operation, now) {
		rec, rerr := process.RecoverInterruptedClaim(gameID, m.configDir, m.instanceID, claim, gabpLive, selfConnectionFrom(bridgeBound, claim.LaunchID), now)
		if rerr != nil {
			m.log.Warnw("claim recovery failed", "gameId", gameID, "error", rerr)
			return "unknown", nil, false
		}
		if rec != nil {
			if rec.Superseded {
				return "", nil, true
			}
			if rec.Removed {
				return "stale-runtime-cleaned", &rec.Evidence, false
			}
			if rec.Claim != nil {
				claim = rec.Claim
			}
			if claim.Phase == process.PhaseActive {
				if rec.Evidence.Verdict == process.StatusRunning {
					return process.RuntimeStateStatusRunning, &rec.Evidence, false
				}
				return "unknown", &rec.Evidence, false
			}
			return process.RuntimeStateStatusStarting, &rec.Evidence, false
		}
	}

	// An in-flight bounded operation owns its claim: status truthfully reports
	// the persisted phase and never cleans the claim out from under the executor.
	opInFlight := process.OperationInFlight(claim.Operation, now)

	ev := process.EvaluateLiveness(process.LivenessInput{
		GABPLive:         gabpLive,
		CallerInstanceID: m.instanceID,
		Claim:            claim,
		StatusHook:       hook,
		GameID:           gameID,
		Profile:          profile,
	})
	switch ev.Verdict {
	case process.StatusRunning:
		// A live claim: reconcile any pending history credits whose write failed.
		// Gated on a non-empty pending list so a steady-state status stays
		// read-only.
		if len(claim.PendingDeliveries) > 0 || len(claim.PendingCleanStops) > 0 {
			if err := process.ReconcilePendingCredits(gameID, m.configDir, claim.LaunchID); err != nil {
				m.log.Warnw("pending credit reconciliation failed", "gameId", gameID, "error", err)
			}
		}
		switch claim.Phase {
		case process.PhaseStarting:
			if claim.Operation == nil {
				// Passive promotion (design/20): running seen by a status
				// observation promotes a completed-unobserved claim to active;
				// credit the start from the pinned identity BEFORE the flip
				// commits (record-first + launchID-idempotent).
				if _, err := process.FencedTransitionWithCredit(gameID, m.configDir, claim.LaunchID, "", func(st *process.RuntimeState) error {
					if st.Operation != nil || st.Phase != process.PhaseStarting {
						return process.ErrFencingViolation
					}
					st.Phase = process.PhaseActive
					st.Status = process.RuntimeStateStatusRunning
					return nil
				}, func(st *process.RuntimeState) error {
					return m.ApplyPinnedWorkloadStart(gameID, st)
				}); err == nil {
					return process.RuntimeStateStatusRunning, &ev, false
				}
			}
			return process.RuntimeStateStatusStarting, &ev, false
		case process.PhaseStopping, process.PhaseKilling:
			if opInFlight {
				return claim.Phase, &ev, false
			}
		}
		return process.RuntimeStateStatusRunning, &ev, false
	case process.StatusUnknown:
		switch claim.Phase {
		case process.PhaseStarting:
			return process.RuntimeStateStatusStarting, &ev, false
		case process.PhaseStopping, process.PhaseKilling:
			if opInFlight {
				return claim.Phase, &ev, false
			}
		}
		return "unknown", &ev, false
	default: // stopped
		if opInFlight {
			switch claim.Phase {
			case process.PhaseStopping, process.PhaseKilling:
				return claim.Phase, &ev, false
			}
			return process.RuntimeStateStatusStarting, &ev, false
		}
		// Completed-unobserved asymmetry (design/05 Stage 4): absence-based
		// stopped never clears the claim — only positive evidence does.
		if claim.Phase == process.PhaseStarting && claim.Operation == nil && claim.SpawnState == process.SpawnStateSpawned {
			positive := ev.Source == process.LivenessSourceStatusHook ||
				(ev.Source == process.LivenessSourcePID && claim.PIDRole == process.PIDRoleWorkload)
			if !positive {
				return process.RuntimeStateStatusStarting, &ev, false
			}
		}
		// Fenced removal (design/06): remove only while the evaluated launch
		// identity still holds and no operation was admitted meanwhile.
		if err := process.RemoveRuntimeStateIfCurrent(gameID, m.configDir, m.instanceID, claim.LaunchID, selfConnectionFrom(bridgeBound, claim.LaunchID)); err != nil {
			if errors.Is(err, process.ErrFencingViolation) {
				return "", &ev, true
			}
			// A non-fencing removal failure (write/lock/permission): the claim is
			// RETAINED, so this is not a supersession retry. The empty status is
			// only meaningful for that retry branch; returning it here would render
			// an empty status (CLI) or hand the MCP path an invalid one. Report a
			// real "unknown" — liveness read stopped but the authoritative claim
			// could not be finalized — keeping ev's detail for the human.
			m.log.Warnw("failed to remove stopped runtime claim", "gameId", gameID, "error", err)
			return "unknown", &ev, false
		}
		m.log.Debugw("removed stopped runtime claim", "gameId", gameID, "evidence", ev.Detail)
		return "stale-runtime-cleaned", &ev, false
	}
}
