package mcp

import (
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/lifecycle"
	"github.com/pardeike/gabs/internal/process"
)

// historyContext aliases the shared lifecycle type so the frontend's existing
// references stay unchanged; the pipeline and the presentation code render
// attribution from the same value (design/08).
type historyContext = lifecycle.HistoryContext

// steamNotRunningAdvisory re-exposes the shared Stage-2 advisory string for the
// frontend's tests and any presentation that names it.
const steamNotRunningAdvisory = lifecycle.SteamNotRunningAdvisory

// lifecycle builds a lifecycle.Manager view over the server's current
// lifecycle-relevant fields. It is rebuilt per call so a test that swaps the
// controller factory after construction is always honored (newController is the
// one mutable field); every other field is set-once at wiring time.
func (s *Server) lifecycle() *lifecycle.Manager {
	return lifecycle.NewManager(s.log, s.configDir, s.instanceID, s.gamesConfig, s.ownerLease, s.starter, s.newController)
}

// The forwarders below keep the frontend's original helper names so its many
// call sites (start/status/recovery/attachment/presentation) reach the shared
// implementation without churn.

func (s *Server) computeHistoryContext(snap *config.Snapshot, game config.GameConfig, resolved *launch.Resolved, inputs map[string]interface{}) historyContext {
	return s.lifecycle().ComputeHistoryContext(snap, game, resolved, inputs)
}

func (s *Server) buildHistoryContext(snap *config.Snapshot, game config.GameConfig, resolved *launch.Resolved, inputs map[string]interface{}) historyContext {
	return s.lifecycle().BuildHistoryContext(snap, game, resolved, inputs)
}

func (s *Server) applyPinnedWorkloadStart(gameID string, st *process.RuntimeState) error {
	return s.lifecycle().ApplyPinnedWorkloadStart(gameID, st)
}

func (s *Server) contextProven(gameID string, hc historyContext) bool {
	return s.lifecycle().ContextProven(gameID, hc)
}

func (s *Server) inputComboProven(gameID string, hc historyContext) bool {
	return s.lifecycle().InputComboProven(gameID, hc)
}

func (s *Server) supersededStartRefusal(gameID string) error {
	return s.lifecycle().SupersededStartRefusal(gameID)
}

func (s *Server) runtimeOwnerLeaseForOperation(operationTimeout time.Duration) time.Duration {
	return s.lifecycle().RuntimeOwnerLeaseForOperation(operationTimeout)
}

func (s *Server) runtimeOwnerLeaseDuration() time.Duration {
	return s.lifecycle().RuntimeOwnerLeaseDuration()
}
