# Track record and failure attribution

The failure taxonomy creates a known hazard: an agent that sees a start or
stop failure reaches for the nearest thing it can change — the GABS config
— and "fixes" settings that were never broken, because the real cause was
Steam not running, a stopped container daemon, or a crashing save. GABS
cannot prevent config edits (the config is a plain file, and easy editing
is a feature this design depends on), so this design makes misdiagnosis
hard and visible instead of building locks. The distinction it draws is
between *edits as workflow* — adding a profile, declaring an input,
initial setup: easy, encouraged, hot-reloaded — and *edits as failure
response*, which on a proven configuration are almost always wrong.

## Track record

For every game, GABS keeps a small `history.json` beside the runtime state
(0600, atomic writes; survives claim removal and GABS restarts). It is
keyed per profile by a hash of the *resolved launch context* — target,
mode, argv, config-controlled env, cwd, resolved hooks; managed variables
and launch inputs excluded. Per context it records four counters (workload
starts verified, bridge connections, verified context deliveries, clean
stops), last success time, the last failure (outcome, class, supplied
input names), and a consecutive-failure count. Successes are additionally
bucketed by supplied-input combination — the sorted input names, a hash of
those inputs' *declarations*, and a keyed digest of the supplied *values*,
so `scenario=arena` and `scenario=tutorial` are distinct buckets (value
variants per input set are capped, evicting least-recently-used). Proof
therefore distinguishes "proven bare" from "proven with this exact input
combination". Crucially, an unproven combination adjusts *confidence*, not
*class*: a first failure with `scenario=tutorial` on a proven bare context
keeps the class its outcome implies (a crash is still `game`), with the
unproven combination reported as secondary evidence — "first run with this
input combination; the input is a candidate cause". The `config` class is
reserved for static binding/substitution failures and direct configuration
evidence; inferring it from mere novelty would send agents toward exactly
the speculative config edits this system exists to prevent. Editing an
input's declaration still resets exactly that input's buckets.

Only failures attributable to a resolved launch or active runtime context
mutate history at all. `call`-class errors — unknown profile, undeclared
input, malformed value — happen before any context exists: the response
still carries track-record evidence, but a caller's typo never counts as a
context failure, never advances `consecutiveFailures`, and can never
trigger the edit-visibility notice. Because the hash covers the resolved
context, an edit resets the track record of exactly the contexts it
changes: adding profile C leaves profiles A and B proven; editing profile
B does not touch A's proof. Proof is earned by successful operation and
cannot be edited into existence — any edit of a proven context visibly
resets it to "never proven".

A failed pre-spawn store-readiness gate is likewise evidence about the host,
not an attempted workload. `store_client_not_ready` still renders the resolved
context's existing track record, but it does not write `history.json`, change
the last failure, or advance `consecutiveFailures`.

## Failure attribution

Every failure result carries a `causeClass`:

- `call` — the request was wrong (unknown argument, undeclared input,
  unknown profile). Fix the call, not the config.
- `config` — the config file is wrong (validation errors, incompatible
  launch mode, unresolvable paths on a never-proven context, static
  binding/substitution failures). The result names the exact JSON path.
- `environment` — host/store/network state (Steam not running or updating,
  daemon unreachable, hook timeout, a proven target now missing, port
  exhaustion). Config edits cannot fix these.
- `game` — the workload itself (crash on start, bad save, anti-cheat, a
  bridge mod missing while the workload runs fine).
- `state` — GABS runtime state must be resolved first (already running,
  unknown liveness, operation in progress, unverified termination).

Classification uses the track record: a missing target on a context that
has started 14 times is `environment` ("it existed before — moved or
uninstalled?"); the same error on a never-proven context is `config`
("probably a typo"). The split counters sharpen the message further:
"workload starts fine (14×) but the bridge has never connected" points
game-side, not at launch config. Failure results include the track record
in one line, and `nextActions` never propose a config edit for a
non-`config` class.

## Edit visibility

When a reload changes a proven context whose last recorded outcome was a
non-`config`-class failure, the next result for that game carries a
one-line notice: "configuration changed after an environment-class
failure; the previous context had 14 successful starts — verify this edit
was intended." A nudge, not a block: legitimate edits proceed untouched,
and context-hash granularity keeps unrelated proof intact, so the notice
fires only in the suspicious pattern.

## Agent contract

Codified in the `gabs-mcp` skill and restated in every relevant error:
edit GABS config only when the failure class is `config`, when doing
initial setup of a never-proven game, or when the user explicitly asked
for a config change. For `environment`/`game`/`state` failures on a proven
context: follow the returned next actions, retry within reason, and report
to the user if the problem persists — do not touch the config.

Doctor prints the full track record, and `doctor --show-last-good` prints
the last-known-good entry for a game whose proven context was edited, so a
human (or an instructed agent) can compare or restore by hand. GABS never
restores automatically.
