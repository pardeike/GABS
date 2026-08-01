package lifecycle

import (
	"github.com/pardeike/gabs/internal/process"
)

// StopRequest is one stop or kill against the persisted runtime claim. The
// server frontend supplies its live-bridge / self-connection / launcher-reap
// callbacks; a one-shot CLI passes nil for all three and the claim's persisted
// evidence governs (design/04, design/06).
type StopRequest struct {
	GameID             string
	Action             string // process.OperationActionStop | process.OperationActionKill
	HistoryProfile     string
	HistoryContextHash string
	GABPLive           func() bool
	SelfConnection     func(connectionID string) bool
	ReapLauncher       func()
}

// LoadStopClaim loads and, if needed, normalizes the runtime claim a stop/kill
// operates on. A nil claim with a nil error means no claim exists (nothing for
// the manager to act on — the frontend decides how to report that).
func (m *Manager) LoadStopClaim(gameID, launchMode, configRevision string) (*process.RuntimeState, error) {
	claim, err := process.LoadRuntimeState(gameID, m.configDir)
	if err != nil || claim == nil {
		return claim, err
	}
	if claim.SchemaVersion == 0 {
		normalized, nerr := process.NormalizeLegacyClaim(gameID, m.configDir, launchMode, configRevision)
		if nerr != nil {
			return nil, nerr
		}
		claim = normalized
	}
	return claim, nil
}

// Stop executes a stop or kill against the runtime claim (design/06): bounded
// single-operation admission under the transition lock, hook-or-builtin action,
// and the post-action verification matrix. Exactly one of outcome/refusal is
// non-nil on a nil error. The caller renders the typed result.
func (m *Manager) Stop(req StopRequest) (*process.StopOutcome, *process.StopRefusal, error) {
	return process.ExecuteStopAction(process.StopRequest{
		GameID:             req.GameID,
		ConfigDir:          m.configDir,
		InstanceID:         m.instanceID,
		Action:             req.Action,
		HistoryProfile:     req.HistoryProfile,
		HistoryContextHash: req.HistoryContextHash,
		GABPLive:           req.GABPLive,
		SelfConnection:     req.SelfConnection,
		ReapLauncher:       req.ReapLauncher,
	})
}
