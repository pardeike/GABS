# Test plan

Organized by area; each area lists the tests an implementing agent writes
*before* the corresponding code (see PROGRESS.md protocol). Test IDs
(T-VAL, T-RES, T-START, T-LIFE, T-FENCE, T-RT, T-RELOAD, T-TRACK, T-DELIV,
T-MCP, T-CLI, T-ACC, T-GATE) are referenced from PROGRESS.md.

## T-VAL — Config validation

Default-profile requirement; name grammar; reference errors; reserved env
prefixes incl. Windows case variants; portable identifier grammar for
config-declared env keys (comma/space/glob names rejected); absolute-path
requirement for profile/hook working directories; input
type/enum/bounds/`${value}` rules; string maxLength (code points,
multibyte test), pattern (RE2 full-match — substring match rejected;
invalid pattern fails validation), NUL/invalid-UTF-8 value rejection at
call time; integer exact decoding (2^53+1 supplied as float → rejected
non-integral; int64 bounds; canonical decimal substitution); env-key
conflicts between inputs; `unsetEnv`/`env` same-layer conflict;
placeholder validation; SteamAppId/EpicAppId rejection of
profiles/inputs/game-level env+unsetEnv plus doctor findings (not load
warnings) for legacy launcher-only args/workingDir; URL-mode
observation/control requirement — stopProcessName no longer mandatory when
a game-level status hook plus stop-or-kill hook is configured, still an
error when neither mechanism exists; unknown-key error in new subtrees vs
warning elsewhere (and warning presence in show/list output); top-level
unknown key surfaces in global configWarnings via MCP; duplicate JSON
members anywhere → config_invalid with path, including duplicates that
struct decoding would silently collapse; static resolvability errors with
both JSON path and filesystem path; JSON-path accuracy; Windows
non-executable hook command rejection; the M1 lifecycle feature gate is
removed (M2.14) — `lifecycle` fields now validate and execute rather than
being rejected as "not yet supported".

## T-RES — Resolver

Arg order; env precedence incl. inherited GABS_/GABP_ stripping,
`unsetEnv` layers, and managed-layer wins (a profile `unsetEnv` makes an
inherited host variable absent — not empty — in the child and in delivery
verification); Windows case-insensitive merge; workingDir override; typed
substitution without shell; deep-copy immutability across reload; one
pinned revision per resolution; hook PATH resolution to absolute;
`GABS_FORWARD_ENV` equals the actually-injected key set (drift
assertion); resolved-spec platform-size check (oversized env block/argv →
structured Stage 2 error naming the part, not E2BIG/spawn failure).

## T-START — Start pipeline

- Stage 1 exhaustiveness: every row of the branch-to-code table reachable;
  `launch_spec_unresolvable` carries JSON + filesystem paths;
  `spawn_failed` unreachable for resolvability failures.
- spawn_failed: nonexistent target caught at Stage 1; non-executable and
  bad-arch caught at spawn with OS error.
- exited_during_start: helper exits code 3 after writing to stderr →
  result carries code 3 + stderr tail; claim released.
- adopted: helper spawns a detached same-name child and exits 0 → verified
  running, adopted=true, warning present; bridge connect afterwards proves
  managed-env survival; a `verified` delivery report proves full context.
- unobserved: URL-mode simulation with no observable process → claim kept
  in phase=starting; later fake name-match promotes to active; later
  definitive stopped clears; helper exit in a URL-mode launch is not
  `exited_during_start` and the claim survives to `unobserved`.
- unobserved retry policy: a second games_start while the first attempt is
  still in flight (live owner, operation timing in the claim) →
  operation_in_progress; after `unobserved` has returned (no operation in
  flight) → `blocked_unknown_state` — never already_running (absence is
  not running) and never a fabricated operation; after the age threshold
  with fresh empty probes → the warned reclaim; status observations with
  no evidence never clear the `starting` claim.
- started_bridge_pending: helper runs, never connects →
  started_bridge_pending, background attach continues, later
  games_connect succeeds against a late-starting fake bridge.
- Steam readiness: macOS SteamManaged does not spawn until a fresh hidden-child
  probe proves both client-library pipe and global-user stages; cold-client
  flow opens Steam once and eventually succeeds; permanently not-ready flow
  waits to the caller deadline then returns retryable
  `store_client_not_ready` with `processStarted=false`; absent/unloadable probe
  returns the same stable code with non-retryable `probe_unavailable`; helper
  crash/hang/malformed/oversized output is contained; App-ID environment is
  scrubbed from the helper; failure releases the fresh claim and does not
  mutate history. Success restamps the fenced deadline and preserves the full
  Stage 4/GABP budget. SteamAppId and non-macOS paths retain their current
  advisory/assistance behavior.
- Pre-start probes: no claim + any profile's probe running →
  external_instance_detected (+ external snapshot); probes unknown →
  start proceeds with warning listing unprobeable profiles; with claim +
  unknown → blocked_unknown_state (asymmetry test); all profiles are
  probed, each with its normal GABS_PROFILE — there is no dedupe path;
  byte-identical status hooks that answer differently based on
  GABS_PROFILE: the divergent answers are observed.
- Lost-claim invariant: profile A running (fake container), claim
  deleted, start of profile B → all-profile probing detects A →
  external_instance_detected + external snapshot persisted → games_stop
  with no profile argument stops A via the snapshot, including after a
  server restart; a `${profile}`-bearing game-level hook probes every
  profile and finds A.
- Name-only external detection: unique stopProcessName match with no
  hooks → external snapshot with built-in fallback pinned → stop works
  after restart; colliding matches → all reported, no snapshot.
- Claim-window recovery: kill GABS between claim creation and endpoint
  allocation → next start finds dead owner + spawnState=preflight and
  proceeds without repair; kill between spawnState=spawning and PID
  persist → all three verdicts via the normal liveness rule (pinned
  status hook reports running → active; definitive stopped → claim
  removed; genuinely unknown → occupied/unknown preserved); concurrent
  start during a live preflight → operation_in_progress from the claim's
  operation timing, not blocked_unknown_state.

## T-LIFE — Lifecycle hooks and liveness

Exit-code contract incl. custom sets, unclassified codes, timeout, exec
failure → unknown; unknown preserves state and blocks start (claimed
case); GABP-live vs hook-stopped → running + warning, no cleanup;
stop→verify→cleanup; verifyTimeoutSeconds honored; each verification probe
clipped to min(statusHookTimeout, remaining window) — a hanging 60 s hook
cannot push past the persisted deadline, and a caller re-check at the
deadline finds a terminal state; the stop-verification matrix row-by-row —
running → action_succeeded_running, all-stopped → terminated, any-unknown
(status hook timeout, inspection failure) → termination_unverified with
claim kept, no-sources → hook success clears; stop-only wrapper with a
still-live GABP connection at the verification window →
termination_unverified, claim kept, no duplicate start; kill never falls
back to stop; kill-only URL config: games_stop → stop_unsupported with
games_kill next action; hook timeout kills process group/Job Object,
direct child reaped, treeKillWarning recorded; straggler grandchild
scenario documented-only (manual test); hook env excludes GABP secrets;
stderr tail in failure results; PID-reuse fingerprint mismatch;
inspection errors (permission-denied process table, fingerprint read
failure) → unknown, never stopped, distinct from no-match;
stopProcessName collision warning.

## T-FENCE — Phases, transition lock, fencing

Second stop during stopping → operation_in_progress, hook not run twice;
kill during stopping → operation_in_progress, then runs after resolution;
status never blocks during a long stop and reports phase+deadline;
abandoned caller: stop completes server-side, success removes claim,
failure persists lastActionResult visible in status; operation timing
(action, started-at, deadline) renders in operation_in_progress from a
second process and after a restart; concurrent ops under `-race`.

Domain-scoped fencing: CLI kill and server stop interleaved (incl. crash
injection between hook completion and state write) — a failed stop
finishing after kill removed the claim is rejected by launchID +
operationID validation and cannot re-persist `active`; two processes
cannot both pass read-decide-persist for the same CAS generation; a stale
callback carrying any matching fields but a different launch ID is
discarded (ABA test across claim delete/recreate); the lock is provably
not held during hook execution (a blocked hook does not prevent the other
process from reading state); lock acquisition failure surfaces as a
bounded `operation_in_progress`, never a hang.

Atomic claim publication: a status read racing initial claim publication
never observes empty/partial JSON (hammer test: reader loop during
repeated claim create/remove); a failed initial write publishes nothing
and the next start claims cleanly.

Interrupted-phase normalization: restart with phase=stopping + dead
executor + workload running normalizes to active + lastActionResult
interrupted, never renders operation_in_progress, and an immediate retry
stop succeeds; same with workload unknown → active + unknown verdict;
restart mid-stop: phase=stopping snapshot recovers via liveness without
replay.

Cross-process attachment evidence: CLI stop of a server-owned launch
reads the persisted attachment record (lease fresh, owner fingerprint
alive) as running-evidence and returns termination_unverified instead of
clearing the claim under a live bridge; the same record from a dead owner
is ignored (connection died with the process); CLI stop while server
holds in-memory state → server reconciles from claim file + liveness.

## T-RT — Runtime state and recovery

Claim collision (two starts, one wins); snapshot survives restart;
profile edit/removal doesn't change active stop behavior; a status hook's
custom stopped code still classifies as stopped after restart from the
snapshot (exit-code sets persisted); removed game stays addressable and
no-arg games_status surfaces it (configured:false + phase) so a fresh
agent can stop it; PIDRole: after restart plus a config edit switching
the entry's mode, a persisted helper PID is still never workload evidence
and a persisted workload PID still is (recovery reads the claim, not
config); ContextDelivery verdict survives restart in games_status without
downgrade; adopted/URL launch after restart + config removal still stops
via the pinned stopProcessName fallback from the snapshot; corrupt
runtime.json → unknown + repair path; atomic write; 0600/0700 modes;
ClaimRuntimeState creates 0600 and legacy 0644 runtime files are
tightened on load; input values never persisted/logged; external
snapshots report attachment unavailable and appliedLaunchInputsState
unavailable; legacy-claim first lifecycle touch fully normalizes (launch
ID/generation minted, fallback pinned from the old claim,
normalizedFromLegacy + revision recorded) and degraded
status-before-normalization never renders newer-schema-only fields;
migration gating — a freshly created claim (pre-endpoint) and an external
snapshot never enter the bridge.json migration path (schema-marker test);
the pre-upgrade migration path validates and migrates the legacy
bridge.json endpoint exactly once.

## T-RELOAD — Config reload

Next-call pickup (in-place and rename edits); invalid file →
last-known-good, starts blocked with exact error, active stop still
works; fix clears next call; invalid parsed once; startup with invalid
config fails; tools/list unchanged; concurrent reload under `-race`;
status/show report activeConfigRevision ≠ currentConfigRevision after
reload during an active launch.

## T-TRACK — Track record and attribution

Counters increment at exactly the defined points (Stage 4 / Stage 5 /
verified delivery / verified stop) and consecutiveFailures resets on
success; deliveriesVerified persists across restart and defaults to 0
when absent from an older history file; context-hash granularity —
editing profile B leaves profile A's proof intact, adding profile C
resets nothing, editing shared game-level args resets all profiles;
launch input *values* and managed env never affect the context hash;
input-combination buckets — scenario=arena proven does not mark
scenario=tutorial proven; bucket cap evicts LRU; a *statically* invalid
binding (bad substitution, malformed declaration) fails at Stage 1 as a
config/call error with no history mutation, while a syntactically valid
combination whose workload crashes keeps its outcome-implied class (game)
with the candidate-input secondary note, bare-set proof intact, and
editing that declaration resets only its bucket; proof-adjusted
classification — missing target on proven context → environment, on
never-proven → config; a post-spawn `exited_during_start` is `game` by the
evidence-based default across every launch mode (DirectPath, CustomCommand,
SteamManaged) and however the exit was surfaced (dead PID or status hook
reporting stopped) — never re-attributed to environment on the basis of
launch mode or hook result (design/05 §"Why exited_during_start is always
game"); an OS process-creation failure stays `spawn_failed`/environment;
"workload proven, bridge never connected" renders
the game-side hint; every failure result carries causeClass +
track-record line; no non-config nextActions template mentions config
editing (template-level assertion); call-class errors (unknown profile,
bad input value) mutate no history and never arm the edit notice; edit
notice fires only for proven + last-failure-non-config + hash-changed,
exactly once per edit, and not for legitimate additive edits;
history.json survives claim removal and restart, is 0600/atomic, and
entrySnapshot never appears in MCP results; corrupt history.json degrades
to "no track record" without affecting lifecycle operations; history
counter updates from a server callback and a CLI stop interleaved under
the transition lock lose no increments.

## T-DELIV — Context delivery and conformance

Conformance suite: a tiny Go probe helper (built by the test harness for
the host platform) that writes its argv, `GABS_*`/`GABP_*` env, and cwd
to a result file. Launch it through every chain shape from the ownership
table and assert each cell:

- direct launch (DirectPath/CustomCommand): all three channels arrive;
- script wrapper (`sh` on Unix, explicit `cmd.exe /c` on Windows) that
  forwards `"$@"`/`%*`: argv+env+cwd arrive through the hop; the
  forwarding-wrapper argv case verifies fully (argv[0] excluded from the
  payload digest);
- env-dropping wrapper (execs the probe with a scrubbed environment,
  simulating the Steam re-exec): probe sees no env; delivery report
  yields `partial`/not `verified`; adoption path exercised when the
  wrapper exits;
- filtering wrapper that forwards only the names listed in
  `GABS_FORWARD_ENV` (container simulation): the full declared context
  arrives and verification reports fully `verified`; a variant forwarding
  only the `GABS_*`/`GABP_*` names yields `partial` (context env channel
  fails), proving the verdict cannot claim more than was delivered;
- a filtering boundary that reintroduces a `GABS_ABSENT_ENV` name fails
  the env channel (isolation-violation test);
- detached wrapper (double-fork/`start`): status-hook liveness continues,
  probe still receives the injected context;
- `.app` bundle fixture on macOS: inner-binary resolution, env/argv
  arrive.

Verification semantics: fake bridge reports complete channels →
`verified`; managed env intact but a profile env key, cwd, or an
input-bound arg missing → `partial` with the failing channel named; no
`observed` field → overall `unknown` (never partial), zero
deliveriesVerified; unreported env names (welcome omits both lists) → env
channel unknown, overall at most partial; welcome-result `observed` field
(not hello) parsed and compared; cwd delivered through a symlinked path
(macOS /tmp) and a case-differing path on Windows still verifies via
canonicalization; a legacy relative workingDir yields cwd `unverifiable`,
overall at most `partial`, and no deliveriesVerified increment;
comparisons run against the persisted spawn-time digests and stay correct
after a CLI start + later server connect, after a server restart, and
after a config edit changed the entry; deliveriesVerified increments only
on fully verified; no live-attach code path reads bridge.json for
marker-stamped claims (assert by construction: outside the legacy
migration path, the reader exists only in doctor/diagnostics).

Credentials: a reclaimed launch mints a fresh token, and a delayed
process from the superseded launch connecting with the old token is
rejected and surfaced as stale_bridge_credential without touching the new
claim's attribution.

Endpoint & I/O survival: games_connect attaches from the claim's
persisted endpoint after a CLI start and after a server restart; a
CLI-started child writing to stdout after the CLI exits neither dies nor
blocks (log-file descriptors, no parent-owned pipes) and its output tail
remains available as evidence.

Windows: elevation error 740 maps to `spawn_failed` + hint; argv with
spaces/quotes/unicode round-trips through the standard quoting.

## T-MCP — MCP surface

Explicit/default profile; already-running with both profiles; unknown
argument rejection on every tool (regression: `timeoutSeconds`); input
errors; show/list metadata without templates; games_show exposes
maxLength (explicit default) and pattern; profiles_not_configured shape;
timeout range; every start outcome code reachable in a test; every stable
code in the list is reachable and every terminal branch maps to exactly
one code (exhaustiveness assertion over the outcome enum).

## T-CLI — CLI surface

--profile, repeated typed --input, duplicate-name error; parity with MCP
(Stages 1–4); CLI start terminates with `started_attachment_deferred` and
exits without GABP client after Stage 4, and a later server games_connect
attaches from the claim endpoint; stop/kill from snapshot after server
exit; repair --forget-runtime.

## T-ACC — Acceptance (issue scenario, neutral naming)

Two profiles → separate temp data dirs; launch each sequentially under
one ID; verify argv/env/cwd isolation and activeProfile; wrapper-exit
variant with status hook; stop via hook + verification; slow-shutdown
variant needing raised verifyTimeoutSeconds; rename a profile on disk and
launch it without restarting GABS or the client; early-crash variant
surfacing exit code + output tail end-to-end.

## T-GATE — Final regression gate (implementation-final gate)

- bridge disconnect racing a successful stop: the disconnect callback
  (launchID + connectionID) and the stop completion (launchID +
  operationID + phase) both land; neither invalidates the other;
- a long-lived attached bridge whose owning GABS process stays alive
  while a CLI performs stop: the fresh attachment lease + matching owner
  fingerprint counts as running-evidence; the CLI cannot clear the claim;
- byte-identical status hooks that answer differently based on
  GABS_PROFILE: all profiles probed with their normal variable; the
  divergent answers are observed;
- a CLI operation executor dying while the MCP/attachment owner remains
  alive: the operation normalizes as interrupted (executor gone), the
  attachment record survives untouched, and a late completion from the
  dead executor is rejected by operationID;
- all three spawning-recovery verdicts (running → active, stopped →
  removed, unknown → occupied);
- exhaustive Stage 1 branch-to-code coverage: every row of the table
  reachable, launch_spec_unresolvable carries JSON + filesystem paths,
  spawn_failed unreachable for resolvability failures;
- old bridge with no delivery report → overall unknown, zero
  deliveriesVerified; genuinely partial report (one channel mismatched or
  unreported) → overall partial; the two are never conflated.

## Gates

`go test ./...`, `go test -race ./...`, `make build`, public-docs
genericity scan, skill validation, clean working tree. The conformance
suite runs on all three OSes in CI; a cell that cannot run in CI (real
Steam) is covered by the simulation wrapper and documented as such.
