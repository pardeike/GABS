package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/lifecycle"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// loadCLISnapshot loads a pinned config snapshot for a one-shot CLI command.
func loadCLISnapshot(configDir string) (*config.Snapshot, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, err
	}
	_, snap, cerr := config.NewSeededStore(cp.GetMainConfigPath())
	if cerr != nil {
		return nil, cerr
	}
	return snap, nil
}

// cliStartManager builds a one-shot lifecycle Manager for a start (it needs the
// pinned config for endpoint prep and the start budget, plus a real controller
// factory and starter).
func cliStartManager(log util.Logger, configDir string, snap *config.Snapshot) *lifecycle.Manager {
	return lifecycle.NewManager(log, configDir, lifecycle.NewInstanceID(), snap.Config,
		snap.Config.GetSessionOwnerLease(), process.NewSerializedStarter(),
		func() process.ControllerInterface { return process.NewController() })
}

// cliClaimManager builds a one-shot lifecycle Manager for status/stop/kill,
// which operate purely on the persisted claim (no config, controller, or
// starter needed).
func cliClaimManager(log util.Logger, configDir string) *lifecycle.Manager {
	return lifecycle.NewManager(log, configDir, lifecycle.NewInstanceID(), nil, 0, nil, nil)
}

// parseStartFlags parses `--profile NAME` and repeated `--input NAME=VALUE`
// (repeating a name is an error) from the tokens after the game ID.
func parseStartFlags(args []string) (profile string, rawInputs map[string]string, err error) {
	rawInputs = map[string]string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--profile":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--profile requires a value")
			}
			profile = args[i+1]
			i++
		case "--input":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--input requires NAME=VALUE")
			}
			name, value, ok := strings.Cut(args[i+1], "=")
			if !ok || name == "" {
				return "", nil, fmt.Errorf("--input must be NAME=VALUE, got %q", args[i+1])
			}
			if _, dup := rawInputs[name]; dup {
				return "", nil, fmt.Errorf("--input %q was given more than once", name)
			}
			rawInputs[name] = value
			i++
		default:
			return "", nil, fmt.Errorf("unknown start flag: %s", args[i])
		}
	}
	return profile, rawInputs, nil
}

// coerceInputValue converts a CLI string value to the type the resolver expects
// for the input's declared type (design/11: "parse per the declared type").
func coerceInputValue(decl config.LaunchInputConfig, raw string) (any, error) {
	switch decl.Type {
	case "boolean":
		b, perr := strconv.ParseBool(raw)
		if perr != nil {
			return nil, fmt.Errorf("value %q must be a boolean (true/false)", raw)
		}
		return b, nil
	case "integer":
		// The resolver validates and range-checks a json.Number exactly.
		return json.Number(raw), nil
	default:
		// string and any other declared type take the raw string; an undeclared
		// name stays a string and the resolver rejects it as not declared.
		return raw, nil
	}
}

// startGameCLI runs Stages 1–4 for a game and exits with
// started_attachment_deferred — the workload is verified and its claim active,
// but a one-shot CLI never holds the GABP socket open (design/11).
func startGameCLI(log util.Logger, gameID, configDir, profile string, rawInputs map[string]string) int {
	snap, err := loadCLISnapshot(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config_invalid: %v\n", err)
		return 1
	}
	game, ok := snap.Config.GetGame(gameID)
	if !ok {
		fmt.Fprintf(os.Stderr, "game_not_found: no game %q is configured\n", gameID)
		return 1
	}

	inputs := make(map[string]any, len(rawInputs))
	for name, raw := range rawInputs {
		if decl, declared := game.LaunchInputs[name]; declared {
			v, cerr := coerceInputValue(decl, raw)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "launch_input_invalid: input %q: %v\n", name, cerr)
				return 1
			}
			inputs[name] = v
		} else {
			inputs[name] = raw // resolver reports launch_input_not_declared
		}
	}

	resolved, rerr := launch.Resolve(snap, launch.Request{GameID: gameID, Profile: profile, Inputs: inputs}, launch.Options{
		InheritedEnv:       os.Environ(),
		CaseInsensitiveEnv: runtime.GOOS == "windows",
	})
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", rerr.Code, rerr.Message)
		return 1
	}
	if issues := launch.CheckResolvability(game, resolved); len(issues) > 0 {
		fmt.Fprintf(os.Stderr, "launch_spec_unresolvable: cannot start %q:\n", gameID)
		for _, is := range issues {
			fmt.Fprintf(os.Stderr, "  %s\n", is.String())
		}
		return 1
	}

	m := cliStartManager(log, configDir, snap)
	hctx := m.BuildHistoryContext(snap, *game, resolved, inputs)
	spec := m.LaunchSpecWithRuntimeDir(lifecycle.LaunchSpecFromResolved(*game, resolved))
	sr, serr := m.Start(lifecycle.StartRequest{
		Game:               *game,
		LaunchSpec:         spec,
		Resolved:           resolved,
		ResetEndpoint:      false,
		StartupGABPTimeout: 0,
		HistoryContext:     hctx,
		// A one-shot CLI holds no live bridge and no in-process registry: nil
		// callbacks make the persisted attachment lease the cross-process
		// liveness authority (design/04).
		BridgeBound:          nil,
		CheckInProcessActive: nil,
	})
	if serr != nil {
		code, msg := cliStartFailureText(serr, *game)
		fmt.Fprintf(os.Stderr, "%s: %s\n", code, msg)
		return 1
	}

	for _, w := range sr.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	fmt.Printf("started_attachment_deferred: '%s' verified (pid %d, bridge 127.0.0.1:%d). "+
		"The workload is running and its runtime claim is active; attachment was not attempted. "+
		"Attach from a server session with games_connect.\n", game.ID, sr.RuntimeState.GamePID, sr.Port)
	return 0
}

// cliStartFailureText maps a Stage 1–4 failure to a stable code plus a
// human-readable line, mirroring the MCP handler's outcome identities without
// synthesizing a ToolResult.
func cliStartFailureText(err error, game config.GameConfig) (string, string) {
	var refusal *lifecycle.StartRefusalError
	if errors.As(err, &refusal) {
		return refusal.Refusal.Code, refusal.Refusal.Message
	}
	var unobs *lifecycle.UnobservedStartError
	if errors.As(err, &unobs) {
		return "unobserved", fmt.Sprintf("nothing observable for '%s' within the start budget; the store launcher may still be working — re-check 'gabs games status %s'", game.ID, game.ID)
	}
	var exited *lifecycle.ExitedDuringStartError
	if errors.As(err, &exited) {
		msg := fmt.Sprintf("'%s' exited during start (exit code %d); this is attributed to the workload", game.ID, exited.ExitCode)
		if exited.Tail != "" {
			msg += "\n" + exited.Tail
		}
		return "exited_during_start", msg
	}
	var active *lifecycle.GameAlreadyActiveError
	if errors.As(err, &active) {
		return "already_running", active.ToolMessage(game)
	}
	var endpointInUse *config.BridgeEndpointInUseError
	if errors.As(err, &endpointInUse) {
		return "bridge_endpoint_in_use", err.Error()
	}
	var epErr *lifecycle.EndpointUnavailableError
	if errors.As(err, &epErr) {
		return "endpoint_unavailable", epErr.Error()
	}
	var sizeIssue *launch.SpecSizeIssue
	if errors.As(err, &sizeIssue) {
		return "spec_too_large", sizeIssue.Message
	}
	if errors.Is(err, process.ErrFencingViolation) || errors.Is(err, process.ErrNoRuntimeClaim) {
		return "operation_in_progress", fmt.Sprintf("the start of '%s' was superseded during startup; re-check 'gabs games status %s'", game.ID, game.ID)
	}
	var procErr *process.ProcessError
	if errors.As(err, &procErr) && (procErr.Type == process.ProcessErrorTypeStart || procErr.Type == process.ProcessErrorTypeConfiguration) {
		return "spawn_failed", fmt.Sprintf("failed to start '%s': %v", game.ID, err)
	}
	return "blocked_unknown_state", fmt.Sprintf("failed to start '%s': %v", game.ID, err)
}

// statusGameCLI prints one game's status from its persisted claim, or a summary
// of every configured/claimed game when gameID is empty.
func statusGameCLI(log util.Logger, gameID, configDir string) int {
	m := cliClaimManager(log, configDir)
	if gameID != "" {
		return printOneStatus(m, gameID)
	}

	ids := map[string]bool{}
	if gc, err := config.LoadGamesConfigFromDir(configDir); err == nil {
		for _, g := range gc.ListGames() {
			ids[g.ID] = true
		}
	}
	if claimed, err := process.ListRuntimeClaimIDs(configDir); err == nil {
		for _, id := range claimed {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		fmt.Println("No games configured and no runtime claims.")
		return 0
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sortStrings(ordered)
	for _, id := range ordered {
		printOneStatus(m, id)
	}
	return 0
}

func printOneStatus(m *lifecycle.Manager, gameID string) int {
	ev, claim, err := m.Status(gameID, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: cannot read runtime claim: %v\n", gameID, err)
		return 1
	}
	if claim == nil {
		fmt.Printf("%s: stopped (no runtime claim)\n", gameID)
		return 0
	}
	line := fmt.Sprintf("%s: %s — phase %s", gameID, ev.Verdict, claim.Phase)
	if p := process.EffectiveClaimProfile(claim); p != "" {
		line += fmt.Sprintf(", profile %s", p)
	}
	if ev.Detail != "" {
		line += fmt.Sprintf(" (%s)", ev.Detail)
	}
	fmt.Println(line)
	return 0
}

// stopGameCLI stops or kills a game from its persisted claim.
func stopGameCLI(log util.Logger, gameID, configDir, action string) int {
	m := cliClaimManager(log, configDir)

	launchMode, configRevision := "", ""
	if gc, err := config.LoadGamesConfigFromDir(configDir); err == nil {
		if g, ok := gc.GetGame(gameID); ok {
			launchMode = g.LaunchMode
		}
	}

	claim, err := m.LoadStopClaim(gameID, launchMode, configRevision)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blocked_unknown_state: the runtime claim for '%s' is unreadable: %v. Use 'gabs games repair %s --forget-runtime' if the game is provably gone.\n", gameID, err, gameID)
		return 1
	}
	if claim == nil {
		fmt.Printf("%s is not running (no runtime claim).\n", gameID)
		return 0
	}

	outcome, refusal, err := m.Stop(lifecycle.StopRequest{
		GameID:             gameID,
		Action:             action,
		HistoryProfile:     process.EffectiveClaimProfile(claim),
		HistoryContextHash: claim.HistoryContextHash,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "blocked_unknown_state: failed to %s '%s': %v\n", action, gameID, err)
		return 1
	}
	if refusal != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", refusal.Code, refusal.Message)
		return 1
	}
	fmt.Printf("%s: %s", gameID, outcome.Code)
	if outcome.Detail != "" {
		fmt.Printf(" — %s", outcome.Detail)
	}
	fmt.Println()
	switch outcome.Code {
	case process.OutcomeTerminated, process.OutcomeActionSucceededRunning:
		return 0
	default:
		// termination_unverified, action_failed, action_timed_out, interrupted
		return 1
	}
}

// sortStrings sorts a slice in place (small local helper to avoid importing
// sort just for this file).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
