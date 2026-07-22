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
	Phase             string
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
// liveness rule over its pinned context, honor in-flight operations and
// the unobserved supersession policy, publish the complete pre-spawn
// claim, then probe every profile's status hook plus stopProcessName as
// the lost-claim backstop before any spawn. A lost claim race re-evaluates
// the winner instead of fabricating an outcome.
func GateStart(g StartGate) (*StartGateResult, error) {
	now := time.Now().UTC()
	var warnings []string
	var state RuntimeState
	claimed := false

	for attempt := 0; attempt < 3 && !claimed; attempt++ {
		claim, err := LoadRuntimeState(g.GameID, g.ConfigDir)
		if err != nil {
			// A claim exists but cannot be read: GABS started something and
			// cannot tell what — uncertainty blocks (design/05).
			return &StartGateResult{Refusal: &StartRefusal{
				Code:    RefusalBlockedUnknown,
				Message: fmt.Sprintf("runtime claim for %s is unreadable: %v; inspect it or run repair --forget-runtime", g.GameID, err),
			}, Warnings: warnings}, nil
		}
		if claim != nil && claim.SchemaVersion == 0 {
			// A start's duplicate check is a lifecycle touch: the legacy
			// claim normalizes first, so it is evaluated — and fenced —
			// like any current claim (design/07). On failure the raw
			// claim still gets the degraded legacy evaluation below.
			if normalized, nerr := NormalizeLegacyClaim(g.GameID, g.ConfigDir, g.Spec.Mode, g.Spec.ConfigRevision); nerr == nil {
				claim = normalized
			}
		}
		if claim != nil {
			refusal, ws := gateExistingClaim(g, claim, now)
			warnings = append(warnings, ws...)
			if refusal != nil {
				return &StartGateResult{Refusal: refusal, Warnings: warnings}, nil
			}
			// Clear the stale claim — only the exact launch identity that
			// was just evaluated; a racing start's fresh claim survives.
			if rerr := removeRuntimeStateIfLaunch(g.GameID, g.ConfigDir, claim.LaunchID); rerr != nil {
				if errors.Is(rerr, ErrFencingViolation) {
					continue // someone replaced it mid-evaluation; re-evaluate
				}
				return nil, fmt.Errorf("failed to clear stale claim for %s: %w", g.GameID, rerr)
			}
		}

		state = NewRuntimeState(g.Spec, RuntimeStateStatusStarting)
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

		cerr := ClaimRuntimeState(g.GameID, g.ConfigDir, state)
		if cerr == nil {
			claimed = true
			break
		}
		if !errors.Is(cerr, ErrRuntimeStateExists) {
			return nil, cerr
		}
		// Lost the claim race: loop and judge the winner like any existing
		// claim — it may be a preflight start, an external snapshot, or
		// already stale again.
	}
	if !claimed {
		refusal := &StartRefusal{
			Code:    RefusalOperationInFlight,
			Message: fmt.Sprintf("%s is already starting in a concurrent operation", g.GameID),
		}
		if cur, err := LoadRuntimeState(g.GameID, g.ConfigDir); err == nil && cur != nil {
			refusal.Operation = cur.Operation
			refusal.Phase = cur.Phase
		}
		return &StartGateResult{Refusal: refusal, Warnings: warnings}, nil
	}

	probeWarnings, refusal, err := runPreStartProbes(g, &state, now)
	warnings = append(warnings, probeWarnings...)
	if err != nil {
		_ = removeRuntimeStateIfLaunch(g.GameID, g.ConfigDir, state.LaunchID)
		return nil, err
	}
	if refusal != nil {
		return &StartGateResult{Refusal: refusal, Warnings: warnings}, nil
	}
	return &StartGateResult{Claim: &state, Warnings: warnings}, nil
}

// gateExistingClaim returns a refusal, or nil when the claim may be cleared
// and the start may proceed (possibly with warnings, e.g. supersession).
func gateExistingClaim(g StartGate, claim *RuntimeState, now time.Time) (*StartRefusal, []string) {
	if op := claim.Operation; op != nil && !op.Deadline.IsZero() && now.Before(op.Deadline) && op.ExecutorPID > 0 {
		switch v, detail := VerifyPIDFingerprint(op.ExecutorPID, op.ExecutorPIDStartTime); v {
		case StatusRunning:
			verb := op.Action + " operation"
			if op.Action == OperationActionStart {
				verb = "already starting: a start operation"
			}
			return &StartRefusal{
				Code:      RefusalOperationInFlight,
				Message:   fmt.Sprintf("%s is %s begun %s is still in progress (deadline %s)", g.GameID, verb, op.AttemptStartedAt.Format(time.RFC3339), op.Deadline.Format(time.RFC3339)),
				Phase:     claim.Phase,
				Operation: op,
			}, nil
		case StatusUnknown:
			// An uninspectable executor is not a proven-dead executor: the
			// operation may still be running (design/04: inspection failure
			// is unknown, never stopped).
			return &StartRefusal{
				Code:    RefusalBlockedUnknown,
				Message: fmt.Sprintf("an in-flight %s operation on %s has an executor that cannot be inspected (%s); uncertainty blocks the start", op.Action, g.GameID, detail),
				Phase:   claim.Phase,
			}, nil
		}
		// executor provably stopped: an orphaned attempt, judged below
	}

	// A preflight claim whose executor is provably gone is the one safe
	// removal: spawnState preflight proves process creation was never
	// attempted (design/05 Stage 2).
	if claim.Source == SourceGABS && claim.SpawnState == SpawnStatePreflight {
		if op := claim.Operation; op != nil && op.ExecutorPID > 0 {
			if v, _ := VerifyPIDFingerprint(op.ExecutorPID, op.ExecutorPIDStartTime); v == StatusStopped {
				return nil, nil
			}
		}
	}

	// Completed-unobserved markers (design/05 Stage 4): Stage 4 finished
	// with nothing observable — the operation was cleared and the spawn
	// itself succeeded. Only such claims are supersedable, and only past
	// their pinned budget.
	completedUnobserved := claim.Phase == PhaseStarting &&
		claim.Operation == nil && claim.SpawnState == SpawnStateSpawned
	supersedable := completedUnobserved &&
		!claim.ProcessStartDeadline.IsZero() && now.After(claim.ProcessStartDeadline)
	supersedeWarning := fmt.Sprintf("superseded an unobserved launch of %s past its start budget; the abandoned store launch could still appear later", g.GameID)

	// External snapshots' pinned hooks execute in the observed profile's
	// context — GABS_PROFILE must carry the attribution, not an empty
	// GABS-selected profile.
	profile := claim.Profile
	if claim.Source == SourceExternal && claim.ObservedProfile != "" && claim.ObservedProfile != ObservedProfileUnknown {
		profile = claim.ObservedProfile
	}
	// Claimed-context evaluation uses only the snapshot: a hot config edit
	// must never change how an existing launch is classified. Current
	// config is a fallback only for legacy (pre-schema) claims.
	name := claim.StopProcessName
	if name == "" && claim.SchemaVersion == 0 {
		name = g.StopProcessName
	}

	ev := EvaluateLiveness(LivenessInput{
		GABPLive:        g.GABPLive,
		Claim:           claim,
		StatusHook:      claimStatusHook(claim),
		GameID:          g.GameID,
		Profile:         profile,
		StopProcessName: name,
		Now:             now,
	})
	switch ev.Verdict {
	case StatusRunning:
		return &StartRefusal{
			Code:             RefusalAlreadyRunning,
			Message:          fmt.Sprintf("%s is already running (%s)", g.GameID, ev.Detail),
			Phase:            claim.Phase,
			ActiveProfile:    profile,
			RequestedProfile: g.RequestedProfile,
			Evidence:         &ev,
		}, nil
	case StatusUnknown:
		if supersedable && ev.Source == LivenessSourceNone {
			return nil, []string{supersedeWarning}
		}
		return &StartRefusal{
			Code:     RefusalBlockedUnknown,
			Message:  fmt.Sprintf("cannot determine whether %s is still running (%s); a claim exists, so uncertainty blocks the start — verify manually, then repair --forget-runtime if it is truly gone", g.GameID, ev.Detail),
			Phase:    claim.Phase,
			Evidence: &ev,
		}, nil
	default: // stopped
		// Asymmetry for completed-unobserved claims (design/05 Stage 4):
		// the same absence that produced unobserved must not read as
		// stopped now — the store may still launch the game. Only positive
		// stopped-evidence (a status hook verdict, or a recorded workload
		// PID proven gone) clears early; absence waits for the threshold.
		if completedUnobserved {
			positive := ev.Source == LivenessSourceStatusHook ||
				(ev.Source == LivenessSourcePID && claim.PIDRole == PIDRoleWorkload)
			if !positive {
				if supersedable {
					return nil, []string{supersedeWarning}
				}
				return &StartRefusal{
					Code:     RefusalBlockedUnknown,
					Message:  fmt.Sprintf("a launch of %s is still unobserved within its start budget; the store launcher may yet start it — absence of evidence is not stopped here. Re-check games_status, or wait out the budget", g.GameID),
					Phase:    claim.Phase,
					Evidence: &ev,
				}, nil
			}
		}
		return nil, nil
	}
}

func claimStatusHook(claim *RuntimeState) *launch.ResolvedHook {
	if claim.Lifecycle == nil {
		return nil
	}
	return claim.Lifecycle.Status
}

// removeRuntimeStateIfLaunch removes the claim only while it still carries
// the evaluated launch identity — a racing start's fresh claim survives.
func removeRuntimeStateIfLaunch(gameID, configDir, launchID string) error {
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
		return nil
	}
	if cur.LaunchID != launchID {
		return ErrFencingViolation
	}
	return RemoveRuntimeState(gameID, configDir)
}

// runPreStartProbes probes every profile's status hook concurrently, each
// under its own timeout and with its own GABS_PROFILE, plus the
// stopProcessName check — the backstop for lost claims and straggler hooks.
// The fresh claim is already held: concurrent starts see
// operation_in_progress while probes run. All claim rewrites carry the
// fresh claim's fence identities: a slow probe must never convert a
// successor launch into an unrelated snapshot.
func runPreStartProbes(g StartGate, state *RuntimeState, now time.Time) ([]string, *StartRefusal, error) {
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
		if err := persistExternalSnapshot(g, state, profile, g.Probes[profile], now); err != nil {
			if errors.Is(err, ErrFencingViolation) {
				return warnings, supersededDuringProbeRefusal(g), nil
			}
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
		if err := removeRuntimeStateIfLaunch(g.GameID, g.ConfigDir, state.LaunchID); err != nil && !errors.Is(err, ErrFencingViolation) {
			return warnings, nil, err
		}
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
			if err := persistExternalSnapshot(g, state, ObservedProfileUnknown, nil, now); err != nil {
				if errors.Is(err, ErrFencingViolation) {
					return warnings, supersededDuringProbeRefusal(g), nil
				}
				return warnings, nil, err
			}
			return warnings, &StartRefusal{
				Code:              RefusalExternalInstance,
				Message:           fmt.Sprintf("a process named %q (pid %d) is already running; it is now tracked as an external instance of %s", g.StopProcessName, pids[0], g.GameID),
				RequestedProfile:  g.RequestedProfile,
				SnapshotPersisted: true,
			}, nil
		case len(pids) > 1:
			if err := removeRuntimeStateIfLaunch(g.GameID, g.ConfigDir, state.LaunchID); err != nil && !errors.Is(err, ErrFencingViolation) {
				return warnings, nil, err
			}
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

func supersededDuringProbeRefusal(g StartGate) *StartRefusal {
	return &StartRefusal{
		Code:    RefusalOperationInFlight,
		Message: fmt.Sprintf("the start of %s was superseded while pre-start probes ran; re-check games_status", g.GameID),
	}
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
// The rewrite is fenced on the fresh claim's identities.
func persistExternalSnapshot(g StartGate, state *RuntimeState, observedProfile string, lc *launch.ResolvedLifecycle, now time.Time) error {
	_, err := FencedTransition(g.GameID, g.ConfigDir, state.LaunchID, state.Operation.OperationID, func(s *RuntimeState) error {
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
