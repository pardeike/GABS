package process

import (
	"errors"
	"fmt"
	"time"
)

// OperationInFlight reports whether op is a live bounded attempt: within
// its pinned deadline with an executor not provably gone. Expired or
// provably-dead attempts are recoverable (design/07); an uninspectable
// executor within the window is still in flight — unknown is not gone.
func OperationInFlight(op *RuntimeOperation, now time.Time) bool {
	if op == nil {
		return false
	}
	if op.Deadline.IsZero() || !now.Before(op.Deadline) {
		return false
	}
	return !executorProvablyGone(op)
}

// ClaimRecovery is the outcome of normalizing a claim that carried a dead
// operation. Applied reports whether anything was persisted; Claim is the
// post-recovery state when kept (the original claim when nothing applied).
// Superseded means the evaluated claim lost its fence mid-recovery — it was
// removed or replaced — and the caller must reload and re-evaluate the
// CURRENT claim; the evaluated snapshot must never be presented as
// post-recovery state.
type ClaimRecovery struct {
	Removed    bool
	Applied    bool
	Superseded bool
	Claim      *RuntimeState
	Evidence   LivenessEvidence
}

// RecoverInterruptedClaim implements restart recovery (design/07, design/05
// Stage 3) for a claim whose operation is dead — executor provably gone or
// deadline expired. It returns nil when there is nothing to recover (no
// operation, or a live one). The interrupted attempt is never replayed:
// recovery is liveness over the pinned context, and every write is fenced
// by launchID + the dead attempt's operationID, so a racing successor is
// never touched and the dead executor's own late completion stays rejected.
//
//   - phase stopping/killing (or a lost stop/kill completion on an active
//     claim): record lastActionResult{outcome: interrupted}, clear the
//     operation, then phase per liveness — running → active, stopped →
//     claim removed, unknown → active with the unknown verdict reported.
//   - phase starting, spawnState preflight, executor provably gone: the one
//     safe removal — process creation was never attempted, no liveness
//     puzzle (no probe runs).
//   - phase starting, spawnState spawning/spawned (the crash-during-spawn
//     window): normal liveness — running promotes to active, definitive
//     stopped removes the claim, genuinely unknown preserves it occupied
//     without a write.
//
// Attachment records are never touched: the executor is distinct from the
// attachment owner (a CLI attempt dying must not disturb the server that
// owns the live bridge).
func RecoverInterruptedClaim(gameID, configDir, instanceID string, claim *RuntimeState, gabpLive bool, selfLive func() bool, now time.Time) (*ClaimRecovery, error) {
	if claim == nil || claim.SchemaVersion < RuntimeSchemaVersion {
		return nil, nil
	}
	op := claim.Operation
	if op == nil || OperationInFlight(op, now) {
		return nil, nil
	}

	if claim.Phase == PhaseStarting && claim.SpawnState == SpawnStatePreflight {
		if !executorProvablyGone(op) {
			// Expired but not provably dead: leave it occupied — the stale
			// claim is the start gate's supersession business.
			return &ClaimRecovery{Claim: claim}, nil
		}
		if err := removeRuntimeStateGuarded(gameID, configDir, instanceID, claim.LaunchID, op.OperationID, selfLive); err != nil {
			if errors.Is(err, errStopAttachmentLive) {
				// A bridge holds the claim: not removable, leave occupied.
				return &ClaimRecovery{Claim: claim}, nil
			}
			if errors.Is(err, ErrFencingViolation) || errors.Is(err, ErrNoRuntimeClaim) {
				return &ClaimRecovery{Superseded: true}, nil
			}
			return nil, err
		}
		return &ClaimRecovery{Removed: true, Applied: true}, nil
	}

	ev := EvaluateLiveness(LivenessInput{
		GABPLive:   gabpLive,
		Claim:      claim,
		StatusHook: claimStatusHook(claim),
		GameID:     gameID,
		Profile:    EffectiveClaimProfile(claim),
		Now:        now,
	})
	rec := &ClaimRecovery{Claim: claim, Evidence: ev}

	// A dead start attempt is not a stop/kill outcome: lastActionResult
	// records lifecycle attempts only (design/06).
	reason := "its pinned deadline expired"
	if executorProvablyGone(op) {
		reason = "its executor is no longer running"
	}
	var interrupted *RuntimeActionResult
	if op.Action == OperationActionStop || op.Action == OperationActionKill {
		interrupted = &RuntimeActionResult{
			Action:    op.Action,
			Outcome:   OutcomeInterrupted,
			Detail:    fmt.Sprintf("a %s attempt begun %s was interrupted (%s)", op.Action, op.AttemptStartedAt.Format(time.RFC3339), reason),
			Timestamp: now,
		}
	}

	normalize := func() error {
		updated, err := FencedTransition(gameID, configDir, claim.LaunchID, op.OperationID, func(s *RuntimeState) error {
			if interrupted != nil {
				s.LastActionResult = interrupted
			}
			s.Operation = nil
			s.Phase = PhaseActive
			if ev.Verdict == StatusRunning {
				s.Status = RuntimeStateStatusRunning
			}
			return nil
		})
		if err != nil {
			return err
		}
		rec.Applied = true
		rec.Claim = updated
		return nil
	}

	switch ev.Verdict {
	case StatusRunning:
		if err := normalize(); err != nil {
			if errors.Is(err, ErrFencingViolation) || errors.Is(err, ErrNoRuntimeClaim) {
				return &ClaimRecovery{Superseded: true, Evidence: ev}, nil
			}
			return nil, err
		}
		return rec, nil
	case StatusUnknown:
		if claim.Phase == PhaseStarting {
			// Crash-during-spawn window with genuinely unknown evidence:
			// preserved occupied, no write (design/05 Stage 3).
			return rec, nil
		}
		if err := normalize(); err != nil {
			if errors.Is(err, ErrFencingViolation) || errors.Is(err, ErrNoRuntimeClaim) {
				return &ClaimRecovery{Superseded: true, Evidence: ev}, nil
			}
			return nil, err
		}
		return rec, nil
	default: // stopped
		err := removeRuntimeStateGuarded(gameID, configDir, instanceID, claim.LaunchID, op.OperationID, selfLive)
		switch {
		case err == nil:
			rec.Removed = true
			rec.Applied = true
			rec.Claim = nil
			return rec, nil
		case errors.Is(err, errStopAttachmentLive):
			// A live bridge appeared between evaluation and removal: never
			// cleared under a live bridge — normalize to active instead.
			ev.Verdict = StatusRunning
			rec.Evidence = ev
			if nerr := normalize(); nerr != nil {
				if errors.Is(nerr, ErrFencingViolation) || errors.Is(nerr, ErrNoRuntimeClaim) {
					return &ClaimRecovery{Superseded: true, Evidence: ev}, nil
				}
				return nil, nerr
			}
			return rec, nil
		case errors.Is(err, ErrFencingViolation) || errors.Is(err, ErrNoRuntimeClaim):
			return &ClaimRecovery{Superseded: true, Evidence: ev}, nil
		default:
			return nil, err
		}
	}
}
