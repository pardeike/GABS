# Context delivery

Profiles only matter if the resolved context actually reaches the game.
Past launch-state problems all trace to one implicit assumption — "the
child gets what we set" — that launcher chains silently break. This design
replaces the assumption with a small, provable promise plus per-launch
observation.

## The one-hop guarantee

An OS offers exactly three channels into a new process: argv, environment,
and working directory. GABS guarantees delivery of all three to the first
process it creates — and only to that process. Every further hop (script →
game, wrapper → container, store client → relaunched child) is owned by
whoever creates it. This makes the semantics implementable with certainty:
the guarantee is one syscall deep and fully testable.

## Chain ownership

Who owns the last hop, per chain:

| Chain | argv | env | cwd | last-hop owner |
| --- | --- | --- | --- | --- |
| DirectPath/SteamManaged → binary | GABS | GABS | GABS | GABS |
| Script wrapper → game | wrapper forwards (`"$@"` / `%*`) | inherited by default | inherited | user's wrapper |
| Wrapper → container | wrapper maps args | wrapper maps explicitly (`-e`) | n/a inside | user's wrapper |
| SteamManaged with SteamAPI re-exec | lost (Steam's own options) | lost (Steam client's env) | Steam's choice | Steam client |
| SteamAppId / EpicAppId URL | none | none | none | store client — why these modes reject profiles |

## Wrapper contract

Documented and testable: forward argv; preserve the environment, or map it
explicitly where a boundary filters it (containers, sandboxes). To make
that mapping robust, GABS exports `GABS_FORWARD_ENV`: a comma-separated
list of every variable name a wrapper must carry across a filtering
boundary — the GABS-managed variables plus the names (never the values) of
all config-defined context keys for this launch. A container wrapper
forwards them generically
(`for v in ${GABS_FORWARD_ENV//,/ }; do args+=(-e "$v"); done`) and stays
correct when GABS or the config adds variables later. GABS also exports
`GABS_ABSENT_ENV`: the names that must be *absent* in the workload
(`unsetEnv` results). An absence cannot be forwarded, but a boundary can
reintroduce one — a container image defining `CONTENT_SET` defeats the
profile's isolation — so the wrapper contract includes not re-injecting
these names, and delivery verification checks them (below). Both loops are
safe because validation restricts config-declared env names to a portable
identifier grammar — no commas, whitespace, or glob characters can appear
in a name.

## Files are diagnostic, never live handoff

`bridge.json` additionally records the selected profile, config revision,
and start time — for diagnostics and doctor output only. The live bridge
contract stays env-only: a bridge takes its endpoint, token, and context
from its process environment, never from the file, because a file cannot
prove freshness — a process from a previous launch (or a manual one)
reading the current file would attach to the wrong generation with the
wrong profile attribution. The consequence is stated, not hidden: a chain
that strips the environment produces a workload whose bridge cannot attach
at all; the fix is the wrapper contract or the Steam mitigations, never a
stale-file guess. Argv lost to a re-exec is equally unrecoverable by any
file. (The single narrowly-scoped exception — one-time legacy endpoint
migration for pre-upgrade claims — is defined in
[07-runtime-state.md](07-runtime-state.md), Legacy claims.)

## Credentials are per-launch

Every new runtime claim mints a fresh GABP token, even when the endpoint
port is reused — `resetEndpoint` governs only the port. A connection
presenting a previous launch's token is rejected and surfaced as
`stale_bridge_credential`. Without this, a delayed process from a
superseded launch (a store finally starting the game after an `unobserved`
claim was reclaimed) could authenticate with reused credentials and be
attributed to the new launch and profile. Env-only handoff and per-launch
credentials together are what make a live GABP connection trustworthy as
launch-attribution evidence.

## Delivery verification

GABS is the GABP client; the game-side bridge is the server. The bridge's
**session-welcome response** therefore gains the one optional,
backward-compatible field: the bridge reports the **raw observed values** —
argv, working directory, the environment values for the keys named in
`GABS_FORWARD_ENV`, and explicit present/absent status for the keys named
in `GABS_ABSENT_ENV` — as seen inside the game process; GABS hashes
locally, compares, and discards. The env channel encodes presence versus
absence explicitly: a key that was meant to be absent but arrives with a
value (reintroduced by a container image or wrapper) fails the channel
exactly like a wrong value. (Bridge-side hashing was considered and
rejected: it would require distributing the salt and pinning a canonical
encoding across every bridge SDK for no real privacy gain on a
token-authenticated localhost connection. A separate bridge→GABS follow-up
method was likewise rejected: welcome-time reporting suffices and keeps the
wire surface at exactly one field.)

GABS compares **per channel** — argv, cwd, managed env, config-context env
— against non-reversible digests of the resolved spec pinned at spawn. The
argv channel covers the argument payload *excluding argv[0]*: GABS spawns a
wrapper with the wrapper as argv[0] while the game observes its own
executable there, so element zero legitimately differs across hops and
process identity is judged by fingerprint/adoption, not by this channel.

`contextDelivery` reports per-channel verdicts plus an overall summary,
aggregated by one pinned matrix:

- no `observed` field at all → every expected channel `unknown`, overall
  `unknown` (an old bridge yields `unknown`, never `partial`);
- a reported, matching channel → `verified`;
- a reported, differing value → `mismatched`;
- an expected but unreported channel → `unknown`;
- a channel that cannot be compared by contract (legacy relative
  game-level `workingDir`) → `unverifiable`;
- overall `verified` requires every channel the resolved spec uses to be
  `verified`;
- any explicit `mismatched` → overall `partial`;
- comparable evidence mixed with `unknown`/`unverifiable` channels →
  overall `partial`;
- no comparable evidence and no mismatch → overall `unknown`;
- only overall `verified` increments `deliveriesVerified`.

So a wrapper that forwards the managed variables but drops `CONTENT_SET` or
changes the working directory yields `partial`, never a false `verified`.
Working directories are compared in one platform-canonical form on both
sides — absolute, symlink-resolved (macOS `/tmp` is `/private/tmp`), case-
and separator-folded on Windows — computed by GABS for both the spawn
digest and the reported value; canonicalization failure makes that channel
`unknown`, never a false mismatch. Comparing against spawn-time digests
(not current config) keeps verification correct for delayed handshakes —
after a CLI start, a server restart, or a config edit. "Did my profile
actually reach the game?" becomes a per-launch observed fact, not an
inference.

## Platform rules (fixed here so implementers never guess)

- macOS: a `.app` bundle target resolves to its inner executable
  (`Contents/MacOS/`, per `CFBundleExecutable`) and is exec'd directly.
  For the propagation-capable modes (DirectPath, SteamManaged,
  CustomCommand) GABS never uses `open` — LaunchServices launches drop
  argv and env silently. The URL-only modes are the explicit exception:
  they exist to hand a URL to the OS opener (`open`/`xdg-open`/shell
  association), promise no propagation, and reject profiles for exactly
  that reason. Doctor warns when a quarantined/translocated app makes
  relative paths unreliable.
- Windows: argv is joined into one command line using standard
  `CommandLineToArgvW` quoting; games with custom command-line parsing may
  mis-split exotic values — a documented caveat, not a GABS bug. For the
  propagation-capable modes GABS never falls back to ShellExecute (it
  would silently lose env injection and the PID); URL modes use the
  shell's URL association by design. A target that requires elevation
  fails spawn with a precise "requires elevation; GABS does not elevate"
  hint.
- Linux: Steam Linux Runtime (pressure-vessel), Flatpak, and Snap may
  filter or contain the environment. The wrapper contract plus
  `GABS_FORWARD_ENV` is the supported path, and delivery verification
  shows what actually arrived.

## Why this is complete

argv, environment, cwd, files, and post-start IPC are the only ways any OS
lets one process hand state to another. GABS owns the first three for one
hop, deliberately refuses the file channel for live handoff (freshness is
unprovable), and post-start IPC is GABP itself. Anything a chain drops, the
wrapper contract makes assignable and delivery verification makes visible.
There is deliberately no pre-launch "prepare" hook for materializing
per-profile state (config files, registry keys, directories): a wrapper
script *is* the prepare hook — it composes, runs under the user's control,
and is testable without GABS.
