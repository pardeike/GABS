package process

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// Stable Stage 2 refusal codes (design/05, design/10).
const (
	RefusalAlreadyRunning     = "already_running"
	RefusalBlockedUnknown     = "blocked_unknown_state"
	RefusalOperationInFlight  = "operation_in_progress"
	RefusalExternalInstance   = "external_instance_detected"
	OperationActionStart      = "start"
	ObservedProfileUnknown    = "unknown"
	transitionLockGateTimeout = 5 * time.Second
)

// ErrFencingViolation means a claim mutation found a different launch or
// operation identity than the caller's — the claim was superseded and the
// completion must not apply (design/06).
var ErrFencingViolation = errors.New("runtime claim superseded; completion discarded")

// StartGate is one Stage 2 evaluation: claim gating, pre-start probing,
// and claim creation. Probes carry the resolved lifecycle per profile
// (key "" for unprofiled games).
type StartGate struct {
	GameID           string
	ConfigDir        string
	InstanceID       string
	RequestedProfile string
	GABPLive         bool
	Spec             LaunchSpec
	Budget           time.Duration // process-start budget; pins ProcessStartDeadline
	Probes           map[string]*launch.ResolvedLifecycle
	StopProcessName  string
}

// StartRefusal is a structured Stage 2 refusal with a stable code.
type StartRefusal struct {
	Code              string
	Message           string
	ActiveProfile     string
	RequestedProfile  string
	Candidates        []string
	Evidence          *LivenessEvidence
	Operation         *RuntimeOperation
	SnapshotPersisted bool
}

// StartGateResult carries either a fresh claim (start may proceed) or a
// refusal — never both.
type StartGateResult struct {
	Claim    *RuntimeState
	Warnings []string
	Refusal  *StartRefusal
}

// GateStart implements design/05 Stage 2: evaluate an existing claim by the
// liveness rule (running → already_running; unknown → blocked; stopped →
// clear and proceed), honor in-flight operations and the unobserved
// supersession policy, publish the complete pre-spawn claim, then probe
// every profile's status hook plus stopProcessName as the lost-claim
// backstop before any spawn.
func GateStart(g StartGate) (*StartGateResult, error) {
	now := time.Now().UTC()

	claim, err := LoadRuntimeState(g.GameID, g.ConfigDir)
	if err != nil {
		// A claim exists but cannot be read: GABS started something and
		// cannot tell what — uncertainty blocks (design/05).
		return &StartGateResult{Refusal: &StartRefusal{
			Code:    RefusalBlockedUnknown,
			Message: fmt.Sprintf("runtime claim for %s is unreadable: %v; inspect it or run repair --forget-runtime", g.GameID, err),
		}}, nil
	}
	if claim != nil {
		if ref := gateExistingClaim(g, claim, now); ref != nil {
			return &StartGateResult{Refusal: ref}, nil
		}
		// stale or superseded: clear it and proceed
		if err := RemoveRuntimeState(g.GameID, g.ConfigDir); err != nil {
			return nil, fmt.Errorf("failed to clear stale claim for %s: %w", g.GameID, err)
		}
	}

	state := NewRuntimeState(g.Spec, RuntimeStateStatusStarting)
	state.OwnerInstanceID = g.InstanceID
	execPID := os.Getpid()
	execStart, _ := ProcessStartTime(execPID)
	state.Operation = &RuntimeOperation{
		OperationID:          NewFencingID(),
		Action:               OperationActionStart,
		ExecutorInstanceID:   g.InstanceID,
		ExecutorPID:          execPID,
		ExecutorPIDStartTime: execStart,
		AttemptStartedAt:     now,
		Deadline:             now.Add(g.Budget),
	}
	state.ProcessStartDeadline = now.Add(g.Budget)

	if err := ClaimRuntimeState(g.GameID, g.ConfigDir, state); err != nil {
		if errors.Is(err, ErrRuntimeStateExists) {
			// Lost the claim race: the concurrent start's preflight claim
			// carries the operation timing (design/05).
			if racing, lerr := LoadRuntimeState(g.GameID, g.ConfigDir); lerr == nil && racing != nil {
				return &StartGateResult{Refusal: &StartRefusal{
					Code:      RefusalOperationInFlight,
					Message:   fmt.Sprintf("%s is already starting in a concurrent operation", g.GameID),
					Operation: racing.Operation,
				}}, nil
			}
			return &StartGateResult{Refusal: &StartRefusal{
				Code:    RefusalOperationInFlight,
				Message: fmt.Sprintf("%s is already starting in a concurrent operation", g.GameID),
			}}, nil
		}
		return nil, err
	}

	warnings, refusal, err := runPreStartProbes(g, now)
	if err != nil {
		_ = RemoveRuntimeState(g.GameID, g.ConfigDir)
		return nil, err
	}
	if refusal != nil {
		return &StartGateResult{Refusal: refusal, Warnings: warnings}, nil
	}
	return &StartGateResult{Claim: &state, Warnings: warnings}, nil
}

// gateExistingClaim returns a refusal, or nil when the claim is stale (or
// superseded per the unobserved policy) and the caller may clear it.
func gateExistingClaim(g StartGate, claim *RuntimeState, now time.Time) *StartRefusal {
	if op := claim.Operation; op != nil && !op.Deadline.IsZero() && now.Before(op.Deadline) {
		// An in-flight operation blocks only while its executor is provably
		// alive; a dead executor's orphaned attempt falls through to the
		// liveness rule (design/06, refined by M2.7 recovery).
		if op.ExecutorPID > 0 {
			if v, _ := VerifyPIDFingerprint(op.ExecutorPID, op.ExecutorPIDStartTime); v == StatusRunning {
				verb := op.Action + " operation"
				if op.Action == OperationActionStart {
					verb = "already starting: a start operation"
				}
				return &StartRefusal{
					Code:      RefusalOperationInFlight,
					Message:   fmt.Sprintf("%s is %s begun %s is still in progress (deadline %s)", g.GameID, verb, op.AttemptStartedAt.Format(time.RFC3339), op.Deadline.Format(time.RFC3339)),
					Operation: op,
				}
			}
		}
	}

	// Unobserved supersession (design/05 Stage 4): a starting claim older
	// than its process-start budget may be reclaimed if fresh evidence
	// again finds nothing observable.
	superseded := claim.Phase == PhaseStarting &&
		!claim.ProcessStartDeadline.IsZero() && now.After(claim.ProcessStartDeadline)

	ev := EvaluateLiveness(LivenessInput{
		GABPLive:        g.GABPLive,
		Claim:           claim,
		StatusHook:      claimStatusHook(claim),
		GameID:          g.GameID,
		Profile:         claim.Profile,
		StopProcessName: g.StopProcessName,
		Now:             now,
	})
	switch ev.Verdict {
	case StatusRunning:
		return &StartRefusal{
			Code:             RefusalAlreadyRunning,
			Message:          fmt.Sprintf("%s is already running (%s)", g.GameID, ev.Detail),
			ActiveProfile:    claim.Profile,
			RequestedProfile: g.RequestedProfile,
			Evidence:         &ev,
		}
	case StatusUnknown:
		// "Nothing observable" on a superseded starting claim is the same
		// absence that produced unobserved — reclaim. Unknown from failing
		// evidence (a hook error, an uninspectable PID) still blocks.
		if superseded && ev.Source == LivenessSourceNone {
			return nil
		}
		return &StartRefusal{
			Code:     RefusalBlockedUnknown,
			Message:  fmt.Sprintf("cannot determine whether %s is still running (%s); a claim exists, so uncertainty blocks the start — verify manually, then repair --forget-runtime if it is truly gone", g.GameID, ev.Detail),
			Evidence: &ev,
		}
	default: // stopped: stale claim, clear and proceed
		return nil
	}
}

func claimStatusHook(claim *RuntimeState) *launch.ResolvedHook {
	if claim.Lifecycle == nil {
		return nil
	}
	return claim.Lifecycle.Status
}

// runPreStartProbes probes every profile's status hook concurrently, each
// under its own timeout and with its own GABS_PROFILE, plus the
// stopProcessName check — the backstop for lost claims and straggler hooks.
// The fresh claim is already held: concurrent starts see
// operation_in_progress while probes run.
func runPreStartProbes(g StartGate, now time.Time) ([]string, *StartRefusal, error) {
	var warnings []string

	type probeOutcome struct {
		profile string
		verdict string
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		outcomes []probeOutcome
	)
	for profile, lc := range g.Probes {
		if lc == nil || lc.Status == nil {
			continue
		}
		wg.Add(1)
		go func(profile string, hook *launch.ResolvedHook) {
			defer wg.Done()
			verdict, _ := runStatusHookFunc(hook, g.GameID, profile)
			mu.Lock()
			outcomes = append(outcomes, probeOutcome{profile: profile, verdict: verdict})
			mu.Unlock()
		}(profile, lc.Status)
	}
	wg.Wait()

	var running, unknown []string
	for _, o := range outcomes {
		switch o.verdict {
		case StatusRunning:
			running = append(running, o.profile)
		case StatusUnknown:
			unknown = append(unknown, o.profile)
		}
	}
	sort.Strings(running)
	sort.Strings(unknown)

	if len(running) == 1 {
		profile := running[0]
		if err := persistExternalSnapshot(g, profile, g.Probes[profile], now); err != nil {
			return warnings, nil, err
		}
		return warnings, &StartRefusal{
			Code:              RefusalExternalInstance,
			Message:           fmt.Sprintf("an instance of %s (profile %s) is already running outside GABS management; it is now tracked by ID for status/stop/kill", g.GameID, profileLabel(profile)),
			ActiveProfile:     profile,
			RequestedProfile:  g.RequestedProfile,
			SnapshotPersisted: true,
		}, nil
	}
	if len(running) > 1 {
		// GABS never guesses among candidates: report all, persist nothing.
		_ = RemoveRuntimeState(g.GameID, g.ConfigDir)
		return warnings, &StartRefusal{
			Code:             RefusalExternalInstance,
			Message:          fmt.Sprintf("multiple profiles of %s report running (%s); resolve manually before starting", g.GameID, strings.Join(running, ", ")),
			RequestedProfile: g.RequestedProfile,
			Candidates:       running,
		}, nil
	}

	if len(unknown) > 0 {
		// No claim existed: GABS owns nothing yet, so unprobeable profiles
		// warn instead of blocking (the asymmetry is deliberate, design/05).
		warnings = append(warnings, fmt.Sprintf("could not probe profile(s) %s before starting; if another instance is running there, this start violates the one-active-instance expectation", strings.Join(unknown, ", ")))
	}

	if g.StopProcessName != "" {
		pids, err := findProcessesByNameFunc(g.StopProcessName)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("process scan for %q failed before starting: %v", g.StopProcessName, err))
		case len(pids) == 1:
			if err := persistExternalSnapshot(g, ObservedProfileUnknown, nil, now); err != nil {
				return warnings, nil, err
			}
			return warnings, &StartRefusal{
				Code:              RefusalExternalInstance,
				Message:           fmt.Sprintf("a process named %q (pid %d) is already running; it is now tracked as an external instance of %s", g.StopProcessName, pids[0], g.GameID),
				RequestedProfile:  g.RequestedProfile,
				SnapshotPersisted: true,
			}, nil
		case len(pids) > 1:
			_ = RemoveRuntimeState(g.GameID, g.ConfigDir)
			candidates := make([]string, 0, len(pids))
			for _, p := range pids {
				candidates = append(candidates, fmt.Sprintf("pid %d", p))
			}
			return warnings, &StartRefusal{
				Code:             RefusalExternalInstance,
				Message:          fmt.Sprintf("multiple processes named %q are running (%s); resolve manually before starting", g.StopProcessName, strings.Join(candidates, ", ")),
				RequestedProfile: g.RequestedProfile,
				Candidates:       candidates,
			}, nil
		}
	}

	return warnings, nil, nil
}

func profileLabel(profile string) string {
	if profile == "" {
		return "none"
	}
	return profile
}

// persistExternalSnapshot rewrites the fresh preflight claim into an
// external snapshot (design/07): phase active, source external, observed
// profile, hooks pinned from current config, and truthfully absent
// GABS-only fields — appliedLaunchInputsState is unavailable, never empty.
func persistExternalSnapshot(g StartGate, observedProfile string, lc *launch.ResolvedLifecycle, now time.Time) error {
	_, err := TransitionRuntimeState(g.GameID, g.ConfigDir, transitionLockGateTimeout, func(s *RuntimeState) error {
		s.Status = RuntimeStateStatusRunning
		s.Phase = PhaseActive
		s.Source = SourceExternal
		s.SpawnState = ""
		s.Operation = nil
		s.ProcessStartDeadline = time.Time{}
		s.GamePID = 0
		s.PIDStartTime = 0
		s.Endpoint = nil
		s.Profile = ""
		s.ObservedProfile = observedProfile
		s.AppliedInputNames = nil
		s.AppliedInputsState = AppliedInputsStateUnavailable
		s.Lifecycle = lc
		s.StopProcessName = g.StopProcessName
		s.UpdatedAt = now
		return nil
	})
	return err
}

// FencedTransition applies a claim mutation only when the claim still
// carries the caller's launch identity (and operation identity when one is
// given): completions never apply to a superseded claim (design/06).
func FencedTransition(gameID, configDir, launchID, operationID string, mutate func(*RuntimeState) error) (*RuntimeState, error) {
	return TransitionRuntimeState(gameID, configDir, transitionLockGateTimeout, func(s *RuntimeState) error {
		if s.LaunchID != launchID {
			return ErrFencingViolation
		}
		if operationID != "" {
			if s.Operation == nil || s.Operation.OperationID != operationID {
				return ErrFencingViolation
			}
		}
		return mutate(s)
	})
}
