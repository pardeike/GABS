package process

import "time"

// Round-16 F5: verified history events (clean stops, verified deliveries) whose
// credit failed once are recorded as SELF-CONTAINED pending events on the claim,
// each carrying its own immutable identity + history coordinates. Reconciliation
// credits the exact event as a pure function of the entry — never re-reading the
// claim's current Operation/Attachment, which ordinary lifecycle replaces or
// clears. Every deleter reconciles both lists before removal (and status also
// reconciles pending deliveries on the live claim, for the disconnect case).

// appendPendingCredit records a pending credit for an already-happened event
// (a verified welcome report, a verified termination) unless its id is already
// present. It NEVER drops the event at a cap (round 17 F5): the report was
// already consumed / the action already executed, so refusing would be permanent
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
// in ONE history write (round 17 F5): each is credited by its own self-contained
// coordinates, idempotent by a lifetime-coupled marker (CreditedPendingEvents)
// that is GC'd against the claim's current pending ids in the SAME write — so
// the dedup identity is durable exactly as long as the pending record it guards,
// never dropped by an LRU while the record can still replay. Does NOT prune
// cur's pending lists: the caller prunes them and persists runtime AFTER this
// (or removes the claim); until that prune is durable the markers keep the
// credit idempotent, and are GC'd once the pending records are gone. MUST hold
// the per-game transition lock.
func creditPendingEventsLocked(gameID, configDir string, cur *RuntimeState) error {
	live := livePendingMarkers(cur)
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
		// GC: a marker whose pending record is no longer on the claim is durably
		// unreplayable and safe to forget, keeping the marker set a bounded
		// subset of the current pending ids.
		for _, e := range h.Profiles {
			e.retainPendingCreditMarkers(live)
		}
	})
}

// livePendingMarkers is the "kind:id" marker set for the claim's CURRENT pending
// records — the markers whose credit could still replay.
func livePendingMarkers(cur *RuntimeState) map[string]bool {
	live := make(map[string]bool, len(cur.PendingDeliveries)+len(cur.PendingCleanStops))
	for _, p := range cur.PendingDeliveries {
		live["delivery:"+p.ID] = true
	}
	for _, p := range cur.PendingCleanStops {
		live["stop:"+p.ID] = true
	}
	return live
}

// reconcilePendingBeforeRemoval credits every pending event on cur under the
// transition lock immediately BEFORE the claim is removed (round 16 F5). If the
// credit write fails it returns the error so the caller ABORTS removal, leaving
// the claim + its durable pending records for a later retry — no verified event
// is ever lost with the deleted claim. cur is loaded under the lock.
func reconcilePendingBeforeRemoval(gameID, configDir string, cur *RuntimeState) error {
	return creditPendingEventsLocked(gameID, configDir, cur)
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
	if err := creditPendingEventsLocked(gameID, configDir, cur); err != nil {
		return err
	}
	// All pending events are now credited (marked). Prune them and persist; the
	// markers keep the credit idempotent until this prune is durable (a save
	// failure leaves both the pending records and their markers, so a replay
	// re-credits nothing).
	cur.PendingDeliveries = nil
	cur.PendingCleanStops = nil
	return SaveRuntimeState(gameID, configDir, *cur)
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
			// Never dropped at a cap — the report was already consumed, so a
			// refusal would be permanent loss (round 17 F5). The reconcile that
			// follows this append drains the list.
			st.PendingDeliveries = appendPendingCredit(st.PendingDeliveries, pendingDeliveryEvent(st, connectionID, at))
		}
		return nil
	})
	return err
}
