package process

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
)

// Stable stop/kill codes (design/06, design/10).
const (
	OperationActionStop = "stop"
	OperationActionKill = "kill"

	RefusalStopUnsupported = "stop_unsupported"
	RefusalKillUnsupported = "kill_unsupported"

	OutcomeTerminated             = "terminated"
	OutcomeActionSucceededRunning = "action_succeeded_running"
	OutcomeTerminationUnverified  = "termination_unverified"
	OutcomeActionFailed           = "action_failed"
	OutcomeActionTimedOut         = "action_timed_out"
	OutcomeInterrupted            = "interrupted"

	// Pinned built-in strategies (design/07). The pin is decided at claim
	// creation for the launching platform; executors dispatch on it so a
	// stop after restart or from another GABS process runs what the launch
	// decided.
	BuiltinGracefulSigterm  = "sigterm"
	BuiltinGracefulTaskkill = "taskkill"
	BuiltinForceSigkill     = "sigkill"
	BuiltinForceTaskkill    = "taskkill_force"

	// builtinActionBudget bounds the hook-less action step (a signal send
	// plus at most one process scan); hooks bring their own timeouts.
	builtinActionBudget = 10 * time.Second
)

// Injectable primitives: tests control the action, the verification probe,
// the built-in signal executors, and the poll cadence.
var (
	runActionHookFunc      = RunActionHook
	runStatusProbeFunc     = RunStatusHookClipped
	builtinTerminateFunc   = builtinTerminate
	builtinKillFunc        = builtinKill
	stopVerifyPollInterval = 500 * time.Millisecond
)

// errStopRefusal aborts the admission transition without persisting: a
// refusal never writes.
var errStopRefusal = errors.New("stop refused")

// StopRequest is one stop or kill attempt against the runtime snapshot.
// The claim is the context — callers never pass current config (design/07).
type StopRequest struct {
	GameID     string
	ConfigDir  string
	InstanceID string
	Action     string // OperationActionStop | OperationActionKill

	// GABPLive reports whether this process holds a live bridge connection
	// for the game right now; nil when the caller cannot know (CLI).
	GABPLive func() bool
	// ReapLauncher terminates and reaps a still-live launcher child this
	// process started — called before a stopped verdict clears the claim
	// (design/04); nil when the caller tracks no child.
	ReapLauncher func()
}

// StopRefusal is a structured refusal: nothing was executed and nothing
// was persisted.
type StopRefusal struct {
	Code          string // operation_in_progress | stop_unsupported | kill_unsupported
	Message       string
	Phase         string
	ActiveProfile string
	Operation     *RuntimeOperation
	KillCapable   bool // stop_unsupported only: whether games_kill is the way out
}

// StopOutcome is the terminal result of an executed action, including what
// was persisted as lastActionResult (nil when the claim was removed).
type StopOutcome struct {
	Code          string
	Action        string
	ActiveProfile string
	HookResult    *HookResult
	Detail        string
	Warnings      []string
	ClaimRemoved  bool
	FinalPhase    string
	Result        *RuntimeActionResult
}

// ExecuteStopAction implements design/06: bounded single-operation
// admission under the transition lock, hook-or-builtin action execution
// with the lock released, and the post-action verification matrix. Exactly
// one of outcome/refusal is non-nil on a nil error.
func ExecuteStopAction(req StopRequest) (*StopOutcome, *StopRefusal, error) {
	if req.Action != OperationActionStop && req.Action != OperationActionKill {
		return nil, nil, fmt.Errorf("unknown lifecycle action %q", req.Action)
	}
	now := time.Now().UTC()

	var (
		refusal    *StopRefusal
		op         RuntimeOperation
		priorPhase string
		profile    string
		actionHook *launch.ResolvedHook
		claimSnap  RuntimeState
	)

	_, terr := TransitionRuntimeState(req.GameID, req.ConfigDir, transitionLockGateTimeout, func(s *RuntimeState) error {
		// One lifecycle operation per game at a time: an in-flight attempt
		// refuses immediately with its timing — never queue, never block.
		if cur := s.Operation; cur != nil {
			if OperationInFlight(cur, now) {
				opCopy := *cur
				refusal = &StopRefusal{
					Code:          RefusalOperationInFlight,
					Message:       fmt.Sprintf("a %s operation on %s begun %s is still in progress (deadline %s); re-check after the deadline", cur.Action, req.GameID, cur.AttemptStartedAt.Format(time.RFC3339), cur.Deadline.Format(time.RFC3339)),
					Phase:         s.Phase,
					ActiveProfile: EffectiveClaimProfile(s),
					Operation:     &opCopy,
				}
				return errStopRefusal
			}
			// Expired or provably dead attempt: record the interruption and
			// proceed — retrying is what the idempotent-hook contract is for
			// (design/07). Its late completion is fenced out by operationID.
			s.LastActionResult = &RuntimeActionResult{
				Action:    cur.Action,
				Outcome:   OutcomeInterrupted,
				Detail:    fmt.Sprintf("a previous %s attempt begun %s never completed", cur.Action, cur.AttemptStartedAt.Format(time.RFC3339)),
				Timestamp: now,
			}
			s.Operation = nil
			if s.Phase == PhaseStopping || s.Phase == PhaseKilling {
				s.Phase = PhaseActive
			}
		}

		hook, builtinOK := actionCapability(s, req.Action)
		if hook == nil && !builtinOK {
			refusal = unsupportedRefusal(s, req)
			return errStopRefusal
		}
		actionHook = hook
		priorPhase = s.Phase
		profile = EffectiveClaimProfile(s)

		execPID := os.Getpid()
		execStart, _ := ProcessStartTime(execPID)
		op = RuntimeOperation{
			OperationID:          NewFencingID(),
			Action:               req.Action,
			ExecutorInstanceID:   req.InstanceID,
			ExecutorPID:          execPID,
			ExecutorPIDStartTime: execStart,
			AttemptStartedAt:     now,
			Deadline:             now.Add(actionBudget(hook) + verifyBudget(hook)),
		}
		s.Operation = &op
		if req.Action == OperationActionKill {
			s.Phase = PhaseKilling
		} else {
			s.Phase = PhaseStopping
		}
		claimSnap = *s
		return nil
	})
	switch {
	case terr == nil:
	case errors.Is(terr, errStopRefusal):
		return nil, refusal, nil
	case errors.Is(terr, ErrTransitionLockBusy):
		// Bounded, never a hang (design/06): contention reads as an
		// operation in progress; the caller re-checks.
		return nil, &StopRefusal{
			Code:    RefusalOperationInFlight,
			Message: fmt.Sprintf("another lifecycle operation on %s holds the transition lock; re-check games_status shortly", req.GameID),
		}, nil
	default:
		return nil, nil, terr
	}

	outcome := &StopOutcome{Action: req.Action, ActiveProfile: profile}

	// The action runs with the lock released (design/06: the lock is never
	// held while a hook runs or anything waits).
	actionFailed := ""
	failDetail := ""
	if actionHook != nil {
		ok, hr := runActionHookFunc(actionHook, req.GameID, profile)
		hrCopy := hr
		outcome.HookResult = &hrCopy
		if !ok {
			actionFailed = OutcomeActionFailed
			if hr.TimedOut {
				actionFailed = OutcomeActionTimedOut
			}
			if hr.ExecError != nil {
				failDetail = fmt.Sprintf("%s hook failed to run: %v", req.Action, hr.ExecError)
			}
		}
	} else {
		warnings, err := runBuiltinAction(&claimSnap, req.Action)
		outcome.Warnings = append(outcome.Warnings, warnings...)
		if err != nil {
			actionFailed = OutcomeActionFailed
			failDetail = err.Error()
		}
	}

	if actionFailed != "" {
		result := &RuntimeActionResult{Action: req.Action, Outcome: actionFailed, Detail: failDetail, Timestamp: time.Now().UTC()}
		attachHookEvidence(result, outcome.HookResult)
		outcome.Code = actionFailed
		outcome.Detail = failDetail
		// On failure the phase returns to where it was and the attempt's
		// result is persisted (design/06).
		persistStopCompletion(req, claimSnap.LaunchID, op.OperationID, priorPhase, "", result, outcome)
		return outcome, nil, nil
	}

	verdictCode, verdictDetail := verifyTermination(req, &claimSnap, op, actionHook, profile, outcome)
	outcome.Detail = verdictDetail
	outcome.Code = verdictCode

	switch verdictCode {
	case OutcomeTerminated:
		// Before a stopped verdict clears the claim, terminate and reap any
		// still-live launcher child this process started (design/04).
		if req.ReapLauncher != nil {
			req.ReapLauncher()
		}
		err := removeRuntimeStateForStopCompletion(req, claimSnap.LaunchID, op.OperationID)
		switch {
		case err == nil:
			outcome.ClaimRemoved = true
		case errors.Is(err, errStopAttachmentLive):
			// A live foreign bridge attachment appeared between the last
			// evidence round and the removal: a claim is never cleared under
			// a live bridge (design/04, T-FENCE) — downgrade to unverified.
			verdictDetail = "a live bridge attachment appeared during verification; " + verdictDetail
			outcome.Detail = verdictDetail
			outcome.Code = OutcomeTerminationUnverified
			result := &RuntimeActionResult{Action: req.Action, Outcome: OutcomeTerminationUnverified, Detail: verdictDetail, Timestamp: time.Now().UTC()}
			attachHookEvidence(result, outcome.HookResult)
			persistStopCompletion(req, claimSnap.LaunchID, op.OperationID, priorPhase, "", result, outcome)
			return outcome, nil, nil
		case errors.Is(err, ErrFencingViolation) || errors.Is(err, ErrNoRuntimeClaim):
			outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("the %s completion was superseded and left the current claim untouched", req.Action))
		default:
			outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("verified termination, but the claim could not be removed: %v", err))
		}
		outcome.FinalPhase = ""
	case OutcomeActionSucceededRunning:
		// Positive running evidence after a successful action: the claim
		// stays and — running seen — the phase promotes to active
		// (design/20: any later observation promotes).
		result := &RuntimeActionResult{Action: req.Action, Outcome: verdictCode, Detail: verdictDetail, Timestamp: time.Now().UTC()}
		attachHookEvidence(result, outcome.HookResult)
		persistStopCompletion(req, claimSnap.LaunchID, op.OperationID, PhaseActive, RuntimeStateStatusRunning, result, outcome)
	default: // termination_unverified
		result := &RuntimeActionResult{Action: req.Action, Outcome: verdictCode, Detail: verdictDetail, Timestamp: time.Now().UTC()}
		attachHookEvidence(result, outcome.HookResult)
		persistStopCompletion(req, claimSnap.LaunchID, op.OperationID, priorPhase, "", result, outcome)
	}
	return outcome, nil, nil
}

// executorProvablyGone reports whether an operation's executor fingerprint
// is positively stopped — unknown is not gone (design/04).
func executorProvablyGone(op *RuntimeOperation) bool {
	if op.ExecutorPID <= 0 {
		return false
	}
	v, _ := VerifyPIDFingerprint(op.ExecutorPID, op.ExecutorPIDStartTime)
	return v == StatusStopped
}

// actionCapability resolves what can execute the action: the pinned hook
// (profile overrides already merged at resolution), else the built-in
// fallback when a usable target is pinned. Kill never considers the stop
// hook and stop never considers the kill hook (design/06).
func actionCapability(s *RuntimeState, action string) (*launch.ResolvedHook, bool) {
	var hook *launch.ResolvedHook
	if s.Lifecycle != nil {
		if action == OperationActionKill {
			hook = s.Lifecycle.Kill
		} else {
			hook = s.Lifecycle.Stop
		}
	}
	return hook, builtinTargetExists(s)
}

// builtinTargetExists reports whether the built-in fallback has anything to
// act on: a pinned workload PID (helper PIDs are never the workload,
// design/04) or a pinned stopProcessName.
func builtinTargetExists(s *RuntimeState) bool {
	if s.PIDRole == PIDRoleWorkload && s.GamePID > 0 {
		return true
	}
	return s.StopProcessName != ""
}

func unsupportedRefusal(s *RuntimeState, req StopRequest) *StopRefusal {
	mode := ""
	if s.LaunchMode != "" {
		mode = fmt.Sprintf(" (%s launch)", s.LaunchMode)
	}
	if req.Action == OperationActionKill {
		return &StopRefusal{
			Code:          RefusalKillUnsupported,
			Message:       fmt.Sprintf("no force-capable action exists for %s%s: no kill hook is pinned and no workload PID or stopProcessName is available. Configure a kill hook or 'stopProcessName' — kill never falls back to the graceful stop hook", req.GameID, mode),
			Phase:         s.Phase,
			ActiveProfile: EffectiveClaimProfile(s),
		}
	}
	killHook, builtinOK := actionCapability(s, OperationActionKill)
	return &StopRefusal{
		Code:          RefusalStopUnsupported,
		Message:       fmt.Sprintf("no graceful stop action exists for %s%s: no stop hook is pinned and no workload PID or stopProcessName is available. Configure a stop hook or 'stopProcessName' — stop never silently escalates to kill", req.GameID, mode),
		Phase:         s.Phase,
		ActiveProfile: EffectiveClaimProfile(s),
		KillCapable:   killHook != nil || builtinOK,
	}
}

// EffectiveClaimProfile is the profile a claim's pinned hooks execute with
// and the activeProfile MCP results report: the selected profile, or the
// observed profile for external snapshots (design/05 Stage 2, design/10).
func EffectiveClaimProfile(s *RuntimeState) string {
	if s.Source == SourceExternal && s.ObservedProfile != "" && s.ObservedProfile != ObservedProfileUnknown {
		return s.ObservedProfile
	}
	return s.Profile
}

func actionBudget(hook *launch.ResolvedHook) time.Duration {
	if hook != nil {
		return resolveHookTimeout(hook)
	}
	return builtinActionBudget
}

func verifyBudget(hook *launch.ResolvedHook) time.Duration {
	if hook != nil && hook.VerifyTimeoutSeconds > 0 {
		return time.Duration(hook.VerifyTimeoutSeconds) * time.Second
	}
	return time.Duration(config.VerifyTimeoutDefault) * time.Second
}

func attachHookEvidence(result *RuntimeActionResult, hr *HookResult) {
	if hr == nil {
		return
	}
	code := hr.ExitCode
	result.ExitCode = &code
	result.StderrTail = hr.StderrTail
	result.TreeKillWarning = hr.TreeKillWarning
}

// persistStopCompletion merges the attempt's result into the latest claim,
// fenced by launchID + operationID: a completion racing a kill that removed
// the claim is discarded and can never resurrect state (design/06).
func persistStopCompletion(req StopRequest, launchID, operationID, phase, status string, result *RuntimeActionResult, outcome *StopOutcome) {
	final, err := FencedTransition(req.GameID, req.ConfigDir, launchID, operationID, func(s *RuntimeState) error {
		if phase != "" {
			s.Phase = phase
		}
		if status != "" {
			s.Status = status
		}
		s.Operation = nil
		s.LastActionResult = result
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrFencingViolation) || errors.Is(err, ErrNoRuntimeClaim) {
			outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("the %s result was not persisted: the runtime claim was superseded while the action ran", req.Action))
			return
		}
		outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("the %s result could not be persisted: %v", req.Action, err))
		return
	}
	outcome.Result = result
	outcome.FinalPhase = final.Phase
}

// errStopAttachmentLive refuses a terminated-removal because the current
// claim carries a live foreign bridge attachment: never cleared under a
// live bridge (design/04).
var errStopAttachmentLive = errors.New("live foreign bridge attachment")

// removeRuntimeStateForStopCompletion removes the claim only while it still
// carries the completing attempt's launch and operation identity, and only
// when no fresh fingerprint-matched foreign attachment lease exists — the
// last-instant re-check behind the per-round evidence reads.
func removeRuntimeStateForStopCompletion(req StopRequest, launchID, operationID string) error {
	return removeRuntimeStateGuarded(req.GameID, req.ConfigDir, req.InstanceID, launchID, operationID)
}

func removeRuntimeStateGuarded(gameID, configDir, instanceID, launchID, operationID string) error {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	cur, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrNoRuntimeClaim
	}
	if cur.LaunchID != launchID {
		return ErrFencingViolation
	}
	if cur.Operation == nil || cur.Operation.OperationID != operationID {
		return ErrFencingViolation
	}
	if foreignAttachmentLive(cur.Attachment, instanceID) {
		return errStopAttachmentLive
	}
	return RemoveRuntimeState(gameID, configDir)
}

// foreignAttachmentLive reports whether the record is another process's
// fresh fingerprint-matched lease: the evidence that forbids clearing a
// claim under a live bridge (design/04).
func foreignAttachmentLive(a *RuntimeAttachment, instanceID string) bool {
	if a == nil || attachmentOwnedBy(a, instanceID) {
		return false
	}
	if a.OwnerPID <= 0 || a.OwnerPIDStartTime == 0 || a.LeaseDeadline.IsZero() || !time.Now().Before(a.LeaseDeadline) {
		return false
	}
	v, _ := VerifyPIDFingerprint(a.OwnerPID, a.OwnerPIDStartTime)
	return v == StatusRunning
}

// ReleaseStartClaim removes a start attempt's own claim after a failure —
// fenced by the launch identity it created, so a cleanup path that lost a
// race (its claim superseded, its transition fenced out) can never delete
// a successor claim by bare game ID (design/06). It accepts either the
// attempt's own operation or an operation-free claim (the attempt may have
// completed its promote-to-active transition before discovering the exit),
// never a different operation; a live foreign attachment likewise leaves
// the claim in place — another process owns a bridge into it.
func ReleaseStartClaim(gameID, configDir, instanceID, launchID, operationID string) error {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	cur, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrNoRuntimeClaim
	}
	if cur.LaunchID != launchID {
		return ErrFencingViolation
	}
	if cur.Operation != nil && cur.Operation.OperationID != operationID {
		return ErrFencingViolation
	}
	if foreignAttachmentLive(cur.Attachment, instanceID) {
		return ErrFencingViolation
	}
	return RemoveRuntimeState(gameID, configDir)
}

// RemoveRuntimeStateIfCurrent removes the claim only while it still carries
// the evaluated launch identity, no operation at all, and no live foreign
// attachment — the status path's fenced removal (design/06): a stopped
// verdict computed over a seconds-long hook must never delete a successor
// claim, an attempt that was admitted meanwhile (expired or not — its
// recovery is not the status path's business), or a claim another process
// just attached to.
func RemoveRuntimeStateIfCurrent(gameID, configDir, instanceID, launchID string) error {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	cur, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return err
	}
	if cur == nil {
		return nil // already gone: the goal state
	}
	if cur.LaunchID != launchID {
		return ErrFencingViolation
	}
	if cur.Operation != nil {
		return ErrFencingViolation
	}
	if foreignAttachmentLive(cur.Attachment, instanceID) {
		return ErrFencingViolation
	}
	return RemoveRuntimeState(gameID, configDir)
}

// runBuiltinAction executes the pinned built-in fallback: the verified
// workload PID first, then every stopProcessName match. Signaling nothing
// (already gone) is not a failure — verification decides the outcome; only
// an inability to act (scan error, all signals refused) fails the action.
func runBuiltinAction(claim *RuntimeState, action string) ([]string, error) {
	var warnings []string
	graceful, force := pinnedBuiltinStrategies(claim)
	signal := func(pid int) error {
		if action == OperationActionKill {
			return builtinKillFunc(force, pid)
		}
		return builtinTerminateFunc(graceful, pid)
	}

	if claim.PIDRole == PIDRoleWorkload && claim.GamePID > 0 {
		switch v, detail := VerifyPIDFingerprint(claim.GamePID, claim.PIDStartTime); v {
		case StatusRunning:
			if err := signal(claim.GamePID); err != nil {
				return warnings, fmt.Errorf("failed to signal workload pid %d: %w", claim.GamePID, err)
			}
			return warnings, nil
		case StatusUnknown:
			// A PID that cannot be verified must not be signaled: a reused
			// PID would receive the signal (design/04).
			warnings = append(warnings, fmt.Sprintf("pinned workload PID %d could not be verified (%s); not signaling it, falling back to the process name", claim.GamePID, detail))
		}
		// provably stopped: fall through to the name
	}

	if claim.StopProcessName == "" {
		return warnings, nil // nothing left to act on; verification decides
	}
	pids, err := findProcessesByNameFunc(claim.StopProcessName)
	if err != nil {
		return warnings, fmt.Errorf("process scan for %q failed: %w", claim.StopProcessName, err)
	}
	if len(pids) == 0 {
		return warnings, nil
	}
	if len(pids) > 1 {
		warnings = append(warnings, fmt.Sprintf("%d processes named %q matched; signaling all of them — configure a more specific stopProcessName if this is wrong", len(pids), claim.StopProcessName))
	}
	signaled := 0
	var lastErr error
	for _, pid := range pids {
		if err := signal(pid); err != nil {
			lastErr = err
			warnings = append(warnings, fmt.Sprintf("failed to signal pid %d: %v", pid, err))
			continue
		}
		signaled++
	}
	if signaled == 0 && lastErr != nil {
		return warnings, fmt.Errorf("failed to signal any process named %q: %w", claim.StopProcessName, lastErr)
	}
	return warnings, nil
}

func pinnedBuiltinStrategies(claim *RuntimeState) (string, string) {
	if bf := claim.BuiltinFallback; bf != nil && bf.GracefulStrategy != "" && bf.ForceStrategy != "" {
		return bf.GracefulStrategy, bf.ForceStrategy
	}
	return builtinStrategiesForPlatform()
}

func builtinStrategiesForPlatform() (string, string) {
	if runtime.GOOS == "windows" {
		return BuiltinGracefulTaskkill, BuiltinForceTaskkill
	}
	return BuiltinGracefulSigterm, BuiltinForceSigkill
}

// The strategy pin names the mechanism; on any one machine the platform
// implementation is that mechanism (claims never move between OSes), so
// the executors dispatch to the existing platform helpers.
func builtinTerminate(strategy string, pid int) error {
	_ = strategy
	return terminateProcess(pid, 0)
}

func builtinKill(strategy string, pid int) error {
	_ = strategy
	return killProcess(pid)
}

// stopEvidenceRound is one verification poll over the existing evidence
// sources (design/06): positive sources can prove running or stopped;
// bridge evidence (live GABP, fresh foreign attachment lease) keeps the
// claim but never upgrades to a positive running verdict — it cannot
// distinguish a workload surviving the action from a socket that has not
// yet noticed death (T-FENCE: unverified, never cleared, under a live
// bridge).
type stopEvidenceRound struct {
	sources       int
	running       bool
	anyUnknown    bool
	bridgeLive    bool
	gabpInProcess bool // this process's own live connection (contradiction rule)
	hookStopped   bool // the status hook explicitly reported stopped
	details       []string
}

func (r stopEvidenceRound) allStopped() bool {
	return r.sources > 0 && !r.running && !r.anyUnknown && !r.bridgeLive
}

func (r stopEvidenceRound) noSources() bool {
	return r.sources == 0 && !r.bridgeLive
}

func verifyTermination(req StopRequest, claim *RuntimeState, op RuntimeOperation, actionHook *launch.ResolvedHook, profile string, outcome *StopOutcome) (string, string) {
	window := verifyBudget(actionHook)
	deadline := time.Now().Add(window)
	if !op.Deadline.IsZero() && op.Deadline.Before(deadline) {
		deadline = op.Deadline
	}
	// Always evaluate at least one round, even if the action consumed the
	// whole budget — the outcome must rest on evidence, not elapsed time.
	if !deadline.After(time.Now()) {
		deadline = time.Now().Add(50 * time.Millisecond)
	}

	var statusHook *launch.ResolvedHook
	if claim.Lifecycle != nil {
		statusHook = claim.Lifecycle.Status
	}

	var last stopEvidenceRound
	superseded := false
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		// Every round reads the LATEST claim: attachment changes during
		// stop verification are the ordinary case (design/06) — a bridge
		// attaching while the hook ran must be seen, and a cleared record
		// must stop counting. The pinned context (hooks, PID, name) is
		// immutable within a launch, so the reload only changes what can
		// legitimately change.
		cur, err := LoadRuntimeState(req.GameID, req.ConfigDir)
		switch {
		case err != nil:
			// Transient read failure: evaluate against the last known
			// snapshot rather than inventing evidence.
		case cur == nil || cur.LaunchID != claim.LaunchID:
			// The claim was removed or replaced mid-verification: every
			// completion write would be fenced out — stop polling.
			superseded = true
		default:
			claim = cur
		}
		if superseded {
			break
		}
		last = evaluateStopEvidence(req, claim, statusHook, profile, remaining)
		if last.allStopped() || last.noSources() {
			break
		}
		sleep := stopVerifyPollInterval
		if until := time.Until(deadline); until < sleep {
			sleep = until
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}

	if superseded {
		outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("the runtime claim was removed or replaced while the %s was verified; the result applies to the finished launch only", req.Action))
		detail := joinDetails(last.details)
		if detail == "" {
			detail = "verification abandoned: the claim was superseded"
		}
		return OutcomeTerminationUnverified, detail
	}

	// Contradiction rule (design/04): this process's own live bridge versus
	// an explicit stopped verdict from the status hook is running, with a
	// warning about the hook — never resolved by unverified limbo. The
	// narrow unverified treatments remain for the stop-only wrapper (no
	// independent source) and for foreign persisted leases (T-FENCE).
	if last.gabpInProcess && last.hookStopped {
		outcome.Warnings = append(outcome.Warnings,
			"status hook reports stopped while the GABP bridge is live; the bridge wins — check the hook's exit-code contract")
		return OutcomeActionSucceededRunning, joinDetails(last.details)
	}

	detail := joinDetails(last.details)
	switch {
	case last.noSources():
		// No independent evidence source exists at all: hook success alone
		// stands as the stop-only-wrapper evidence (design/06).
		return OutcomeTerminated, "no independent evidence source exists; the successful action stands as termination evidence"
	case last.allStopped():
		return OutcomeTerminated, detail
	case last.running:
		return OutcomeActionSucceededRunning, detail
	default:
		return OutcomeTerminationUnverified, detail
	}
}

func evaluateStopEvidence(req StopRequest, claim *RuntimeState, statusHook *launch.ResolvedHook, profile string, remaining time.Duration) stopEvidenceRound {
	var r stopEvidenceRound

	if claim.PIDRole == PIDRoleWorkload && claim.GamePID > 0 {
		r.sources++
		switch v, detail := VerifyPIDFingerprint(claim.GamePID, claim.PIDStartTime); v {
		case StatusRunning:
			r.running = true
			r.details = append(r.details, fmt.Sprintf("workload pid %d is running", claim.GamePID))
		case StatusUnknown:
			r.anyUnknown = true
			r.details = append(r.details, fmt.Sprintf("workload pid %d could not be inspected (%s)", claim.GamePID, detail))
		default:
			r.details = append(r.details, fmt.Sprintf("workload pid %d is gone", claim.GamePID))
		}
	}

	if claim.StopProcessName != "" {
		r.sources++
		pids, err := findProcessesByNameFunc(claim.StopProcessName)
		switch {
		case err != nil:
			r.anyUnknown = true
			r.details = append(r.details, fmt.Sprintf("process scan for %q failed: %v", claim.StopProcessName, err))
		case len(pids) > 0:
			r.running = true
			r.details = append(r.details, fmt.Sprintf("%d process(es) named %q still running", len(pids), claim.StopProcessName))
		default:
			r.details = append(r.details, fmt.Sprintf("no process named %q", claim.StopProcessName))
		}
	}

	if statusHook != nil {
		r.sources++
		verdict, hr := runStatusProbeFunc(statusHook, claim.GameID, profile, remaining)
		switch verdict {
		case StatusRunning:
			r.running = true
			r.details = append(r.details, fmt.Sprintf("status hook reports running (exit %d)", hr.ExitCode))
		case StatusStopped:
			r.hookStopped = true
			r.details = append(r.details, fmt.Sprintf("status hook reports stopped (exit %d)", hr.ExitCode))
		default:
			r.anyUnknown = true
			switch {
			case hr.TimedOut:
				r.details = append(r.details, "status hook timed out")
			case hr.ExecError != nil:
				r.details = append(r.details, fmt.Sprintf("status hook failed to run: %v", hr.ExecError))
			default:
				r.details = append(r.details, fmt.Sprintf("status hook exit %d is unclassified", hr.ExitCode))
			}
		}
	}

	if req.GABPLive != nil && req.GABPLive() {
		r.bridgeLive = true
		r.gabpInProcess = true
		r.details = append(r.details, "the GABP bridge connection is still live")
	}

	if a := claim.Attachment; a != nil && !attachmentOwnedBy(a, req.InstanceID) {
		// A foreign attachment record is evidence only while the lease is
		// fresh and the owner fingerprint matches a live process; a
		// self-owned record defers to the in-process connection state
		// (GABPLive), which is more current than the persisted lease.
		if a.OwnerPID > 0 && a.OwnerPIDStartTime != 0 && !a.LeaseDeadline.IsZero() && time.Now().Before(a.LeaseDeadline) {
			switch v, _ := VerifyPIDFingerprint(a.OwnerPID, a.OwnerPIDStartTime); v {
			case StatusRunning:
				r.bridgeLive = true
				r.details = append(r.details, fmt.Sprintf("another process (pid %d) holds a fresh bridge attachment lease", a.OwnerPID))
			case StatusUnknown:
				r.anyUnknown = true
				r.details = append(r.details, fmt.Sprintf("a bridge attachment lease owner (pid %d) could not be inspected", a.OwnerPID))
			}
		}
	}

	return r
}

func attachmentOwnedBy(a *RuntimeAttachment, instanceID string) bool {
	if instanceID != "" && a.OwnerInstanceID != "" {
		return a.OwnerInstanceID == instanceID
	}
	return a.OwnerPID == os.Getpid()
}

func joinDetails(details []string) string {
	out := ""
	for i, d := range details {
		if i > 0 {
			out += "; "
		}
		out += d
	}
	return out
}
