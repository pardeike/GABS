package process

import (
	"fmt"
	"time"
)

// Verified history events (clean stops, verified deliveries) whose credit
// failed once are recorded as SELF-CONTAINED pending events on the claim,
// each carrying its own immutable identity + history coordinates. Reconciliation
// credits the exact event as a pure function of the entry — never re-reading the
// claim's current Operation/Attachment, which ordinary lifecycle replaces or
// clears. Every deleter reconciles both lists before removal (and status also
// reconciles pending deliveries on the live claim, for the disconnect case).

// appendPendingCredit records a pending credit for an already-happened event
// (a verified welcome report, a verified termination) unless its id is already
// present. It NEVER drops the event at a cap: the report was already
// consumed / the action already executed, so refusing would be permanent
// loss. In normal operation the list stays tiny — the reconcile after every
// append drains it. It could only grow under a sustained, history-SPECIFIC write
// outage (runtime writes landing while history writes fail), and non-dropping is
// deliberately correct there: a bounded, replayable backlog is strictly better
// than discarding an event that already happened.
func appendPendingCredit(list []PendingCredit, e PendingCredit) []PendingCredit {
	for _, p := range list {
		if p.ID == e.ID {
			return list
		}
	}
	return append(list, e)
}

// creditPendingEventsLocked credits every pending clean-stop and delivery on cur
// in ONE history write: each is credited by its own self-contained
// coordinates, idempotent by a lifetime-coupled marker (CreditedPendingEvents).
// It does NOT garbage-collect markers and does NOT prune cur's pending lists —
// the caller must first make the runtime transition durable (prune+save, or
// claim removal) and only THEN GC the drained markers via
// gcPendingCreditMarkersLocked. Crediting-then-GCing in one write would be a
// bug: a just-credited event that is not yet durable in runtime (an appended
// completion whose removal has not committed) would have its marker dropped by an
// unrelated reconcile, letting the still-current event replay. MUST hold the
// per-game transition lock.
func creditPendingEventsLocked(gameID, configDir string, cur *RuntimeState) error {
	return applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		for _, p := range cur.PendingDeliveries {
			e := h.entryForContext(p.Profile, p.ContextHash)
			if e.markPendingCreditOnce("delivery:" + p.ID) {
				e.DeliveriesVerified++
			}
		}
		for _, p := range cur.PendingCleanStops {
			e := h.entryForContext(p.Profile, p.ContextHash)
			if e.markPendingCreditOnce("stop:" + p.ID) {
				e.CleanStops++
			}
		}
	})
}

// pendingMarkerKeys is the "kind:id" marker set for the claim's CURRENT pending
// records — the exact set a prune/removal of cur DE-REFERENCES, and therefore the
// only markers a following GC may drop.
func pendingMarkerKeys(cur *RuntimeState) map[string]bool {
	keys := make(map[string]bool, len(cur.PendingDeliveries)+len(cur.PendingCleanStops))
	for _, p := range cur.PendingDeliveries {
		keys["delivery:"+p.ID] = true
	}
	for _, p := range cur.PendingCleanStops {
		keys["stop:"+p.ID] = true
	}
	return keys
}

// gcPendingCreditMarkersLocked drops exactly the credited-event markers named in
// drained — the records a just-committed prune/removal made durably
// unreferenced. It is SCOPED: it never touches a marker outside drained, so a
// pending event on another path (durable in history but not yet in this claim's
// runtime state) keeps its marker. Run ONLY after the runtime transition is
// durable. MUST hold the transition lock.
func gcPendingCreditMarkersLocked(gameID, configDir string, drained map[string]bool) error {
	if len(drained) == 0 {
		return nil
	}
	return applyHistoryLocked(gameID, configDir, func(h *GameHistory) {
		for _, e := range h.Profiles {
			e.dropPendingCreditMarkers(drained)
		}
	})
}

// creditPendingThenRemoveLocked credits every pending history event on cur, then
// removes the claim, then GCs those markers — in that DURABLE order.
// GC runs only AFTER the removal is durable, so an intervening reconcile
// of an unrelated claim can never drop a marker whose runtime transition has not
// committed (a premature GC lets the still-current event replay and double-count).
// A credit-write failure persists the pending lists and ABORTS removal, leaving
// the claim + its durable pending records for a later retry — nothing is lost
// with the deleted claim. A removal failure leaves the credited markers intact
// (a stale marker is harmless — event ids are random — but a premature GC is
// not). MUST hold the transition lock; cur is the loaded claim, with any
// completion event already appended.
func creditPendingThenRemoveLocked(gameID, configDir string, cur *RuntimeState) error {
	drained := pendingMarkerKeys(cur)
	if rerr := creditPendingEventsLocked(gameID, configDir, cur); rerr != nil {
		if serr := SaveRuntimeState(gameID, configDir, *cur); serr != nil {
			return fmt.Errorf("pending credit failed (%v) and could not be persisted: %w", rerr, serr)
		}
		return rerr
	}
	if err := RemoveRuntimeState(gameID, configDir); err != nil {
		return err // credited markers retained; a stale marker after a failed removal is harmless
	}
	// The removal is durable: the claim no longer references these events, so
	// their markers are safe to forget. A GC-write failure only leaves harmless
	// stale markers, so it must not fail the already-durable removal.
	_ = gcPendingCreditMarkersLocked(gameID, configDir, drained)
	return nil
}

// pendingDeliveryEvent builds a self-contained pending delivery credit for a
// verified welcome report observed on connectionID. Raw observed
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
// stop that verified termination under operationID.
func pendingCleanStopEvent(operationID, profile, contextHash string, at time.Time) PendingCredit {
	return PendingCredit{ID: operationID, Profile: profile, ContextHash: contextHash, At: at}
}

// ReconcilePendingCredits credits any pending history events (clean stops AND
// verified deliveries) on the current claim by each event's own self-contained
// coordinates, pruning those that land — the status/connect
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
	cur, err := loadRuntimeStateLocked(gameID, configDir)
	if err != nil {
		return err
	}
	if cur == nil || cur.LaunchID != launchID {
		return nil
	}
	if len(cur.PendingCleanStops) == 0 && len(cur.PendingDeliveries) == 0 {
		return nil
	}
	drained := pendingMarkerKeys(cur)
	if err := creditPendingEventsLocked(gameID, configDir, cur); err != nil {
		return err
	}
	// All pending events are now credited (marked). Prune them and persist; the
	// markers keep the credit idempotent until this prune is durable (a save
	// failure leaves both the pending records and their markers, so a replay
	// re-credits nothing).
	cur.PendingDeliveries = nil
	cur.PendingCleanStops = nil
	if err := SaveRuntimeState(gameID, configDir, *cur); err != nil {
		return err
	}
	// The prune is durable: these events are gone from runtime state, so GC only
	// THEIR markers. A scoped drop (not a global retain-live sweep) is essential —
	// a marker for an event on another path that is durable in history but not yet
	// pruned from its own claim must survive. A GC-write failure
	// only leaves harmless stale markers.
	_ = gcPendingCreditMarkersLocked(gameID, configDir, drained)
	return nil
}

// AppendPendingDelivery evaluates the welcome report against the claim's PINNED
// digests under the transition lock (fenced to launchID + connectionID),
// renders the derived verdict for display, and — when it is VERIFIED — records a
// self-contained pending credit bound to THIS connectionID. The
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
			// Never dropped at a cap — the report was already consumed, so a
			// refusal would be permanent loss. The reconcile that
			// follows this append drains the list.
			st.PendingDeliveries = appendPendingCredit(st.PendingDeliveries, pendingDeliveryEvent(st, connectionID, at))
		}
		return nil
	})
	return err
}
