package config

import (
	"fmt"
	"sort"
	"strings"
)

// ArgumentsLabel names the game-level `args` list for display. When a game
// declares profiles, the game-level list is only the base — the selected
// profile appends to it — so labelling it plainly "Arguments" presents a
// partial command line as the whole thing. That misreads worst exactly after a
// migration, when args a user recognizes have just moved into a profile.
func ArgumentsLabel(g GameConfig) string {
	if len(g.Profiles) > 0 {
		return "Base Arguments (a profile appends to these)"
	}
	return "Arguments"
}

// DescribeLaunchContexts renders a game's profiles and launch inputs as text.
// It returns "" for games without either, so legacy entries gain no output and
// the warning-free compatibility promise is preserved.
//
// This exists because the launch-profile data was reachable only through
// `structuredContent`, which not every MCP client surfaces to a model, while
// games_start's own schema tells callers to "discover profiles with
// games_show". Both frontends render this same text so the CLI and MCP agree.
func DescribeLaunchContexts(g GameConfig) string {
	if len(g.Profiles) == 0 && len(g.LaunchInputs) == 0 {
		return ""
	}

	var b strings.Builder

	if len(g.Profiles) > 0 {
		if g.DefaultProfile != "" {
			fmt.Fprintf(&b, "\nDefault Profile: %s (used by a start that names no profile)\n", g.DefaultProfile)
		}
		// Names and descriptions only: design/10 pins arg/env templates as
		// omitted — "noise, not secret". Callers needing the effective
		// command line use `gabs games doctor`, which resolves each context.
		b.WriteString("\nProfiles:\n")
		for _, name := range SortedProfileNames(g.Profiles) {
			p := g.Profiles[name]
			marker := ""
			if name == g.DefaultProfile {
				marker = " (default)"
			}
			fmt.Fprintf(&b, "  %s%s", name, marker)
			if p.Description != "" {
				fmt.Fprintf(&b, " - %s", p.Description)
			}
			b.WriteString("\n")
		}
	}

	if len(g.LaunchInputs) > 0 {
		b.WriteString("\nLaunch Inputs (only these may be supplied per start):\n")
		names := make([]string, 0, len(g.LaunchInputs))
		for name := range g.LaunchInputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			in := g.LaunchInputs[name]
			fmt.Fprintf(&b, "  %s: %s", name, describeInputConstraints(in))
			if in.Description != "" {
				fmt.Fprintf(&b, " - %s", in.Description)
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// describeInputConstraints renders an input's type and every declared bound, so
// a caller can form a valid value without a trial call.
func describeInputConstraints(in LaunchInputConfig) string {
	parts := []string{in.Type}
	if len(in.Enum) > 0 {
		parts = append(parts, "one of ["+strings.Join(in.Enum, ", ")+"]")
	}
	switch {
	case in.Minimum != nil && in.Maximum != nil:
		parts = append(parts, fmt.Sprintf("range %d..%d", *in.Minimum, *in.Maximum))
	case in.Minimum != nil:
		parts = append(parts, fmt.Sprintf("minimum %d", *in.Minimum))
	case in.Maximum != nil:
		parts = append(parts, fmt.Sprintf("maximum %d", *in.Maximum))
	}
	// Length and pattern are rendered even when an enum is declared:
	// configuration validation does not force enum members through these
	// constraints, while every start enforces all of them together — an enum
	// member that fails the pattern is rejected at call time, and a caller
	// who cannot see the pattern cannot discover a usable value.
	if in.Type == "string" {
		maxLen := InputMaxLengthDefault
		if in.MaxLength != nil {
			maxLen = *in.MaxLength
		}
		parts = append(parts, fmt.Sprintf("maxLength %d", maxLen))
		if in.Pattern != "" {
			// RE2, anchored to the whole value.
			parts = append(parts, fmt.Sprintf("pattern %s (RE2, full match)", in.Pattern))
		}
	}
	if len(in.Profiles) > 0 {
		restricted := append([]string(nil), in.Profiles...)
		sort.Strings(restricted)
		parts = append(parts, "only for profiles ["+strings.Join(restricted, ", ")+"]")
	}
	return strings.Join(parts, ", ")
}

// SortedProfileNames returns the profile names in deterministic order.
func SortedProfileNames(profiles map[string]ProfileConfig) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
