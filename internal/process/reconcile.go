package process

import (
	"fmt"
	"time"
)

// Round-16 F5: verified history events (clean stops, verified deliveries) whose
// credit failed once are recorded as SELF-CONTAINED pending events on the claim,
// each carrying its own immutable identity + history coordinates. Reconciliation
// credits the exact event as a pure function of the entry — never re-reading the
// claim's current Operation/Attachment, which ordinary lifecycle replaces or
// clears. Every deleter reconciles both lists before removal (and status also
// reconciles pending deliveries on the live claim, for the disconnect case).

// pendingCreditCap bounds each pending list. Pending lists only grow during a
// sustained history-write outage (normally 0-1 entries), so hitting the cap is
// a real fault, never routine — it is logged loudly and the append is refused
// rather than silently evicting an older un-credited event (which would be the
// very loss this design prevents).
const pendingCreditCap = 256

// appendPendingCreditOnce appends a pending credit unless its ID is already
// present (a repeat observation of the same event) or the list is full. Returns
// the new list and whether it changed; overflow returns changed=false so the
// caller can surface the fault.
func appendPendingCreditOnce(list []PendingCredit, e PendingCredit) (out []PendingCredit, changed, overflow bool) {
	for _, p := range list {
		if p.ID == e.ID {
			return list, false, false
		}
	}
	if len(list) >= pendingCreditCap {
		return list, false, true
	}
	return append(list, e), true, false
}

// removePendingCreditByID drops the entry with id, if present.
func removePendingCreditByID(list []PendingCredit, id string) ([]PendingCredit, bool) {
	for i, p := range list {
		if p.ID == id {
			return append(list[:i:i], list[i+1:]...), true
		}
	}
	return list, false
}

// creditPendingEventsLocked credits every pending clean-stop and delivery on cur
// by the entry's OWN stored coordinates (idempotent by "stop:"/"delivery:"+ID),
// pruning each entry from cur once its credit has landed. MUST be called under
// the per-game transition lock. Returns whether cur's pending lists changed (so
// the caller can persist them) and the FIRST credit error encountered — on
// error the credited entries are already pruned, so a retry re-attempts only the
// rest. A no-op (no error, no change) when nothing is pending.
func creditPendingEventsLocked(gameID, configDir string, cur *RuntimeState) (changed bool, err error) {
	for _, p := range append([]PendingCredit(nil), cur.PendingCleanStops...) {
		if cerr := applyCleanStop(gameID, configDir, p.Profile, p.ContextHash, p.ID, creditAt(p.At)); cerr != nil {
			return changed, cerr
		}
		cur.PendingCleanStops, _ = removePendingCreditByID(cur.PendingCleanStops, p.ID)
		changed = true
	}
	for _, p := range append([]PendingCredit(nil), cur.PendingDeliveries...) {
		if cerr := ApplyDeliveryVerifiedLocked(gameID, configDir, p.Profile, p.ContextHash, p.ID); cerr != nil {
			return changed, cerr
		}
		cur.PendingDeliveries, _ = removePendingCreditByID(cur.PendingDeliveries, p.ID)
		changed = true
	}
	return changed, nil
}

func creditAt(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at
}

// reconcilePendingBeforeRemoval credits every pending event on cur under the
// transition lock immediately BEFORE the claim is removed (round 16 F5). If any
// credit write fails it returns the error so the caller ABORTS removal and
// persists cur (with the credited entries already pruned) for a later retry —
// no verified event is ever lost with the deleted claim. cur is the claim
// loaded under the lock; the caller must hold that lock.
func reconcilePendingBeforeRemoval(gameID, configDir string, cur *RuntimeState) error {
	_, err := creditPendingEventsLocked(gameID, configDir, cur)
	return err
}

// pendingDeliveryEvent builds a self-contained pending delivery credit for a
// verified welcome report observed on connectionID (round 16 F5). Raw observed
// env values are never carried — only the derived verdict's identity.
func pendingDeliveryEvent(st *RuntimeState, connectionID string, at time.Time) PendingCredit {
	return PendingCredit{
		ID:          connectionID,
		Profile:     EffectiveClaimProfile(st),
		ContextHash: st.HistoryContextHash,
		At:          at,
	}
}

// pendingCleanStopEvent builds a self-contained pending clean-stop credit for a
// stop that verified termination under operationID (round 16 F5).
func pendingCleanStopEvent(operationID, profile, contextHash string, at time.Time) PendingCredit {
	return PendingCredit{ID: operationID, Profile: profile, ContextHash: contextHash, At: at}
}

// ReconcilePendingCredits credits any pending history events (clean stops AND
// verified deliveries) on the current claim by each event's own self-contained
// coordinates, pruning those that land — the round-16 F5 status/connect
// reconciliation on a LIVE claim (no removal), INDEPENDENT of the current
// Operation/Attachment (a disconnect or operation change must not strand an
// event). Fenced to launchID. Persists the pruned lists only when a credit
// landed, so a steady-state call never writes; on a credit-write failure the
// un-credited events are left for a later retry.
func ReconcilePendingCredits(gameID, configDir, launchID string) error {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	cur, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return err
	}
	if cur == nil || cur.LaunchID != launchID {
		return nil
	}
	if len(cur.PendingCleanStops) == 0 && len(cur.PendingDeliveries) == 0 {
		return nil
	}
	changed, cerr := creditPendingEventsLocked(gameID, configDir, cur)
	if changed {
		if serr := SaveRuntimeState(gameID, configDir, *cur); serr != nil && cerr == nil {
			cerr = serr
		}
	}
	return cerr
}

// AppendPendingDelivery evaluates the welcome report against the claim's PINNED
// digests under the transition lock (fenced to launchID + connectionID),
// renders the derived verdict for display, and — when it is VERIFIED — records a
// self-contained pending credit bound to THIS connectionID (round 16 F5). The
// credit itself is applied by ReconcilePendingCredits. Only the derived
// verdict + identity persist; the raw env-bearing report never does. Overflow of
// the bounded list is a loud fault, returned as an error, never a silent drop.
func AppendPendingDelivery(gameID, configDir, launchID, connectionID string, obs *ObservedContext, at time.Time) error {
	_, err := FencedTransition(gameID, configDir, launchID, "", func(st *RuntimeState) error {
		if st.Attachment == nil || st.Attachment.ConnectionID != connectionID {
			return ErrFencingViolation
		}
		verdict := EvaluateContextDelivery(st.ContextDigests, obs)
		st.ContextDelivery = verdict // rendered: the latest connection's verdict
		if verdict != nil && verdict.Overall == DeliveryVerified && st.HistoryContextHash != "" {
			next, _, overflow := appendPendingCreditOnce(st.PendingDeliveries, pendingDeliveryEvent(st, connectionID, at))
			if overflow {
				return fmt.Errorf("pending delivery credits for %s exceeded %d: a sustained history-write outage is losing credits", gameID, pendingCreditCap)
			}
			st.PendingDeliveries = next
		}
		return nil
	})
	return err
}
