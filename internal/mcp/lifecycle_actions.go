package mcp

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
)

// lifecycleLockTimeout bounds attachment-record transitions; contention is
// logged, never a hang.
const lifecycleLockTimeout = 5 * time.Second

// bridgeAttachmentRef is the in-process identity of the current bridge
// attachment for a game: which claim (launchID) and which connection
// lifetime (connectionID) this server last persisted. Detach callbacks
// carry it so an old disconnect can never clear a newer connection
// (design/06).
type bridgeAttachmentRef struct {
	launchID     string
	connectionID string
}

// recordBridgeAttachment persists the attachment record after a successful
// GABP connect (design/04): connectionID, owner instance + process
// fingerprint, and a renewable lease. A starting claim promotes to active —
// a live bridge is proof of running (design/05 Stage 4 passive promotion).
// The in-flight start operation, if any, is not touched: operations are
// completed only by their own fenced transitions.
func (s *Server) recordBridgeAttachment(gameID string, isConnected func() bool) {
	now := time.Now().UTC()
	connID := process.NewFencingID()
	ownerPID := os.Getpid()
	ownerStart, err := process.ProcessStartTime(ownerPID)
	if err != nil {
		// Without a verifiable owner fingerprint the record would be
		// malformed evidence (design/04); skip persisting it.
		s.log.Warnw("cannot fingerprint this process; bridge attachment not persisted", "gameId", gameID, "error", err)
		return
	}
	lease := s.runtimeOwnerLeaseDuration()

	var launchID string
	_, terr := process.TransitionRuntimeState(gameID, s.configDir, lifecycleLockTimeout, func(st *process.RuntimeState) error {
		if st.SchemaVersion < process.RuntimeSchemaVersion {
			// Legacy claims get attachment records only after their full
			// normalization (design/07, lands with M2.8).
			return errAttachmentSkipped
		}
		launchID = st.LaunchID
		st.Attachment = &process.RuntimeAttachment{
			ConnectionID:      connID,
			OwnerInstanceID:   s.instanceID,
			OwnerPID:          ownerPID,
			OwnerPIDStartTime: ownerStart,
			ObservedAt:        now,
			LeaseDeadline:     now.Add(lease),
		}
		if st.Phase == process.PhaseStarting {
			st.Phase = process.PhaseActive
			st.Status = process.RuntimeStateStatusRunning
		}
		return nil
	})
	if terr != nil {
		if !errors.Is(terr, errAttachmentSkipped) && !errors.Is(terr, process.ErrNoRuntimeClaim) {
			s.log.Warnw("failed to persist bridge attachment", "gameId", gameID, "error", terr)
		}
		return
	}

	s.mu.Lock()
	if s.bridgeAttachments == nil {
		s.bridgeAttachments = make(map[string]bridgeAttachmentRef)
	}
	s.bridgeAttachments[gameID] = bridgeAttachmentRef{launchID: launchID, connectionID: connID}
	s.mu.Unlock()

	if isConnected != nil {
		go s.refreshBridgeAttachmentLease(gameID, launchID, connID, isConnected, lease)
	}
}

var errAttachmentSkipped = errors.New("attachment record skipped")

// refreshBridgeAttachmentLease renews the persisted lease while the socket
// stays connected (design/04); it stops the moment the connection dies, the
// identity rotates, or the fenced write is rejected.
func (s *Server) refreshBridgeAttachmentLease(gameID, launchID, connID string, isConnected func() bool, lease time.Duration) {
	interval := lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		cur, ok := s.bridgeAttachments[gameID]
		s.mu.RUnlock()
		if !ok || cur.connectionID != connID || !isConnected() {
			return
		}
		if _, err := process.FencedTransition(gameID, s.configDir, launchID, "", func(st *process.RuntimeState) error {
			a := st.Attachment
			if a == nil || a.ConnectionID != connID {
				return process.ErrFencingViolation
			}
			now := time.Now().UTC()
			a.ObservedAt = now
			a.LeaseDeadline = now.Add(lease)
			return nil
		}); err != nil {
			return
		}
	}
}

// clearBridgeAttachment removes the attachment record only while it still
// carries the given connection identity within the given claim lifetime.
func (s *Server) clearBridgeAttachment(gameID, launchID, connID string) {
	if launchID == "" || connID == "" {
		return
	}
	_, err := process.FencedTransition(gameID, s.configDir, launchID, "", func(st *process.RuntimeState) error {
		a := st.Attachment
		if a == nil || a.ConnectionID != connID {
			return process.ErrFencingViolation
		}
		st.Attachment = nil
		return nil
	})
	if err != nil && !errors.Is(err, process.ErrFencingViolation) && !errors.Is(err, process.ErrNoRuntimeClaim) {
		s.log.Warnw("failed to clear bridge attachment", "gameId", gameID, "error", err)
	}
}

// recordBridgeDetachment clears the current attachment record for gameID.
// Callers must not hold s.mu.
func (s *Server) recordBridgeDetachment(gameID string) {
	s.mu.Lock()
	ref, ok := s.bridgeAttachments[gameID]
	if ok {
		delete(s.bridgeAttachments, gameID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	s.clearBridgeAttachment(gameID, ref.launchID, ref.connectionID)
}

// takeBridgeAttachmentRefLocked pops the in-process attachment identity;
// for callers that already hold s.mu.
func (s *Server) takeBridgeAttachmentRefLocked(gameID string) (bridgeAttachmentRef, bool) {
	ref, ok := s.bridgeAttachments[gameID]
	if ok {
		delete(s.bridgeAttachments, gameID)
	}
	return ref, ok
}

// lifecycleActionResult routes games_stop/games_kill through the design/06
// pipeline when a current-schema claim exists. It returns nil when the
// legacy path should handle the call (no claim, or a pre-profile claim that
// M2.8's normalization has not touched yet).
func (s *Server) lifecycleActionResult(game config.GameConfig, action string) *ToolResult {
	claim, err := process.LoadRuntimeState(game.ID, s.configDir)
	if err != nil {
		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("The runtime claim for '%s' is unreadable: %v. Inspect it, or use 'gabs games repair %s --forget-runtime' if the game is provably gone.", game.ID, err, game.ID)}},
			IsError: true,
		}
	}
	if claim == nil || claim.SchemaVersion < process.RuntimeSchemaVersion {
		return nil
	}

	outcome, refusal, err := process.ExecuteStopAction(process.StopRequest{
		GameID:     game.ID,
		ConfigDir:  s.configDir,
		InstanceID: s.instanceID,
		Action:     action,
		GABPLive:   func() bool { return s.hasLiveGABPClient(game.ID) },
		ReapLauncher: func() {
			s.mu.RLock()
			controller := s.games[game.ID]
			s.mu.RUnlock()
			if controller != nil {
				controller.TerminateDirectChild()
			}
		},
	})
	if err != nil {
		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to %s '%s': %v", action, game.ID, err)}},
			IsError: true,
		}
	}
	if refusal != nil {
		return s.stopRefusalResult(game, action, refusal)
	}

	if outcome.Code == process.OutcomeTerminated {
		// The claim is gone; release the in-memory controller, bridge
		// client, mirrored tools, and the diagnostic bridge file.
		s.cleanupStoppedGame(game.ID)
		s.CleanupBridgeConfig(game.ID)
	}
	return s.stopOutcomeResult(game, action, outcome)
}

func (s *Server) stopRefusalResult(game config.GameConfig, action string, ref *process.StopRefusal) *ToolResult {
	structured := map[string]interface{}{
		"code":   ref.Code,
		"gameId": game.ID,
		"action": action,
	}
	if ref.Phase != "" {
		structured["phase"] = ref.Phase
	}
	if op := ref.Operation; op != nil {
		structured["operation"] = map[string]interface{}{
			"action":           op.Action,
			"attemptStartedAt": op.AttemptStartedAt.Format(time.RFC3339),
			"deadline":         op.Deadline.Format(time.RFC3339),
		}
	}

	var next []map[string]interface{}
	isError := true
	switch ref.Code {
	case process.RefusalOperationInFlight:
		// Informational: a bounded operation is already doing lifecycle
		// work; the caller re-checks after its deadline.
		isError = false
		next = append(next, mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Re-check once the in-flight operation finishes."))
	case process.RefusalStopUnsupported:
		if ref.KillCapable {
			next = append(next, mcpNextAction("games_kill", map[string]interface{}{"gameId": game.ID}, "Force termination is the configured way to end this game."))
		}
		next = append(next, mcpNextAction("games_show", map[string]interface{}{"gameId": game.ID}, "Add a stop hook or stopProcessName to enable graceful stops."))
	case process.RefusalKillUnsupported:
		next = append(next, mcpNextAction("games_show", map[string]interface{}{"gameId": game.ID}, "Add a kill hook or stopProcessName to enable force termination."))
	}
	if len(next) > 0 {
		structured["nextActions"] = next
	}
	return &ToolResult{
		Content:           []Content{{Type: "text", Text: ref.Message}},
		StructuredContent: structured,
		IsError:           isError,
	}
}

func (s *Server) stopOutcomeResult(game config.GameConfig, action string, outcome *process.StopOutcome) *ToolResult {
	structured := map[string]interface{}{
		"code":         outcome.Code,
		"gameId":       game.ID,
		"action":       action,
		"claimRemoved": outcome.ClaimRemoved,
	}
	if outcome.ActiveProfile != "" {
		structured["activeProfile"] = outcome.ActiveProfile
	}
	if outcome.Detail != "" {
		structured["evidence"] = outcome.Detail
	}
	if len(outcome.Warnings) > 0 {
		structured["warnings"] = outcome.Warnings
	}
	if hr := outcome.HookResult; hr != nil {
		structured["exitCode"] = hr.ExitCode
		if hr.StderrTail != "" {
			structured["stderrTail"] = hr.StderrTail
		}
		if hr.TreeKillWarning {
			structured["treeKillWarning"] = true
		}
	}

	verb := "stop"
	if action == process.OperationActionKill {
		verb = "kill"
	}
	var text string
	isError := false
	var next []map[string]interface{}
	switch outcome.Code {
	case process.OutcomeTerminated:
		past := "stopped"
		if action == process.OperationActionKill {
			past = "killed"
		}
		text = fmt.Sprintf("Game '%s' (%s) was %s and its termination verified.", game.ID, game.Name, past)
		if outcome.Detail != "" {
			text += " " + outcome.Detail + "."
		}
	case process.OutcomeActionSucceededRunning:
		text = fmt.Sprintf("The %s action for '%s' succeeded, but the game is still running at the end of the verification window (%s).", verb, game.ID, outcome.Detail)
		if action == process.OperationActionStop {
			next = append(next, mcpNextAction("games_kill", map[string]interface{}{"gameId": game.ID}, "Force termination if the game will not stop gracefully."))
		}
		next = append(next, mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Watch the game; it may still be saving and shutting down."))
	case process.OutcomeTerminationUnverified:
		text = fmt.Sprintf("The %s action for '%s' succeeded, but termination could not be verified (%s). The runtime claim is kept; a later observation that finds the game stopped will clear it.", verb, game.ID, outcome.Detail)
		next = append(next, mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Re-check for definitive evidence."))
	case process.OutcomeActionTimedOut:
		isError = true
		text = fmt.Sprintf("The %s hook for '%s' timed out and its process tree was terminated.", verb, game.ID)
		if hr := outcome.HookResult; hr != nil && hr.StderrTail != "" {
			text += " Stderr tail: " + hr.StderrTail
		}
	default: // action_failed
		isError = true
		text = fmt.Sprintf("The %s action for '%s' failed.", verb, game.ID)
		if hr := outcome.HookResult; hr != nil {
			text += fmt.Sprintf(" Exit code %d.", hr.ExitCode)
			if hr.StderrTail != "" {
				text += " Stderr tail: " + hr.StderrTail
			}
		} else if outcome.Detail != "" {
			text += " " + outcome.Detail + "."
		}
		if action == process.OperationActionStop {
			next = append(next, mcpNextAction("games_kill", map[string]interface{}{"gameId": game.ID}, "Force termination if graceful stopping keeps failing."))
		}
		next = append(next, mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Inspect the game's current state and lastActionResult."))
	}
	if len(outcome.Warnings) > 0 {
		for _, w := range outcome.Warnings {
			text += "\nWarning: " + w
		}
	}
	if len(next) > 0 {
		structured["nextActions"] = next
	}
	return &ToolResult{
		Content:           []Content{{Type: "text", Text: text}},
		StructuredContent: structured,
		IsError:           isError,
	}
}

// attachRuntimeLifecycle surfaces the persisted lifecycle facts —
// phase, in-flight operation timing, lastActionResult — in games_status
// structured output (design/06: status is non-blocking and truthful).
func attachRuntimeLifecycle(statusItem map[string]interface{}, rs *process.RuntimeState) {
	if rs == nil {
		return
	}
	if rs.Phase != "" {
		statusItem["phase"] = rs.Phase
	}
	if op := rs.Operation; op != nil {
		statusItem["operation"] = map[string]interface{}{
			"action":           op.Action,
			"attemptStartedAt": op.AttemptStartedAt.Format(time.RFC3339),
			"deadline":         op.Deadline.Format(time.RFC3339),
		}
	}
	if lar := rs.LastActionResult; lar != nil {
		entry := map[string]interface{}{
			"action":    lar.Action,
			"outcome":   lar.Outcome,
			"timestamp": lar.Timestamp.Format(time.RFC3339),
		}
		if lar.ExitCode != nil {
			entry["exitCode"] = *lar.ExitCode
		}
		if lar.StderrTail != "" {
			entry["stderrTail"] = lar.StderrTail
		}
		if lar.Detail != "" {
			entry["detail"] = lar.Detail
		}
		if lar.TreeKillWarning {
			entry["treeKillWarning"] = true
		}
		statusItem["lastActionResult"] = entry
	}
}
