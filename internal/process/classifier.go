package process

import (
	"fmt"
	"strings"
	"time"
)

// ClassifyContext is the evidence the pure classifier reads: proof state
// from the track record and the supplied-input situation. Classification
// may READ history (including proof-adjusted rules); only accepted-attempt
// terminal failures WRITE it (design/08, design/20).
type ClassifyContext struct {
	// Proven is true when the resolved context has at least one verified
	// workload start recorded.
	Proven bool
	// InputCombinationFresh is true when this exact supplied-input
	// combination has never been proven on an otherwise-proven context.
	InputCombinationFresh bool
	SuppliedInputs        []string
	// WrapperExit is CAUSE evidence for exited_during_start: the exit was
	// produced by a wrapper/container in the launch chain (a missing image,
	// name conflict, or host failure) rather than the game process itself —
	// environment-class. A status hook merely reporting "stopped" is LIVENESS
	// evidence, not cause evidence (round 11 P2-5), so it does NOT set this:
	// a plain game with a status hook that crashes stays game-class. With no
	// wrapper signal, exited_during_start defaults to game (design/08's
	// "crash on start → game").
	WrapperExit bool
}

// Classification is a cause class plus an optional secondary note (the
// candidate-input confidence adjustment, never a class change).
type Classification struct {
	Class         string
	SecondaryNote string
}

// Classify maps an outcome code plus evidence to exactly one cause class
// (design/08). The static table is authoritative from the design/05
// bad-case map's Class column; only launch_spec_unresolvable and unobserved
// are proof-adjusted. An unproven input combination adjusts confidence
// (a secondary note), never the class — inferring config from novelty would
// send agents toward the speculative edits this system exists to prevent.
func Classify(code string, ctx ClassifyContext) Classification {
	switch code {
	// call — the request was wrong; fix the call, not the config.
	case "unknown_argument", "invalid_argument", "game_not_found",
		"ambiguous_game_reference", "profiles_not_configured", "profile_not_found",
		"launch_input_not_declared", "launch_input_invalid",
		"timeout_out_of_range":
		return Classification{Class: CauseCall}

	// config — the config file is wrong (validation, incompatible mode,
	// oversized spec, static binding). Stop/kill "unsupported" is a
	// configuration gap: no stop/kill mechanism is configured (design/08
	// does not tabulate these; recorded as config in the Deviations note).
	case "config_invalid", "launch_mode_incompatible", "spec_too_large",
		"stop_unsupported", "kill_unsupported":
		return Classification{Class: CauseConfig}

	// Clean success — no failure cause (round 11 P2-6). A verified stop must
	// never acquire a causeClass; without this it would hit the environment
	// default. Callers attach causeClass only when Class is non-empty.
	case "terminated":
		return Classification{}

	// state — GABS runtime state must be resolved first. A stop that
	// succeeded while the workload is still running (action_succeeded_
	// running) or was interrupted is likewise a state situation to resolve.
	case "already_running", "blocked_unknown_state", "external_instance_detected",
		"operation_in_progress", "termination_unverified", "action_succeeded_running",
		"interrupted", "action_execution_failed":
		return Classification{Class: CauseState}

	// environment — host/store/network state; config edits cannot fix it.
	case "spawn_failed", "endpoint_unavailable", "stale_bridge_credential",
		"action_failed", "action_timed_out":
		return Classification{Class: CauseEnvironment}

	// exited_during_start defaults to game (design/08: "crash on start →
	// game"). It is environment ONLY with positive wrapper/container CAUSE
	// evidence — the wrapper itself exited (missing image, host failure). A
	// status hook reporting "stopped" is liveness, not cause, so it never
	// flips a plain game crash to environment (round 11 P2-5).
	case "exited_during_start":
		if ctx.WrapperExit {
			return withInputNote(Classification{Class: CauseEnvironment}, ctx)
		}
		return withInputNote(Classification{Class: CauseGame}, ctx)

	// Proof-adjusted (design/05 dual-class rows): proven → environment
	// ("it existed before — moved or uninstalled?"), never-proven → config
	// ("probably a typo").
	case "launch_spec_unresolvable", "unobserved":
		if ctx.Proven {
			return withInputNote(Classification{Class: CauseEnvironment}, ctx)
		}
		return Classification{Class: CauseConfig}

	default:
		// An unmapped terminal code is treated as environment rather than
		// config, so a new branch never sends agents at the config file by
		// default; adding a branch means adding a case (design/10).
		return Classification{Class: CauseEnvironment}
	}
}

func withInputNote(c Classification, ctx ClassifyContext) Classification {
	if ctx.InputCombinationFresh && len(ctx.SuppliedInputs) > 0 {
		c.SecondaryNote = fmt.Sprintf("first run with this input combination (%s); the input is a candidate cause", strings.Join(ctx.SuppliedInputs, ", "))
	}
	return c
}

// TrackRecordLine renders the one-line track-record summary attached to
// every failure result (design/08): counts plus the split-counter hint
// that points game-side when the workload is proven but the bridge never
// connected.
func TrackRecordLine(e *HistoryEntry) string {
	if e == nil || e.WorkloadStarts == 0 {
		// "Never proven" is itself the relevant evidence (design/08): a
		// never-proven context is where a config edit is legitimate, and
		// the line says so rather than being omitted.
		if e != nil && e.ConsecutiveFailures > 0 {
			return fmt.Sprintf("no successful starts recorded for this context (%d consecutive failures)", e.ConsecutiveFailures)
		}
		return "no successful starts recorded for this context"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "started %d×", e.WorkloadStarts)
	if !e.LastSuccessAt.IsZero() {
		fmt.Fprintf(&b, ", last %s ago", humanizeAgo(time.Since(e.LastSuccessAt)))
	}
	fmt.Fprintf(&b, "; bridge connected %d×", e.BridgeConnects)
	if e.DeliveriesVerified > 0 {
		fmt.Fprintf(&b, ", delivery verified %d×", e.DeliveriesVerified)
	}
	if e.CleanStops > 0 {
		fmt.Fprintf(&b, ", clean stops %d×", e.CleanStops)
	}
	if e.WorkloadStarts > 0 && e.BridgeConnects == 0 {
		b.WriteString(" — the workload starts but the bridge has never connected (points game-side, not at launch config)")
	}
	if e.ConsecutiveFailures > 0 {
		fmt.Fprintf(&b, "; %d consecutive failures", e.ConsecutiveFailures)
	}
	return b.String()
}

func humanizeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
