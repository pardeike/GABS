#!/bin/sh
# Genericity scan (design/31): public docs, the example config, and the skill
# must use only neutral, fictional game names — never a real game or studio
# trademark. This guards against a specific hazard: this project is developed
# alongside game-specific bridges (e.g. a RimWorld bridge), and a copy-paste can
# leak that game's name into the user-facing surface, making GABS look
# single-game when it is game-agnostic.
#
# It deliberately does NOT flag platform/tooling names GABS legitimately
# documents (Steam, Epic, Docker, Gatekeeper, GABP) — only game/studio
# trademarks.
#
# Usage: scripts/genericity-scan.sh   (exit 0 clean, 1 on a finding)

set -eu

cd "$(dirname "$0")/.."

# Public, user-facing surface only. Design docs (design/) are internal and are
# not scanned.
PATHS="README.md example-config.json docs skills/gabs-mcp"

# Real game / studio / franchise trademarks — plus game-modding jargon ("mod",
# "modification"), which reads as a specific game culture on a game-agnostic
# tool — that must never appear on the public surface. Word-boundary,
# case-insensitive. This mirrors and extends internal/mcp's
# TestPublicSurfacesStayGeneric (the authoritative in-suite gate); keep the two
# in sync. Extend as needed.
DENY='mods?|modifications?|rimworld|rimbridge|ludeon|minecraft|mojang|factorio|wube|valheim|iron ?gate|terraria|re-?logic|stardew|skyrim|elder ?scrolls|fallout|bethesda|witcher|cd ?projekt|cyberpunk|fortnite|roblox|counter-?strike|dota|team ?fortress|garry'

status=0
for p in $PATHS; do
	[ -e "$p" ] || continue
	# -r recurse dirs, -I skip binary, -n line numbers, -E regex, -i insensitive,
	# -w word boundary. grep exits 1 when there are no matches — that is success.
	if matches=$(grep -rIniEw "$DENY" "$p" 2>/dev/null); then
		if [ -n "$matches" ]; then
			echo "genericity-scan: real game/studio trademark(s) found on the public surface:"
			echo "$matches"
			status=1
		fi
	fi
done

if [ "$status" -eq 0 ]; then
	echo "genericity-scan: clean — public docs, example config, and skill use neutral names."
fi
exit "$status"
