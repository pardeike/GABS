package process

import (
	"fmt"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// Liveness evidence sources, in precedence order (design/04).
const (
	LivenessSourceGABP        = "gabp"
	LivenessSourceAttachment  = "attachment_lease"
	LivenessSourceStatusHook  = "status_hook"
	LivenessSourcePID         = "pid_fingerprint"
	LivenessSourceProcessName = "stop_process_name"
	LivenessSourceNone        = "none"
)

// runStatusHookFunc is injectable for tests.
var runStatusHookFunc = RunStatusHook

// diagnoseHookContradiction runs the status hook even though higher-tier
// evidence already proves running, so a hook that disagrees is reported
// instead of hidden (design/04: contradictions are reported, not resolved).
func diagnoseHookContradiction(ev *LivenessEvidence, in LivenessInput) {
	if !in.DiagnoseHook || in.StatusHook == nil {
		return
	}
	verdict, hr := runStatusHookFunc(in.StatusHook, in.GameID, in.Profile)
	ev.HookResult = &hr
	if verdict == StatusStopped {
		ev.Warnings = append(ev.Warnings,
			"status hook reports stopped while the GABP bridge is live; the bridge wins — check the hook's exit-code contract")
	}
}

// LivenessInput is one evaluation of the liveness rule against the evidence
// a caller can see. StopProcessName is the caller's fallback; a claim's own
// snapshot wins when present (callers never re-guess launch context).
type LivenessInput struct {
	GABPLive        bool // the caller owns a live bridge connection
	Claim           *RuntimeState
	StatusHook      *launch.ResolvedHook
	GameID          string
	Profile         string
	StopProcessName string
	Now             time.Time
	// DiagnoseHook also runs the status hook when higher evidence already
	// proves running, so contradictions are reported instead of hidden.
	DiagnoseHook bool
}

// LivenessEvidence is the verdict plus what was observed — unknown carries
// the observation so callers can say what to do next, never a bare shrug.
type LivenessEvidence struct {
	Verdict    string // running | stopped | unknown
	Source     string
	Detail     string
	HookResult *HookResult
	Warnings   []string
}

// EvaluateLiveness applies the one liveness rule (design/04): live GABP /
// fresh fingerprint-matched attachment lease → status hook → PID fingerprint
// with stopProcessName fallback. unknown never cleans state and never
// authorizes a start while a claim exists.
func EvaluateLiveness(in LivenessInput) LivenessEvidence {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	// 1. A live bridge proves running.
	if in.GABPLive {
		ev := LivenessEvidence{Verdict: StatusRunning, Source: LivenessSourceGABP, Detail: "live GABP connection"}
		diagnoseHookContradiction(&ev, in)
		return ev
	}
	// A persisted attachment is running-evidence for other processes only
	// while the lease is fresh AND the owner fingerprint matches a live
	// process; expired leases are history. Attachments postdate the
	// fingerprint schema, so a missing (zero) fingerprint is a malformed
	// record, never legacy existence-only evidence — a reused owner PID
	// must not impersonate a live bridge.
	if in.Claim != nil && in.Claim.Attachment != nil {
		a := in.Claim.Attachment
		if a.OwnerPID > 0 && a.OwnerPIDStartTime != 0 && !a.LeaseDeadline.IsZero() && now.Before(a.LeaseDeadline) {
			if verdict, _ := VerifyPIDFingerprint(a.OwnerPID, a.OwnerPIDStartTime); verdict == StatusRunning {
				ev := LivenessEvidence{
					Verdict: StatusRunning, Source: LivenessSourceAttachment,
					Detail: fmt.Sprintf("bridge attachment lease fresh (owner pid %d alive, lease until %s)",
						a.OwnerPID, a.LeaseDeadline.UTC().Format(time.RFC3339)),
				}
				// Same precedence tier as a live bridge: contradictions are
				// reported here too, not hidden behind the early return.
				diagnoseHookContradiction(&ev, in)
				return ev
			}
		}
	}

	// 2. A configured status hook is authoritative.
	if in.StatusHook != nil {
		verdict, hr := runStatusHookFunc(in.StatusHook, in.GameID, in.Profile)
		ev := LivenessEvidence{Verdict: verdict, Source: LivenessSourceStatusHook, HookResult: &hr}
		switch {
		case hr.TimedOut:
			ev.Detail = fmt.Sprintf("status hook timed out after %s", hr.Duration.Round(time.Millisecond))
		case hr.ExecError != nil:
			ev.Detail = fmt.Sprintf("status hook failed to run: %v", hr.ExecError)
		default:
			ev.Detail = fmt.Sprintf("status hook exit code %d", hr.ExitCode)
		}
		return ev
	}

	// 3. Built-in evidence.
	if in.Claim == nil {
		// No claim does not mean nothing is running: a configured
		// stopProcessName is still probed so an already-running untracked
		// instance is detected (the lost-claim backstop) instead of being
		// reported stopped.
		if in.StopProcessName != "" {
			pids, err := findProcessesByNameFunc(in.StopProcessName)
			if err != nil {
				return LivenessEvidence{Verdict: StatusUnknown, Source: LivenessSourceProcessName,
					Detail: fmt.Sprintf("no runtime claim; process scan for %q failed: %v", in.StopProcessName, err)}
			}
			if len(pids) > 0 {
				return LivenessEvidence{Verdict: StatusRunning, Source: LivenessSourceProcessName,
					Detail: fmt.Sprintf("no runtime claim, but %d process(es) named %q — external instance candidate", len(pids), in.StopProcessName)}
			}
		}
		return LivenessEvidence{Verdict: StatusStopped, Source: LivenessSourceNone, Detail: "no runtime claim"}
	}

	pidVerdict, pidDetail := "", ""
	switch {
	case in.Claim.PIDRole == PIDRoleHelper:
		// URL modes track the short-lived opener helper, never the workload:
		// its liveness proves nothing and its exit is expected (design/04).
		pidDetail = "tracked PID is the URL-opener helper; not workload evidence"
	case in.Claim.GamePID > 0:
		pidVerdict, pidDetail = VerifyPIDFingerprint(in.Claim.GamePID, in.Claim.PIDStartTime)
		if pidVerdict == StatusRunning {
			return LivenessEvidence{Verdict: StatusRunning, Source: LivenessSourcePID, Detail: pidDetail}
		}
	default:
		pidDetail = "no workload PID recorded"
	}

	name := in.StopProcessName
	if in.Claim.StopProcessName != "" {
		name = in.Claim.StopProcessName
	}
	if name != "" {
		pids, err := findProcessesByNameFunc(name)
		if err != nil {
			return LivenessEvidence{Verdict: StatusUnknown, Source: LivenessSourceProcessName,
				Detail: fmt.Sprintf("process scan for %q failed: %v", name, err)}
		}
		if len(pids) > 0 {
			return LivenessEvidence{Verdict: StatusRunning, Source: LivenessSourceProcessName,
				Detail: fmt.Sprintf("%d process(es) named %q", len(pids), name)}
		}
		if pidVerdict == StatusUnknown {
			// The tracked PID may still exist but could not be inspected; an
			// empty name scan must not downgrade that to stopped.
			return LivenessEvidence{Verdict: StatusUnknown, Source: LivenessSourcePID, Detail: pidDetail}
		}
		return LivenessEvidence{Verdict: StatusStopped, Source: LivenessSourceProcessName,
			Detail: fmt.Sprintf("no process named %q (%s)", name, pidDetail)}
	}

	switch pidVerdict {
	case StatusStopped:
		return LivenessEvidence{Verdict: StatusStopped, Source: LivenessSourcePID, Detail: pidDetail}
	case StatusUnknown:
		return LivenessEvidence{Verdict: StatusUnknown, Source: LivenessSourcePID, Detail: pidDetail}
	default:
		// Helper-role or no PID, no hook, no name: nothing can prove the
		// workload either way.
		return LivenessEvidence{Verdict: StatusUnknown, Source: LivenessSourceNone, Detail: pidDetail}
	}
}
