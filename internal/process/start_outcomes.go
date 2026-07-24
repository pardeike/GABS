package process

// Terminal start-outcome codes (design/05, design/10). These name the three
// non-failure ways a start can end; none carries a cause class (see Classify).
//
//   - OutcomeStartedConnected: Stages 1–5 completed and a GABP bridge attached
//     synchronously within the budget.
//   - OutcomeStartedBridgePending: the workload verified through Stage 4 and a
//     server frontend is attaching the bridge in the background (Stage 5).
//   - OutcomeStartedAttachmentDeferred: the workload verified through Stage 4
//     and attachment was intentionally not attempted — the terminal success of
//     a one-shot CLI start, which cannot hold the GABP socket open. The claim
//     is left in phase active; a later server games_connect attaches from the
//     persisted endpoint (design/11 CLI surface).
//
// The classifier and both frontends refer to these constants so the shared
// lifecycle manager can name a start outcome without a frontend dependency.
const (
	OutcomeStartedConnected          = "started_connected"
	OutcomeStartedBridgePending      = "started_bridge_pending"
	OutcomeStartedAttachmentDeferred = "started_attachment_deferred"
)
