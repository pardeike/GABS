package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
)

// lifecycleLockTimeout bounds attachment-record transitions; contention is
// logged, never a hang.
const lifecycleLockTimeout = 5 * time.Second

// bridgeAttachmentRef is the atomic in-process binding published ONLY
// after handshake authentication and successful attachment publication:
// the exact client plus the launch and connection identities it persisted
// (review round 9). Detach callbacks carry it so an old disconnect can
// never clear a newer connection (design/06); every consumer lookup
// requires the exact client, a matching claim launchID, the matching
// persisted Attachment.ConnectionID, and a live authenticated socket.
type bridgeAttachmentRef struct {
	client       *gabp.Client
	launchID     string
	connectionID string
}

// errAttachmentSkipped: no record applies (no claim, a pre-normalization
// legacy claim, or the migration touch whose publication follows the
// endpoint persist) — the connection itself is legitimate.
var errAttachmentSkipped = errors.New("attachment record skipped")

// errAttachmentSuperseded: the claim vanished, was replaced, could not be
// written, or the connection stopped being current before publication — the
// connection has no persisted binding and must not survive to mirror tools
// or count as evidence (review round 8).
var errAttachmentSuperseded = errors.New("attachment publication superseded")

// claimBoundClient returns the game's live GABP client only when it is
// credential-bound to the CURRENT claim: a live socket plus this server's
// in-process attachment ref carrying the freshly loaded claim's launch
// identity. Every consumer of GABP evidence or mirrored capability uses
// this lookup — a live client for launch A must never prove, keep, or
// service launch B (design/03, design/04).
func (s *Server) claimBoundClient(gameID string) (*gabp.Client, *process.RuntimeState) {
	s.mu.RLock()
	ref, hasRef := s.bridgeAttachments[gameID]
	s.mu.RUnlock()
	if !hasRef || ref.client == nil || !ref.client.IsConnected() {
		return nil, nil
	}
	claim, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil || claim == nil || claim.SchemaVersion != process.RuntimeSchemaVersion {
		return nil, nil
	}
	if ref.launchID != claim.LaunchID {
		return nil, nil
	}
	if claim.Attachment == nil || claim.Attachment.ConnectionID != ref.connectionID {
		return nil, nil
	}
	return ref.client, claim
}

// bridgeBound reports — at call time — whether this server holds a live
// authenticated client bound to the given launch (and, when connectionID
// is non-empty, that exact connection).
func (s *Server) bridgeBound(gameID string) func(launchID, connectionID string) bool {
	return func(launchID, connectionID string) bool {
		s.mu.RLock()
		ref, ok := s.bridgeAttachments[gameID]
		s.mu.RUnlock()
		if !ok || ref.client == nil || !ref.client.IsConnected() {
			return false
		}
		if ref.launchID != launchID {
			return false
		}
		if connectionID != "" && ref.connectionID != connectionID {
			return false
		}
		return true
	}
}

// selfConnectionFor adapts the binding check to the removal guards'
// connection-scoped self-liveness signature.
func (s *Server) selfConnectionFor(gameID, launchID string) func(connectionID string) bool {
	bound := s.bridgeBound(gameID)
	return func(connectionID string) bool { return bound(launchID, connectionID) }
}

// boundGABPForClaim is the GABP-evidence predicate for one loaded claim:
// the binding must match the claim's launch AND its persisted attachment
// connection (review round 9).
func (s *Server) boundGABPForClaim(gameID string, claim *process.RuntimeState) bool {
	if claim == nil || claim.Attachment == nil {
		return false
	}
	return s.bridgeBound(gameID)(claim.LaunchID, claim.Attachment.ConnectionID)
}

// errStaleAttachmentCredential: the connection's credential is not the
// current claim's per-launch credential — it authenticated against an
// earlier launch's bridge and must not survive as this claim's evidence
// (design/03: tokens rotate every launch exactly for this).
var errStaleAttachmentCredential = errors.New("stale bridge credential")

// recordBridgeAttachment persists the attachment record after a successful
// GABP connect (design/04): connectionID, owner instance + process
// fingerprint, and a renewable lease. The record binds to the launch whose
// endpoint credential authenticated the handshake. A completed-unobserved
// starting claim promotes to active (design/05 Stage 4 passive promotion);
// a claim still carrying its start operation is left for that operation's
// own fenced completion. stillCurrent reports whether this connection is
// still the game's current live client; publication is undone immediately
// when it no longer is (disconnect-before-publication safety).
//
// Returns nil on publication, errAttachmentSkipped when no record applies,
// and errStaleAttachmentCredential when the credential does not match the
// current claim — the caller must not keep such a connection.
func (s *Server) recordBridgeAttachment(gameID string, client *gabp.Client, endpointPort int, endpointToken string, stillCurrent func() bool) (bridgeAttachmentRef, error) {
	now := time.Now().UTC()
	connID := process.NewFencingID()
	ownerPID := os.Getpid()
	ownerStart, err := process.ProcessStartTime(ownerPID)
	if err != nil {
		// Without a verifiable owner fingerprint the record would be
		// malformed evidence (design/04) — no binding, no survival.
		s.log.Warnw("cannot fingerprint this process; bridge attachment not persisted", "gameId", gameID, "error", err)
		return bridgeAttachmentRef{}, errAttachmentSuperseded
	}
	lease := s.runtimeOwnerLeaseDuration()

	var launchID string
	promotedToActive := false
	_, terr := process.TransitionRuntimeStateWithCredit(gameID, s.configDir, lifecycleLockTimeout, func(st *process.RuntimeState) error {
		if st.SchemaVersion == 0 {
			// Legacy claims get attachment records only after their full
			// normalization (design/07).
			return errAttachmentSkipped
		}
		if st.Endpoint == nil {
			if st.NormalizedFromLegacy {
				// The migration touch: publication follows the fenced
				// endpoint persist in the connect handler.
				return errAttachmentSkipped
			}
			return errStaleAttachmentCredential
		}
		if st.Endpoint.Port != endpointPort || st.Endpoint.Token != endpointToken {
			return errStaleAttachmentCredential
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
		// Passive promotion only for completed-unobserved claims: an
		// in-flight start operation owns its own phase completion, and a
		// mid-spawn claim must not read as active (design/05 Stage 4).
		if st.Phase == process.PhaseStarting && st.Operation == nil && st.SpawnState == process.SpawnStateSpawned {
			st.Phase = process.PhaseActive
			st.Status = process.RuntimeStateStatusRunning
			promotedToActive = true
		}
		return nil
	}, func(st *process.RuntimeState) error {
		// Credit BEFORE the runtime save (round 14 F5), still under the
		// transition lock and fenced to this launch: recorded first and
		// idempotent, so a history-write failure aborts the attachment persist
		// (retry re-credits exactly once) and no counter is ever lost or
		// double-counted.
		if promotedToActive {
			// Stage 4 verification for a previously unobserved launch:
			// workloadStarts++ (launchID-idempotent).
			if err := s.applyPinnedWorkloadStart(gameID, st); err != nil {
				return err
			}
		}
		// bridgeConnects++ per credential-bound attachment (design/20),
		// idempotent by this attachment's connectionID.
		if st.HistoryContextHash != "" {
			return process.ApplyBridgeConnectLocked(gameID, s.configDir, process.EffectiveClaimProfile(st), st.HistoryContextHash, connID)
		}
		return nil
	})
	if terr != nil {
		if errors.Is(terr, errAttachmentSkipped) || errors.Is(terr, errStaleAttachmentCredential) {
			return bridgeAttachmentRef{}, terr
		}
		// The claim disappeared during the handshake, could not be read, or
		// the write failed: the connection has no binding and must not
		// survive (review round 8).
		if !errors.Is(terr, process.ErrNoRuntimeClaim) {
			s.log.Warnw("failed to persist bridge attachment", "gameId", gameID, "error", terr)
		}
		return bridgeAttachmentRef{}, errAttachmentSuperseded
	}

	ref := bridgeAttachmentRef{client: client, launchID: launchID, connectionID: connID}
	s.mu.Lock()
	if s.bridgeAttachments == nil {
		s.bridgeAttachments = make(map[string]bridgeAttachmentRef)
	}
	s.bridgeAttachments[gameID] = ref
	s.mu.Unlock()

	// A disconnect that fired before the record existed had nothing to
	// clear — verify the connection is still the current live one and, if
	// not, roll back EXACTLY the record just created: pop the in-memory
	// entry only if it is still ours and clear only our connectionID, so a
	// connection B that published meanwhile is never touched (design/06).
	if stillCurrent == nil || !stillCurrent() {
		s.mu.Lock()
		if cur, ok := s.bridgeAttachments[gameID]; ok && cur.connectionID == connID {
			delete(s.bridgeAttachments, gameID)
		}
		s.mu.Unlock()
		s.clearBridgeAttachment(gameID, launchID, connID)
		return bridgeAttachmentRef{}, errAttachmentSuperseded
	}
	if s.admitBackgroundTask() {
		go func() {
			defer s.bgWG.Done()
			s.refreshBridgeAttachmentLease(gameID, launchID, connID, stillCurrent, lease)
		}()
	}
	return ref, nil
}

// refreshBridgeAttachmentLease renews the persisted lease while the socket
// stays connected (design/04); it stops the moment the connection dies, the
// identity rotates, the fenced write is rejected, or the server shuts down
// (round 12 F4 — so it never writes runtime.json during TempDir teardown).
func (s *Server) refreshBridgeAttachmentLease(gameID, launchID, connID string, isConnected func() bool, lease time.Duration) {
	interval := lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.shutdownCh:
			return
		case <-ticker.C:
		}
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
	// The detach completion uses the no-create transition lock: a detach
	// racing directory teardown finds nothing and, crucially, never
	// recreates the game directory or lock file (review round 8).
	err := process.ClearAttachmentIfCurrent(gameID, s.configDir, launchID, connID, lifecycleLockTimeout)
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

// releaseGameArtifacts removes the in-memory controller, bridge client, and
// mirrored tools/resources for a finished launch — but only the exact
// instances the caller observed before the operation, never a successor's,
// and never the runtime claim (its removal is the fenced completion's
// business). A nil expected instance means none existed at observation
// time, and whatever exists now belongs to someone else.
func (s *Server) releaseGameArtifacts(gameID string, expectedController process.ControllerInterface, expectedClient *gabp.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedController != nil && s.games[gameID] == expectedController {
		delete(s.games, gameID)
	}
	if expectedClient != nil && s.gabpClients[gameID] == expectedClient {
		s.cleanupGABPConnectionInternal(gameID)
		s.cleanupGameResourcesInternal(gameID)
	}
}

// lifecycleActionResult routes games_stop/games_kill through the design/06
// pipeline. A pre-profile claim is fully normalized first — stop/kill are
// lifecycle touches (design/07). It returns nil only when no claim exists
// at all (the in-memory legacy path handles that).
func (s *Server) lifecycleActionResult(game config.GameConfig, action, configRevision string) *ToolResult {
	claim, err := process.LoadRuntimeState(game.ID, s.configDir)
	if err != nil {
		// An unreadable claim is a GABS-side state situation to resolve first
		// (round 13 F2: authorized code blocked_unknown_state; dispatch fills
		// causeClass/track record/next actions).
		return &ToolResult{
			Content:           []Content{{Type: "text", Text: fmt.Sprintf("The runtime claim for '%s' is unreadable: %v. Inspect it, or use 'gabs games repair %s --forget-runtime' if the game is provably gone.", game.ID, err, game.ID)}},
			IsError:           true,
			StructuredContent: map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID, "action": action},
		}
	}
	if claim == nil {
		return nil
	}
	if claim.SchemaVersion == 0 {
		normalized, nerr := process.NormalizeLegacyClaim(game.ID, s.configDir, game.LaunchMode, configRevision)
		if nerr != nil {
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("The pre-upgrade runtime claim for '%s' could not be normalized: %v. Retry, or use 'gabs games repair %s --forget-runtime' if the game is provably gone.", game.ID, nerr, game.ID)}},
				IsError:           true,
				StructuredContent: map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID, "action": action},
			}
		}
		claim = normalized
	}

	// Capture the exact in-memory instances that belong to the launch being
	// stopped: post-completion cleanup is identity-tied, never keyed by
	// gameID alone — a successor's controller or client must survive.
	s.mu.RLock()
	priorController := s.games[game.ID]
	priorClient := s.gabpClients[game.ID]
	s.mu.RUnlock()

	// The claim's PINNED track-record coordinates (never recomputed from
	// hot config) so a verified stop / failed action credits THIS launch
	// (design/20; round 10).
	stopProfile := process.EffectiveClaimProfile(claim)
	stopHash := claim.HistoryContextHash

	outcome, refusal, err := process.ExecuteStopAction(process.StopRequest{
		GameID:             game.ID,
		ConfigDir:          s.configDir,
		InstanceID:         s.instanceID,
		Action:             action,
		HistoryProfile:     stopProfile,
		HistoryContextHash: stopHash,
		GABPLive:           func() bool { return s.hasLiveGABPClient(game.ID) },
		SelfConnection:     s.selfConnectionFor(game.ID, claim.LaunchID),
		ReapLauncher: func() {
			if priorController != nil {
				priorController.TerminateDirectChild()
			}
		},
	})
	if err != nil {
		// An internal execution error (lock/persistence/system failure) leaves
		// GABS runtime state unresolved — the authorized state-class code is
		// blocked_unknown_state (round 12 F3; not an invented code). It carries
		// attribution with the pinned context's track record; no history write.
		structured := map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID, "action": action}
		s.attachStructuredFailureAttribution(structured, game, "blocked_unknown_state",
			historyContext{profile: stopProfile, contextHash: stopHash})
		return &ToolResult{
			Content:           []Content{{Type: "text", Text: fmt.Sprintf("Failed to %s '%s': %v", action, game.ID, err)}},
			IsError:           true,
			StructuredContent: structured,
		}
	}
	if refusal != nil {
		return s.stopRefusalResult(game, action, stopProfile, stopHash, refusal)
	}

	if outcome.Code == process.OutcomeTerminated && outcome.ClaimRemoved {
		// The fenced removal held to the end: release exactly the observed
		// instances. cleanStops++ was recorded under the removal lock. A
		// superseded completion (ClaimRemoved false) touches nothing.
		s.releaseGameArtifacts(game.ID, priorController, priorClient)
	}
	return s.stopOutcomeResult(game, action, stopProfile, stopHash, outcome)
}

func (s *Server) stopRefusalResult(game config.GameConfig, action string, profile, contextHash string, ref *process.StopRefusal) *ToolResult {
	structured := map[string]interface{}{
		"code":   ref.Code,
		"gameId": game.ID,
		"action": action,
	}
	if class := process.Classify(ref.Code, process.ClassifyContext{}).Class; class != "" {
		structured["causeClass"] = class
	}
	s.attachStopTrackRecord(structured, game.ID, profile, contextHash)
	if ref.Phase != "" {
		structured["phase"] = ref.Phase
	}
	if ref.ActiveProfile != "" {
		structured["activeProfile"] = ref.ActiveProfile
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

func (s *Server) stopOutcomeResult(game config.GameConfig, action string, profile, contextHash string, outcome *process.StopOutcome) *ToolResult {
	structured := map[string]interface{}{
		"code":         outcome.Code,
		"gameId":       game.ID,
		"action":       action,
		"claimRemoved": outcome.ClaimRemoved,
	}
	// A clean success (terminated) carries NO failure cause (round 11 P2-6):
	// Classify returns an empty class for it, and we omit the field entirely.
	if class := process.Classify(outcome.Code, process.ClassifyContext{}).Class; class != "" {
		structured["causeClass"] = class
	}
	s.attachStopTrackRecord(structured, game.ID, profile, contextHash)
	if outcome.ActiveProfile != "" {
		structured["activeProfile"] = outcome.ActiveProfile
	}
	if outcome.FinalPhase != "" {
		structured["phase"] = outcome.FinalPhase
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

// attachClaimIdentity loads the current claim and surfaces its lifecycle
// facts — activeProfile, phase, operation, lastActionResult — on a tool
// result (design/10: games_connect reports activeProfile and phase).
func (s *Server) attachClaimIdentity(structured map[string]interface{}, gameID string) {
	rs, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil || rs == nil {
		return
	}
	attachRuntimeLifecycle(structured, rs)
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
	if profile := process.EffectiveClaimProfile(rs); profile != "" {
		statusItem["activeProfile"] = profile
	}
	if op := rs.Operation; op != nil {
		statusItem["operation"] = map[string]interface{}{
			"action":           op.Action,
			"attemptStartedAt": op.AttemptStartedAt.Format(time.RFC3339),
			"deadline":         op.Deadline.Format(time.RFC3339),
		}
	}
	if cd := rs.ContextDelivery; cd != nil {
		entry := map[string]interface{}{"overall": cd.Overall}
		if len(cd.Channels) > 0 {
			entry["channels"] = cd.Channels
		}
		if len(cd.Reasons) > 0 {
			entry["reasons"] = cd.Reasons
		}
		statusItem["contextDelivery"] = entry
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

// staleBridgeCredentialResult renders the stable stale_bridge_credential
// outcome (design/10): the observed bridge belongs to a previous launch
// environment and must never be attached to.
func staleBridgeCredentialResult(gameID, message string) *ToolResult {
	return &ToolResult{
		Content: []Content{{Type: "text", Text: message}},
		StructuredContent: map[string]interface{}{
			"code":   "stale_bridge_credential",
			"gameId": gameID,
			"nextActions": []map[string]interface{}{
				mcpNextAction("games_status", map[string]interface{}{"gameId": gameID}, "Inspect the running instance; it carries an earlier launch's bridge environment."),
				mcpNextAction("games_stop", map[string]interface{}{"gameId": gameID}, "Stop the stale instance, then start it again to receive the new bridge environment."),
			},
		},
		IsError: true,
	}
}

// attachStatusEvidence renders the liveness observation behind a status
// verdict — verdict, source, detail, hook facts, warnings — so unknown says
// what was observed and contradictions surface (design/04; T-LIFE).
func attachStatusEvidence(statusItem map[string]interface{}, ev *process.LivenessEvidence) {
	if ev == nil {
		return
	}
	entry := map[string]interface{}{
		"verdict": ev.Verdict,
		"source":  ev.Source,
	}
	if ev.Detail != "" {
		entry["detail"] = ev.Detail
	}
	if hr := ev.HookResult; hr != nil {
		hook := map[string]interface{}{"exitCode": hr.ExitCode}
		if hr.TimedOut {
			hook["timedOut"] = true
		}
		if hr.ExecError != nil {
			hook["execError"] = hr.ExecError.Error()
		}
		if hr.StderrTail != "" {
			hook["stderrTail"] = hr.StderrTail
		}
		entry["statusHook"] = hook
	}
	statusItem["evidence"] = entry
	if len(ev.Warnings) > 0 {
		statusItem["statusWarnings"] = ev.Warnings
	}
}

// historyContext carries everything the track-record store needs for one
// launch: the launch identity (for fencing), the input-free context hash,
// the COMPLETE last-known-good snapshot, and the supplied-input bucket
// coordinates (design/08). launchID is filled once the claim is created.
type historyContext struct {
	launchID    string
	profile     string
	contextHash string
	snapshot    process.ContextSnapshot
	bucket      process.SuccessBucket
	inputNames  []string
}

// buildHistoryContext composes the track-record coordinates for a launch.
// The context hash comes from the resolver's input-free base context (never
// the post-input Resolved, never a second merge); the value digest is keyed
// with the per-game bucket key so supplied values never persist in the
// clear (design/08, design/20).
// computeHistoryContext derives the input-free context coordinates and the
// bucket IDENTITY with ZERO history mutation (round 11 P1-1/P1-2): the hash,
// the last-good snapshot, and the per-input declaration identity. It performs
// NO key creation and NO bucket invalidation, so it is safe on a pre-accept
// failure path (launch_spec_unresolvable, resolver/call-class errors) where a
// caller typo must not persist a per-game bucket key or drop buckets. The
// value digest is left empty — it needs the per-game key and belongs only to
// the accepted-start path.
func (s *Server) computeHistoryContext(snap *config.Snapshot, game config.GameConfig, resolved *launch.Resolved, inputs map[string]interface{}) historyContext {
	profile := ""
	var inputNames []string
	if resolved != nil {
		profile = resolved.Profile
		inputNames = append([]string(nil), resolved.AppliedInputs...)
	}
	hc := historyContext{profile: profile, inputNames: inputNames}

	base, berr := launch.ResolveBaseContext(snap, game.ID, profile, launch.Options{
		InheritedEnv:       os.Environ(),
		CaseInsensitiveEnv: runtime.GOOS == "windows",
	})
	if berr == nil {
		hc.contextHash = process.ContextHash(base)
		hc.snapshot = process.ContextSnapshot{
			Target:         base.Target,
			Mode:           base.Mode,
			Args:           base.Args,
			ConfigEnv:      base.ConfigEnv,
			AbsentEnvNames: base.AbsentEnvNames,
			WorkingDir:     base.WorkingDir,
			Lifecycle:      base.Lifecycle,
		}
	}

	if len(inputNames) > 0 {
		hc.bucket.InputNames = inputNames
		hc.bucket.DeclHash = process.InputDeclHash(game, inputNames)
		hc.bucket.PerInputDecl = perInputDeclHashes(game, inputNames)
		// Compute the value digest READ-ONLY when a bucket key already exists
		// (round 12 F8), so a pre-accept failure (launch_spec_unresolvable) on
		// a previously proven exact input combination is recognized as proven
		// rather than always "first run". If no key exists, the combination is
		// genuinely unproven and the digest stays empty — no key is minted.
		if key := process.BucketKeyIfExists(game.ID, s.configDir); key != "" {
			hc.bucket.ValueDigest = process.BucketValueDigest(key, appliedInputValues(inputNames, inputs))
		}
	}
	return hc
}

// appliedInputValues maps supplied input names to their string values for the
// per-game-keyed value digest (values never persist in the clear).
func appliedInputValues(inputNames []string, inputs map[string]interface{}) map[string]string {
	applied := map[string]string{}
	for _, n := range inputNames {
		if v, ok := inputs[n]; ok {
			applied[n] = fmt.Sprintf("%v", v)
		}
	}
	return applied
}

// buildHistoryContext is the ACCEPTED-START context: the pure coordinates plus
// the two mutations only a resolved, accepted attempt may perform — dropping
// buckets whose input declaration changed (reload safety) and creating/reading
// the per-game bucket key to compute this launch's value digest.
func (s *Server) buildHistoryContext(snap *config.Snapshot, game config.GameConfig, resolved *launch.Resolved, inputs map[string]interface{}) historyContext {
	hc := s.computeHistoryContext(snap, game, resolved, inputs)

	// Reload safety (design/08): drop any input-combination bucket whose
	// per-input declaration changed — or was REMOVED — since it was recorded;
	// a value that "worked" under an old declaration is not proof under the
	// new one. Runs even when every input was removed (empty current map), so
	// a re-added declaration never resurrects stale proof (round 11 P2-4).
	currentDecls := make(map[string]string, len(game.LaunchInputs))
	for name := range game.LaunchInputs {
		currentDecls[name] = process.InputDeclHash(game, []string{name})
	}
	if err := process.InvalidateChangedInputDeclarations(game.ID, s.configDir, hc.profile, currentDecls); err != nil {
		s.log.Warnw("failed to invalidate changed input declarations", "gameId", game.ID, "error", err)
	}

	if len(hc.inputNames) > 0 {
		if key, err := process.EnsureBucketKey(game.ID, s.configDir); err == nil {
			hc.bucket.ValueDigest = process.BucketValueDigest(key, appliedInputValues(hc.inputNames, inputs))
		}
	}
	return hc
}

func perInputDeclHashes(game config.GameConfig, names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		out[n] = process.InputDeclHash(game, []string{n})
	}
	return out
}

// applyPinnedWorkloadStart records this launch's Stage 4 verified start from
// the identity PINNED in the claim (round 11 P1-2), using an already-held
// runtime-state transition lock — so every promotion path (synchronous start,
// passive status observation, attachment, recovery) credits the start exactly
// once, atomically with the phase flip, from the claim alone. No-op for a
// claim without a pinned identity (legacy/external claims). The caller MUST
// invoke this only when the transition actually flips starting→active.
func (s *Server) applyPinnedWorkloadStart(gameID string, st *process.RuntimeState) error {
	return process.ApplyPinnedWorkloadStartLocked(gameID, s.configDir, st, time.Now().UTC())
}

// recordTerminalStartFailure writes a terminal accepted-attempt failure to
// history while the claim is still alive (called from startGame before the
// deferred claim release), fenced to the launch (design/08, design/20;
// round 10). Returns the classification so the caller can carry it to the
// render step. Only design-eligible codes write; the pure classification is
// always returned for rendering.
func (s *Server) recordTerminalStartFailure(game config.GameConfig, hc historyContext, code string) process.Classification {
	cls := process.Classify(code, process.ClassifyContext{
		Proven:                s.contextProven(game.ID, hc),
		InputCombinationFresh: !s.inputComboProven(game.ID, hc),
		SuppliedInputs:        hc.inputNames,
	})
	if writeEligibleStartFailure(code) && hc.contextHash != "" && hc.launchID != "" {
		if err := process.RecordFailure(game.ID, s.configDir, hc.launchID, hc.profile, hc.contextHash, code, cls.Class, hc.inputNames, time.Now().UTC()); err != nil {
			s.log.Warnw("failed to record start failure in history", "gameId", game.ID, "error", err)
		}
	}
	return cls
}

// finalizeStartFailure RENDERS a start failure's attribution — causeClass,
// track record, class-keyed actions, edit notice — reading the history the
// record step (in startGame, while the claim was alive) already wrote. It
// never mutates history (round 10: the write and render are split by the
// claim lifetime).
func (s *Server) finalizeStartFailure(structured map[string]interface{}, game config.GameConfig, hc historyContext, code string) {
	cls := process.Classify(code, process.ClassifyContext{
		Proven:                s.contextProven(game.ID, hc),
		InputCombinationFresh: !s.inputComboProven(game.ID, hc),
		SuppliedInputs:        hc.inputNames,
	})
	s.attachFailureAttribution(structured, game, hc, cls.Class, cls.SecondaryNote)
}

// writeEligibleStartFailure reports whether a start-failure code is a
// terminal failure of an accepted attempt with a resolved context — the
// only failures that mutate history (design/08, design/20). Pre-accept
// codes (call-class, config_invalid, Stage 2 refusals) render but never
// write.
func writeEligibleStartFailure(code string) bool {
	switch code {
	case "exited_during_start", "spawn_failed", "endpoint_unavailable", "spec_too_large":
		return true
	default:
		return false
	}
}

// coreManagementTools are the GABS core game-management tools — the ones that
// return the stable-code failures the attribution contract governs (design/20:
// 220). Mirrored game/GABP tools are deliberately excluded: their result's
// `code` field is game-defined and must not be attributed (round 13 F2). Both
// the dotted and strict-safe spellings are listed since either reaches dispatch.
var coreManagementTools = map[string]bool{
	"games.list": true, "games_list": true,
	"games.show": true, "games_show": true,
	"games.status": true, "games_status": true,
	"games.start": true, "games_start": true,
	"games.stop": true, "games_stop": true,
	"games.kill": true, "games_kill": true,
	"games.connect": true, "games_connect": true,
	"games.tools": true, "games_tools": true,
	"games.tool_names": true, "games_tool_names": true,
	"games.tool_detail": true, "games_tool_detail": true,
	"games.call_tool": true, "games_call_tool": true,
	"games.get_attention": true, "games_get_attention": true,
	"games.ack_attention": true, "games_ack_attention": true,
}

// completeFailureAttribution is the single central completion step applied at
// dispatch to EVERY core-management ToolResult (round 13 F2): any stable
// FAILURE result gets causeClass, a neutral track-record line, and class-keyed
// next actions — INDEPENDENTLY, so a partially-attributed result (e.g. one that
// set causeClass at its branch but no track line) cannot escape. Branches that
// already filled a field keep it (proof-adjusted attribution wins). Success/
// pending and unmapped codes (empty class) get nothing. This is what closes the
// "every failure carries attribution" contract across games_status/show/list
// and the codeless generic branches (now code-bearing), not just the four
// lifecycle tools.
func (s *Server) completeFailureAttribution(toolName string, result *ToolResult) {
	if result == nil || result.StructuredContent == nil || !coreManagementTools[toolName] {
		return
	}
	code, _ := result.StructuredContent["code"].(string)
	if code == "" {
		return
	}
	class := process.Classify(code, process.ClassifyContext{}).Class
	if class == "" {
		return // success / pending / unmapped — no failure cause
	}
	if _, has := result.StructuredContent["causeClass"]; !has {
		result.StructuredContent["causeClass"] = class
	}
	if _, has := result.StructuredContent["trackRecord"]; !has {
		result.StructuredContent["trackRecord"] = process.TrackRecordLine(nil)
	}
	if _, has := result.StructuredContent["nextActions"]; !has {
		gameID, _ := result.StructuredContent["gameId"].(string)
		result.StructuredContent["nextActions"] = failureNextActions(gameID, class)
	}
}

// attachStructuredFailureAttribution is the SINGLE mandatory read-only
// attribution path for every structured failure result (design/20: "every
// failure result gets causeClass and one track-record line"; round 11 P1-1).
// It classifies the code — proof-adjusted when a resolved context is present
// (so a target that vanished after 14 successful starts is environment, not
// unclassified) — attaches causeClass, a track-record line and edit notice
// WHEN a context exists, and class-keyed next actions. It NEVER mutates
// history: a call-class or config error reads proof but a caller typo must
// not distort it. A zero hc (pre-resolution errors — unknown_argument,
// timeout_out_of_range) degrades to the code's class with no track line.
func (s *Server) attachStructuredFailureAttribution(structured map[string]interface{}, game config.GameConfig, code string, hc historyContext) {
	cls := process.Classify(code, process.ClassifyContext{
		Proven:                hc.contextHash != "" && s.contextProven(game.ID, hc),
		InputCombinationFresh: hc.contextHash != "" && !s.inputComboProven(game.ID, hc),
		SuppliedInputs:        hc.inputNames,
	})
	s.attachFailureAttribution(structured, game, hc, cls.Class, cls.SecondaryNote)
}

// attachFailureAttribution renders a failure result's track-record fields
// (design/08): causeClass, the candidate-input secondary note, class-keyed
// next actions (a non-config class NEVER proposes a config edit), and ALWAYS a
// one-line track record. Per design/08 §"call-class" the response still
// carries track-record evidence even for a pre-resolution error: with no
// resolved context, TrackRecordLine(nil) renders the neutral "no successful
// starts recorded for this context" form (round 12 F2). The once-per-edit
// visibility notice only applies to a resolved context.
func (s *Server) attachFailureAttribution(structured map[string]interface{}, game config.GameConfig, hc historyContext, class, secondaryNote string) {
	structured["causeClass"] = class
	if secondaryNote != "" {
		structured["candidateInputNote"] = secondaryNote
	}
	var entry *process.HistoryEntry
	if hc.contextHash != "" {
		if h, err := process.LoadHistory(game.ID, s.configDir); err == nil {
			if e := h.Profiles[hc.profile]; e != nil && e.ContextHash == hc.contextHash {
				entry = e
			}
		}
		if notice := s.editNoticeFor(game.ID, hc); notice != "" {
			structured["editNotice"] = notice
		}
	}
	// entry is nil for a never-proven or contextless failure — TrackRecordLine
	// renders the neutral line either way, so every failure carries evidence.
	structured["trackRecord"] = process.TrackRecordLine(entry)
	structured["nextActions"] = failureNextActions(game.ID, class)
}

// failureNextActions returns class-keyed next actions (design/08). Only the
// config class may suggest editing configuration; every other class routes
// to status/retry/report so an agent never "fixes" settings that were never
// broken.
func failureNextActions(gameID, class string) []map[string]interface{} {
	gameArg := map[string]interface{}{"gameId": gameID}
	switch class {
	case process.CauseConfig:
		return []map[string]interface{}{
			mcpNextAction("games_show", gameArg, "Review and correct the launch configuration; the result names the offending field."),
		}
	case process.CauseCall:
		return []map[string]interface{}{
			mcpNextAction("games_show", gameArg, "Check the accepted profiles and declared inputs, then reissue the call correctly."),
		}
	case process.CauseGame:
		return []map[string]interface{}{
			mcpNextAction("games_status", gameArg, "Inspect the workload; the output tail shows why it exited. This is game-side, not launch config."),
			mcpNextAction("games_start", gameArg, "Retry once the game-side cause (save, mod, login) is addressed."),
		}
	case process.CauseEnvironment:
		return []map[string]interface{}{
			mcpNextAction("games_status", gameArg, "Check host/store/network state (Steam running, daemon up, network reachable) — this is an environment problem, not a launch-settings one."),
			mcpNextAction("games_start", gameArg, "Retry after the environment recovers."),
		}
	default: // state
		return []map[string]interface{}{
			mcpNextAction("games_status", gameArg, "Resolve the runtime state first (already running, in-flight operation, or unverified termination)."),
		}
	}
}

// editNoticeFor returns the one-line edit-visibility notice for this game's
// current context, or "" — firing once per edit (design/08).
func (s *Server) editNoticeFor(gameID string, hc historyContext) string {
	notice, err := process.EditNotice(gameID, s.configDir, hc.profile, hc.contextHash)
	if err != nil {
		return ""
	}
	return notice
}

func (s *Server) contextProven(gameID string, hc historyContext) bool {
	h, err := process.LoadHistory(gameID, s.configDir)
	if err != nil {
		return false
	}
	e := h.Profiles[hc.profile]
	return e != nil && e.ContextHash == hc.contextHash && e.WorkloadStarts > 0
}

func (s *Server) inputComboProven(gameID string, hc historyContext) bool {
	h, err := process.LoadHistory(gameID, s.configDir)
	if err != nil {
		return false
	}
	e := h.Profiles[hc.profile]
	if e == nil || e.ContextHash != hc.contextHash {
		return false
	}
	return e.HasBucket(hc.bucket.DeclHash, hc.bucket.ValueDigest)
}

// computeSpawnDigests pins the expected launch context from the fully
// materialized spawn state (design/03): the argv payload is the resolved
// argument list (argv[0] excluded by construction), the cwd is the
// effective working directory, and the env values are exactly the names
// the wrapper contract forwards (GABS_FORWARD_ENV, falling back to the
// managed GABS_*/GABP_* variables for legacy specs).
func computeSpawnDigests(spec process.LaunchSpec, controller process.ControllerInterface) *process.RuntimeContextDigests {
	finalEnv := map[string]string{}
	for _, kv := range controller.FinalEnvironment() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			finalEnv[kv[:i]] = kv[i+1:]
		}
	}

	// Channel membership is decided HERE, from the resolved spec — the
	// config-declared context keys are the contextEnv channel; every other
	// forwarded name (GABS_*/GABP_*, SteamAppId/SteamGameId, SystemRoot)
	// is the managed layer (review round 9: prefix guessing is not a
	// persistable contract).
	contextKeys := map[string]bool{}
	for _, k := range spec.ContextEnvKeys {
		contextKeys[k] = true
	}
	managedEnv := map[string]string{}
	contextEnv := map[string]string{}
	classify := func(n, v string) {
		if contextKeys[n] {
			contextEnv[n] = v
		} else {
			managedEnv[n] = v
		}
	}
	if names := strings.TrimSpace(finalEnv["GABS_FORWARD_ENV"]); names != "" {
		for _, n := range strings.Split(names, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if v, ok := finalEnv[n]; ok {
				classify(n, v)
			}
		}
	} else {
		for k, v := range finalEnv {
			if strings.HasPrefix(k, "GABS_") || strings.HasPrefix(k, "GABP_") {
				classify(k, v)
			}
		}
	}

	var absent []string
	if names := strings.TrimSpace(finalEnv["GABS_ABSENT_ENV"]); names != "" {
		for _, n := range strings.Split(names, ",") {
			if n = strings.TrimSpace(n); n != "" {
				absent = append(absent, n)
			}
		}
	}

	cwd := spec.WorkingDir
	unverifiable := false
	switch {
	case cwd == "":
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		} else {
			unverifiable = true
		}
	case !filepath.IsAbs(cwd):
		// The legacy relative workingDir: incomparable by contract
		// (design/03) — unverifiable, never a guessed digest.
		unverifiable = true
	}

	digests, err := process.ComputeContextDigests(spec.Args, cwd, unverifiable, managedEnv, contextEnv, absent)
	if err != nil {
		return nil
	}
	return digests
}

// recordContextDelivery evaluates the welcome-time observation against the
// spawn-pinned digests and persists the verdict, fenced by the launch AND
// the current connection identity (design/06: delivery callbacks validate
// launchID + connectionID) — a report from an old connection can never
// stamp a verdict onto a newer launch. A nil observation is itself an
// observation: an old bridge yields overall unknown, persisted.
// recordContextDelivery persists the delivery verdict under EXACTLY the
// connection that produced the report: the caller passes the publication
// result, never a reacquired "current" reference — a report from
// connection A must not be persisted under a connection B that published
// meanwhile (review round 9).
func (s *Server) recordContextDelivery(gameID string, ref bridgeAttachmentRef, obs *gabp.ObservedContext) {
	if ref.launchID == "" || ref.connectionID == "" {
		return
	}
	var pobs *process.ObservedContext
	if obs != nil {
		pobs = &process.ObservedContext{Argv: obs.Argv, Cwd: obs.Cwd, EnvValues: obs.EnvValues, EnvAbsent: obs.EnvAbsent}
	}
	// deliveriesVerified++ only on a fully verified delivery (design/20),
	// recorded INSIDE the same fenced transition (launchID + connectionID)
	// that persists the verdict — a stale callback can never bump a
	// successor's history (round 10). The history coordinates come from the
	// claim's PINNED hash, never recomputed from hot config.
	verified := false
	if _, err := process.FencedTransitionWithCredit(gameID, s.configDir, ref.launchID, "", func(st *process.RuntimeState) error {
		if st.Attachment == nil || st.Attachment.ConnectionID != ref.connectionID {
			return process.ErrFencingViolation
		}
		delivery := process.EvaluateContextDelivery(st.ContextDigests, pobs)
		st.ContextDelivery = delivery
		verified = delivery.Overall == process.DeliveryVerified
		return nil
	}, func(st *process.RuntimeState) error {
		// deliveriesVerified++ recorded BEFORE the verdict commits to
		// runtime.json (round 14 F5), idempotent by this attachment's
		// connectionID, so a history-write failure aborts the verdict persist
		// and a retry credits exactly once — the delivery event is never lost.
		if verified && st.HistoryContextHash != "" {
			return process.ApplyDeliveryVerifiedLocked(gameID, s.configDir, process.EffectiveClaimProfile(st), st.HistoryContextHash, ref.connectionID)
		}
		return nil
	}); err != nil && !errors.Is(err, process.ErrFencingViolation) && !errors.Is(err, process.ErrNoRuntimeClaim) {
		s.log.Warnw("failed to persist context delivery verdict", "gameId", gameID, "error", err)
	}
}

// supersededStartRefusal re-evaluates the CURRENT claim after a start lost
// its fence and returns the applicable existing stable outcome (design/10;
// review round 9): a successor in flight is operation_in_progress, an
// active successor is already_running, anything else is
// blocked_unknown_state — never an unclassified error.
func (s *Server) supersededStartRefusal(gameID string) error {
	cur, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil || cur == nil {
		return &startRefusalError{refusal: &process.StartRefusal{
			Code:    process.RefusalOperationInFlight,
			Message: fmt.Sprintf("the launch of '%s' was superseded during startup and the successor has since finished; re-check games_status", gameID),
		}}
	}
	if process.OperationInFlight(cur.Operation, time.Now().UTC()) {
		op := *cur.Operation
		return &startRefusalError{refusal: &process.StartRefusal{
			Code:          process.RefusalOperationInFlight,
			Message:       fmt.Sprintf("the launch of '%s' was superseded during startup; a successor %s operation is in progress (deadline %s)", gameID, op.Action, op.Deadline.Format(time.RFC3339)),
			Phase:         cur.Phase,
			ActiveProfile: process.EffectiveClaimProfile(cur),
			Operation:     &op,
		}}
	}
	if cur.Phase == process.PhaseActive {
		return &startRefusalError{refusal: &process.StartRefusal{
			Code:          process.RefusalAlreadyRunning,
			Message:       fmt.Sprintf("the launch of '%s' was superseded during startup; a successor launch is active", gameID),
			Phase:         cur.Phase,
			ActiveProfile: process.EffectiveClaimProfile(cur),
		}}
	}
	return &startRefusalError{refusal: &process.StartRefusal{
		Code:    process.RefusalBlockedUnknown,
		Message: fmt.Sprintf("the launch of '%s' was superseded during startup; a successor claim exists in phase %s — re-check games_status", gameID, cur.Phase),
		Phase:   cur.Phase,
	}}
}

// occupiedClaimRefusal is the stable outcome for a Stage 4 persistence
// failure: the claim remains occupied (the operation stays in place) and
// uncertainty blocks — blocked_unknown_state, per the exhaustive
// terminal-branch rule (design/10).
func occupiedClaimRefusal(gameID, what string, err error) error {
	return &startRefusalError{refusal: &process.StartRefusal{
		Code:    process.RefusalBlockedUnknown,
		Message: fmt.Sprintf("%s for '%s': %v — the claim remains occupied; re-check games_status and retry", what, gameID, err),
	}}
}

// attachStartContextDelivery reloads the exact claim and attaches its
// persisted delivery verdict to a start result (design/10: games_start
// carries contextDelivery when applicable). Only a synchronous successful
// connection has produced a verdict by this point; otherwise there is
// nothing to attach yet and status renders it later.
func (s *Server) attachStartContextDelivery(structured map[string]interface{}, gameID string) {
	claim, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil || claim == nil || claim.ContextDelivery == nil {
		return
	}
	cd := claim.ContextDelivery
	entry := map[string]interface{}{"overall": cd.Overall}
	if len(cd.Channels) > 0 {
		entry["channels"] = cd.Channels
	}
	if len(cd.Reasons) > 0 {
		entry["reasons"] = cd.Reasons
	}
	structured["contextDelivery"] = entry
}

// buildTrackRecordSummary returns the per-profile proof and counters for
// games_show (design/08; round 10 P2-12). For each launchable context —
// the default context plus every named profile — it reports whether the
// CURRENT resolved context is proven, the split counters, and whether the
// recorded proof belongs to a now-superseded context (a config edit). An
// edited context reads never-proven even when its predecessor succeeded.
func (s *Server) buildTrackRecordSummary(snap *config.Snapshot, game config.GameConfig) []map[string]interface{} {
	h, err := process.LoadHistory(game.ID, s.configDir)
	if err != nil || h == nil {
		return nil
	}
	// History is keyed under the RESOLVED profile name. A game with named
	// profiles never records under "" (a no-profile start resolves to the
	// default profile), so listing "" for such a game would show a permanent
	// never-proven row that is pure noise — report one row per named profile
	// instead, and only the bare "" context for a game with no profiles.
	var profiles []string
	if len(game.Profiles) > 0 {
		for name := range game.Profiles {
			profiles = append(profiles, name)
		}
	} else {
		profiles = []string{""}
	}
	sort.Strings(profiles)

	out := make([]map[string]interface{}, 0, len(profiles))
	for _, profile := range profiles {
		entry := h.Profiles[profile]
		// The context the NEXT start of this profile would run — used to
		// decide whether the recorded proof still applies.
		currentHash := ""
		if base, berr := launch.ResolveBaseContext(snap, game.ID, profile, launch.Options{
			InheritedEnv:       os.Environ(),
			CaseInsensitiveEnv: runtime.GOOS == "windows",
		}); berr == nil {
			currentHash = process.ContextHash(base)
		}

		row := map[string]interface{}{"profile": profile}
		if profile == "" {
			row["profile"] = "(default)"
		}
		if profile == game.DefaultProfile && len(game.Profiles) > 0 {
			row["isDefault"] = true
		}
		appliesToCurrent := entry != nil && currentHash != "" && entry.ContextHash == currentHash
		row["currentContextProven"] = appliesToCurrent && entry.WorkloadStarts > 0
		row["contextChangedSinceLastRecord"] = entry != nil && currentHash != "" && entry.ContextHash != currentHash
		if entry != nil {
			row["workloadStarts"] = entry.WorkloadStarts
			row["bridgeConnects"] = entry.BridgeConnects
			row["deliveriesVerified"] = entry.DeliveriesVerified
			row["cleanStops"] = entry.CleanStops
			row["consecutiveFailures"] = entry.ConsecutiveFailures
			if entry.LastFailure != nil {
				row["lastFailureClass"] = entry.LastFailure.Class
				row["lastFailureCode"] = entry.LastFailure.Outcome
			}
			row["trackRecord"] = process.TrackRecordLine(entry)
		} else {
			row["trackRecord"] = process.TrackRecordLine(nil)
		}
		out = append(out, row)
	}
	return out
}

// attachStopTrackRecord renders the track-record line for a stop/kill
// result. A verified termination clears the claim (and often the history
// entry stays), so the line reflects whatever proof the context still holds
// — never mutating it here (design/08). It ALWAYS renders a line: the neutral
// "no successful starts" form when the pinned context has no entry (round 12
// F2), so every stop/kill failure carries track-record evidence.
func (s *Server) attachStopTrackRecord(structured map[string]interface{}, gameID, profile, contextHash string) {
	var entry *process.HistoryEntry
	if contextHash != "" {
		if h, err := process.LoadHistory(gameID, s.configDir); err == nil {
			if e := h.Profiles[profile]; e != nil && e.ContextHash == contextHash {
				entry = e
			}
		}
	}
	structured["trackRecord"] = process.TrackRecordLine(entry)
}
