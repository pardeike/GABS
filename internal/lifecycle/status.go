package lifecycle

import (
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
)

// Status drives the shared claim-status state machine (ObserveClaimStatus) over
// a game's persisted claim, so a CLI reports exactly what an MCP status would —
// including passive starting->active promotion with the workload-start credit,
// dead-operation recovery, pending-credit reconciliation, and fenced removal of
// a definitively stopped claim (design/04, /05, /06, /07). It returns the
// machine's status string, the liveness evidence, and the claim as it stands
// AFTER the machine ran (nil when no claim exists or it was cleaned).
//
// gabpLive is the caller's own live-bridge signal for the first pass and
// bridgeBound re-derives it after a lost fence; a one-shot CLI passes false and
// nil (the persisted attachment lease + owner fingerprint is the authoritative
// cross-process evidence, design/04).
func (m *Manager) Status(gameID string, gabpLive bool, bridgeBound func(launchID, connectionID string) bool) (string, *process.LivenessEvidence, *process.RuntimeState, error) {
	claim, err := process.LoadRuntimeState(gameID, m.configDir)
	if err != nil {
		return "unknown", nil, nil, err
	}
	if claim == nil {
		ev := process.LivenessEvidence{Verdict: process.StatusStopped, Source: process.LivenessSourceNone, Detail: "no runtime claim"}
		return "stopped", &ev, nil, nil
	}
	if claim.SchemaVersion >= process.RuntimeSchemaVersion {
		status, ev := m.ObserveClaimStatus(gameID, claim, gabpLive, bridgeBound)
		// The machine may have promoted or removed the claim; reload for the
		// caller so it renders the post-observation state.
		cur, _ := process.LoadRuntimeState(gameID, m.configDir)
		return status, ev, cur, nil
	}
	// A pre-profile (schema-0) claim that no lifecycle touch has normalized yet:
	// report a simple liveness verdict rather than running the current-schema
	// machine over it.
	var hook *launch.ResolvedHook
	if claim.Lifecycle != nil {
		hook = claim.Lifecycle.Status
	}
	ev := process.EvaluateLiveness(process.LivenessInput{
		GABPLive:         gabpLive,
		CallerInstanceID: m.instanceID,
		Claim:            claim,
		StatusHook:       hook,
		GameID:           gameID,
		Profile:          process.EffectiveClaimProfile(claim),
	})
	return ev.Verdict, &ev, claim, nil
}
