package main

import (
	"fmt"
	"strings"
)

// globalFlags are the process-wide flags declared on the top-level FlagSet.
// The value reports whether the flag consumes a following token when it is not
// written in `--flag=value` form.
//
// These are hoisted out of the argument vector before parsing because Go's
// flag package stops at the first non-flag token. Without hoisting,
// `gabs games list --configDir /path` parsed no flags at all: the override was
// silently discarded and the command operated on the DEFAULT config directory
// while exiting 0. For a configuration-first tool, quietly acting on the wrong
// config file is worse than any error message.
var globalFlags = map[string]bool{
	"configDir":        true,
	"log-level":        true,
	"reconnectBackoff": true,
	"grace":            true,
	"http":             true,
	"addr":             true,
}

// hoistGlobalFlags splits args into the tokens belonging to the top-level
// FlagSet and the tokens belonging to the subcommand. Subcommand flags
// (--profile, --input, --show-last-good, ...) are left untouched in rest so
// each action keeps parsing its own surface. A literal "--" ends hoisting and
// is passed through WITH every remaining token, so the subcommand layer can
// honor it too — a canonical dash-prefixed game ID (even one spelled like a
// global flag) stays addressable behind it.
func hoistGlobalFlags(args []string) (globals, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		tok := args[i]

		if tok == "--" {
			rest = append(rest, args[i:]...)
			return globals, rest, nil
		}

		name, hasValue := globalFlagName(tok)
		if name == "" {
			rest = append(rest, tok)
			continue
		}

		if hasValue {
			globals = append(globals, tok)
			continue
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("flag --%s requires a value", name)
		}
		globals = append(globals, tok, args[i+1])
		i++
	}
	return globals, rest, nil
}

// globalFlagName reports the global-flag name a token names, and whether the
// token already carries its value in `=` form. It returns "" for positionals
// and for flags that belong to a subcommand.
func globalFlagName(tok string) (name string, hasValue bool) {
	if len(tok) < 2 || tok[0] != '-' {
		return "", false
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-")
	if trimmed == "" {
		return "", false
	}
	base, _, hasEquals := strings.Cut(trimmed, "=")
	if !globalFlags[base] {
		return "", false
	}
	return base, hasEquals
}

// actionsWithOwnFlagParser lists the actions that parse their own flag surface
// (some of which take values, e.g. `--profile NAME`). Their parsers already
// reject anything they do not recognize, so the generic trailing-argument check
// must not second-guess them — it cannot tell a flag's value from a stray
// positional.
var actionsWithOwnFlagParser = map[string]bool{
	"start":  true, // --profile NAME, --input NAME=VALUE
	"doctor": true, // --show-last-good
	"repair": true, // --forget-runtime, --yes/-y
}

// trailingAllowanceFor reports how many positional arguments an action accepts
// after its own name. A negative result means the action validates its own
// remainder and the generic check is skipped.
func trailingAllowanceFor(action string) int {
	if actionsWithOwnFlagParser[action] {
		return -1
	}
	switch action {
	case "list":
		return 0
	case "status":
		// Optional game ID: no-arg status is the runtime-only union (design/11).
		return 1
	default:
		return 1
	}
}

// checkNoTrailingArgs rejects leftovers beyond what an action takes. Actions
// read a fixed argument index and previously ignored the remainder, so a
// misplaced token or a typo vanished silently. Global flags are already hoisted
// out by the time this runs, so a flag-like token in args is simply unknown —
// while every token in escaped (those after a literal "--") is a positional
// regardless of dashes, so a canonical dash-prefixed game ID stays usable.
func checkNoTrailingArgs(action string, args, escaped []string, allowance int) error {
	if allowance < 0 {
		return nil
	}
	var extra []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("unknown %s flag: %s (place a dash-prefixed game ID after \"--\")", action, a)
		}
		extra = append(extra, a)
	}
	extra = append(extra, escaped...)
	if len(extra) > allowance {
		return fmt.Errorf("games %s takes at most %d argument(s); unexpected: %s",
			action, allowance, strings.Join(extra[allowance:], " "))
	}
	return nil
}
