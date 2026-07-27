package lifecycle

import (
	"fmt"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
)

// StartRefusalError wraps a structured Stage 2 refusal (design/05). Both
// frontends read Refusal.Code/Message and render their own next actions.
type StartRefusalError struct {
	Refusal  *process.StartRefusal
	Warnings []string
}

func (e *StartRefusalError) Error() string { return e.Refusal.Message }

// UnobservedStartError is the Stage 4 unobserved outcome: the claim is kept in
// phase starting; not a failure to retry blindly.
type UnobservedStartError struct {
	Warnings []string
}

func (e *UnobservedStartError) Error() string {
	return "nothing observable within the process-start budget"
}

// ExitedDuringStartError carries the Stage 4 exit evidence.
type ExitedDuringStartError struct {
	ExitCode            int
	Tail                string
	HookEvidence        string
	Warnings            []string
	HookReportedStopped bool
}

func (e *ExitedDuringStartError) Error() string {
	return fmt.Sprintf("exited during start (exit code %d)", e.ExitCode)
}

// StartAttemptError wraps an accepted-attempt failure with the warnings the
// attempt had already earned (unprobeable-profile evidence, the Steam client
// advisory), so neither frontend loses them when the attempt fails before the
// unobserved/exited branches — those carry warnings natively. Unwrap keeps the
// underlying classification (errors.As/Is) intact.
type StartAttemptError struct {
	Err      error
	Warnings []string
}

func (e *StartAttemptError) Error() string { return e.Err.Error() }
func (e *StartAttemptError) Unwrap() error { return e.Err }

// EndpointUnavailableError is the structured Stage 2 endpoint-allocation
// failure (design/05): port exhaustion, filesystem failure, occupied cache.
type EndpointUnavailableError struct {
	GameID string
	Err    error
}

func (e *EndpointUnavailableError) Error() string {
	return fmt.Sprintf("failed to prepare GABS endpoint for game '%s': %v", e.GameID, e.Err)
}

func (e *EndpointUnavailableError) Unwrap() error { return e.Err }

// GameAlreadyActiveError is the in-process fast-path refusal: this server
// already tracks a running controller for the game (never reached by a one-shot
// CLI, whose in-process registry is empty).
type GameAlreadyActiveError struct {
	Status string
}

func (e *GameAlreadyActiveError) Error() string {
	switch e.Status {
	case process.RuntimeStateStatusStarting:
		return "game launch is already in progress"
	default:
		return "game is already running"
	}
}

// ToolMessage renders the caller-facing message for an already-active refusal.
func (e *GameAlreadyActiveError) ToolMessage(game config.GameConfig) string {
	switch e.Status {
	case process.RuntimeStateStatusStarting:
		return fmt.Sprintf("Game '%s' (%s) is already starting. Wait for launch to finish, then use games_connect if you need to attach to the existing instance.", game.ID, game.Name)
	default:
		return fmt.Sprintf("Game '%s' (%s) is already running. Use games_status or games_connect instead of starting it again.", game.ID, game.Name)
	}
}

// SupersededStartRefusal re-evaluates the current claim after this start lost a
// fence during startup, mapping it to a stable refusal (design/06): a
// successor operation in flight, a successor active, or uncertainty.
func (m *Manager) SupersededStartRefusal(gameID string) error {
	cur, err := process.LoadRuntimeState(gameID, m.configDir)
	if err != nil || cur == nil {
		return &StartRefusalError{Refusal: &process.StartRefusal{
			Code:    process.RefusalOperationInFlight,
			Message: fmt.Sprintf("the launch of '%s' was superseded during startup and the successor has since finished; re-check games_status", gameID),
		}}
	}
	if process.OperationInFlight(cur.Operation, time.Now().UTC()) {
		op := *cur.Operation
		return &StartRefusalError{Refusal: &process.StartRefusal{
			Code:          process.RefusalOperationInFlight,
			Message:       fmt.Sprintf("the launch of '%s' was superseded during startup; a successor %s operation is in progress (deadline %s)", gameID, op.Action, op.Deadline.Format(time.RFC3339)),
			Phase:         cur.Phase,
			ActiveProfile: process.EffectiveClaimProfile(cur),
			Operation:     &op,
		}}
	}
	if cur.Phase == process.PhaseActive {
		return &StartRefusalError{Refusal: &process.StartRefusal{
			Code:          process.RefusalAlreadyRunning,
			Message:       fmt.Sprintf("the launch of '%s' was superseded during startup; a successor launch is active", gameID),
			Phase:         cur.Phase,
			ActiveProfile: process.EffectiveClaimProfile(cur),
		}}
	}
	return &StartRefusalError{Refusal: &process.StartRefusal{
		Code:    process.RefusalBlockedUnknown,
		Message: fmt.Sprintf("the launch of '%s' was superseded during startup; a successor claim exists in phase %s — re-check games_status", gameID, cur.Phase),
		Phase:   cur.Phase,
	}}
}

// occupiedClaimRefusal is the stable outcome for a Stage 4 persistence failure:
// the claim remains occupied (the operation stays in place) and uncertainty
// blocks — blocked_unknown_state, per the exhaustive terminal-branch rule
// (design/10).
func occupiedClaimRefusal(gameID, what string, err error) error {
	return &StartRefusalError{Refusal: &process.StartRefusal{
		Code:    process.RefusalBlockedUnknown,
		Message: fmt.Sprintf("%s for '%s': %v — the claim remains occupied; re-check games_status and retry", what, gameID, err),
	}}
}
