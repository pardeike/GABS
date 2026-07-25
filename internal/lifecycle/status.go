package lifecycle

import (
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
)

// Status evaluates a game's liveness from its persisted runtime claim using the
// one liveness rule (design/04): the pinned status hook plus built-in evidence,
// with the claim's persisted attachment lease / owner fingerprint as the
// cross-process running-evidence. gabpLive is the caller's own live-bridge
// signal (a one-shot CLI passes false). It returns the verdict and the claim
// (nil when no claim exists — a stopped/untracked game).
func (m *Manager) Status(gameID string, gabpLive bool) (process.LivenessEvidence, *process.RuntimeState, error) {
	claim, err := process.LoadRuntimeState(gameID, m.configDir)
	if err != nil {
		return process.LivenessEvidence{}, nil, err
	}
	var hook *launch.ResolvedHook
	profile := ""
	if claim != nil {
		if claim.Lifecycle != nil {
			hook = claim.Lifecycle.Status
		}
		profile = process.EffectiveClaimProfile(claim)
	}
	ev := process.EvaluateLiveness(process.LivenessInput{
		GABPLive:         gabpLive,
		CallerInstanceID: m.instanceID,
		Claim:            claim,
		StatusHook:       hook,
		GameID:           gameID,
		Profile:          profile,
	})
	return ev, claim, nil
}
