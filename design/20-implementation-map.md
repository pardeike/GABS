# Implementation map

The spec files (01–12) are the contract; this file maps them onto the
current codebase and pins implementation details the specs leave out.
Where the two disagree, the spec wins.

## Codebase touchpoints (verified against current source)

- `internal/config/games.go`
  - `GameConfig` (l.13-23): id, name, launchMode, target, args, workingDir,
    stopProcessName, gabpMode, description. Add: `Env`, `UnsetEnv`,
    `DefaultProfile`, `Profiles`, `LaunchInputs`, `Lifecycle` (profile and
    hook structs carry `Env`/`UnsetEnv` too; URL-mode validation rejects
    all of them).
  - `GamesConfig` (l.67-75): version, games, toolNormalization, apiKey,
    portRanges, timeouts, stripOutputSchema. Version is written ("1.0")
    but never validated — keep it that way.
  - `LoadGamesConfigFromPath` (l.84-163): plain `json.Unmarshal`, unknown
    fields ignored. Implement the unknown-key warning by decoding into
    `map[string]any` alongside the struct and walking against the known
    schema; error only inside the three new subtrees. Neither decoder
    detects duplicate object members (both keep the last value), so run a
    token-level scan first (`json.Decoder` walking tokens, tracking keys
    per object) and reject duplicates as `config_invalid` with the path
    before either decoded form is accepted. Warnings are part of the
    published snapshot so show/list/doctor can render them.
  - `GameConfig.Validate()` (l.233-272): extend with all rules from
    01-config-schema.md; errors carry JSON paths
    (`/games/<id>/profiles/x/args/1`).
  - `SaveGamesConfigToPath` (l.181-206): already tmp+rename; change file
    mode to 0600 (config may now hold env values).
- `internal/process/controller.go`
  - `LaunchSpec` (l.25): add ResolvedEnv (full map), Profile,
    ConfigRevision, Lifecycle (resolved hook snapshot), per-mode
    process-start budget.
  - `setupEnvironment` (l.175-200): currently appends `os.Environ()` last
    — replace with deterministic map merge per 02-launch-resolution.md
    (case-insensitive keys on Windows), stripping inherited
    `GABS_*`/`GABP_*` first, managed vars last (incl. `GABS_FORWARD_ENV`
    and `GABS_ABSENT_ENV`). Keep the Windows SystemRoot/WINDIR and
    SteamManaged `SteamAppId`/`SteamGameId` injections in the managed
    layer.
  - `Controller.Start` performs no Steam assistance. Stage 2 owns the macOS
    SteamManaged functional-readiness gate immediately before spawning. Its
    native `steamclient.dylib` calls execute only in a hidden child invocation
    of the current GABS binary; the parent passes the configured App ID, bounds
    child runtime/output, opens Steam once, polls with fresh children, and maps
    typed pipe/global-user/app-state/Steamworks-init readiness evidence.
    The gate restamps the fenced operation/process-start deadline on success so
    readiness time cannot consume the normal spawn/verification budget.
  - URL modes (`SteamAppId`/`EpicAppId`): the tracked child is the
    `open`/`xdg-open`/`cmd start` helper. Stage 4 must never count its
    liveness as workload evidence nor classify its expected prompt exit
    as `exited_during_start`; workload evidence is GABP/status
    hook/stopProcessName only, else `unobserved` at budget expiry. The
    open/ShellExecute prohibition applies only to propagation-capable
    modes — URL modes use OS openers by definition.
  - `Start` (l.97-147): after `cmd.Start()`, capture PID + process start
    time (fingerprint) before any waiting. Child stdout/stderr go to a
    per-launch log file (`~/.gabs/<id>/launch.log`, truncated at spawn,
    0600), opened by GABS and inherited as the child's descriptors — NOT
    parent-owned pipes. This is mandatory for CLI start: the CLI exits
    after Stage 4 while the game keeps writing, and a dead pipe reader
    means EPIPE/SIGPIPE can kill a logging game. GABS reads capped tails
    (16 KiB) from the file for `spawn_failed`/`exited_during_start`
    evidence; the server uses the same file (uniform path, no
    pipe-vs-file split). Docs note a chatty game grows the file until the
    next spawn truncates it. `waitForExit` already exists — feed its exit
    code into the start pipeline.
  - `IsRunning` (l.204) / `isRunningByName` (l.250): this name-fallback is
    the existing adoption mechanism — formalize it as the `adopted: true`
    path in Stage 4 rather than a silent equivalence. The current helper
    collapses enumeration/permission errors into "not found" — that must
    not survive: inspection failure maps to `unknown` (claim preserved),
    distinct from a successful lookup with no match (stopped-evidence).
  - `Stop` (l.325) / `Kill` (l.373): route through the lifecycle manager.
- `internal/process/runtime_state.go`
  - `RuntimeState` (l.22-32): add Phase (`starting|active|stopping|
    killing`), SpawnState (`preflight|spawning|spawned|failed`),
    LaunchMode + PIDRole (`workload|helper` — pinned so restart recovery
    never consults mutable config to decide whether the PID is liveness
    evidence), Profile, AppliedInputNames, ConfigRevision, PIDStartTime,
    Adopted, ContextDelivery (per-channel + overall verdict with
    unknown/unverifiable reasons, persisted when the welcome report is
    evaluated), LifecycleSnapshot (complete: command, args, env, unsetEnv,
    workingDir, timeouts, verifyTimeoutSeconds, running/stopped/success
    exit-code sets), BuiltinFallback (stopProcessName + graceful/force
    strategy, pinned), LastActionResult {action, outcome, exitCode,
    stderrTail, treeKillWarning, timestamp}, SchemaVersion (stamped at
    claim creation — the legacy-migration discriminator), Endpoint {port,
    token}, Operation {operationID, action, executorInstanceID,
    executorFingerprint, attemptStartedAt, deadline} (executor ≠
    claim/attachment owner: a CLI stop never takes ownership from the
    server holding the live bridge), Attachment {connectionID,
    ownerInstanceID, ownerFingerprint, observedAt, leaseDeadline —
    refreshed via the existing heartbeat while connected}, and for
    starting claims ProcessStartDeadline (governs unobserved
    reclamation).
  - `SaveRuntimeState` (l.80) uses plain `os.WriteFile` — change to
    same-directory tmp + rename, 0600; per-game dir 0700.
    `ClaimRuntimeState` (l.48) stays the cross-process start guard, but
    its mechanism changes: write the complete claim to a same-dir temp
    file (0600), fsync, `os.Link` into place — EEXIST preserves
    create-exclusivity, and lock-free readers can never see a partial
    initial claim (the old `O_CREATE|O_EXCL`-then-write could expose one;
    it also created 0644, unacceptable now that the claim carries the
    per-launch token). Existing runtime files are chmod-tightened when
    loaded or rewritten. The claim also persists the complete GABP
    endpoint (port + token): it is the normal attachment source for
    `games_connect` after a CLI start or server restart. Lease helpers
    (l.170-229) stay the ownership model.
- `internal/mcp/stdio_server.go`
  - Config pointer captured at l.504 and closed over by every handler —
    replace with a `ConfigStore` accessor; handlers fetch a snapshot per
    call and must not retain it.
  - Tool schemas (games_start l.736-753, games_connect l.1801-1817, etc.):
    add `additionalProperties: false` to every core tool; one shared
    argument-validation helper (known-key set + typed extraction) used by
    all handlers; unknown key → `unknown_argument` with path + sorted
    allowed names.
- `cmd`/`main.go`: `runServer` loads config once (l.232) — construct the
  ConfigStore here. CLI dispatch (l.290-326) gains start/status/stop/kill
  and `repair --forget-runtime`.

## New/changed components

1. **ConfigStore** (internal/config): content hash (sha256) → parse →
   validate → publish immutable snapshot (config-dir path, revision
   `sha256:<12 hex>`, warnings); cache last valid + last invalid (hash,
   error). RWMutex around publication; per-call hashing is a stat+read of
   one small file.
2. **Resolver** (pure functions): selection, input
   validation/substitution, arg ordering, env merge, cwd, hook resolution
   + placeholder substitution + PATH resolution of hook commands to
   absolute paths, static resolvability checks (Stage 1). Deep-copied.
   The only place launch context is computed; MCP and CLI both call it.
3. **Lifecycle manager** (internal/process): owns the five start stages,
   liveness rule, stop/kill + verification, restart recovery, and phase
   persistence (phase written before the action runs, result written
   after). Concurrency has two layers: an in-process per-game `TryLock`
   fast path (a second operation never blocks — it returns
   `operation_in_progress` from the persisted phase + deadline), and the
   cross-process **transition lock**: a stable per-game lock file
   (`transition.lock`, never deleted) locked via flock (Unix) / LockFileEx
   (Windows) around every read-decide-persist step — claim creation, phase
   writes, claim removal, lastActionResult. The lock is never held while a
   hook runs or anything waits. Fencing is **domain-scoped** (a universal
   check would false-invalidate: an attachment write must not kill a
   legitimate stop completion, nor a phase write a legitimate disconnect
   callback): random 128-bit `launchID` minted at claim creation (claim
   lifetime + ABA across delete/recreate — recreation invalidates all old
   work), `operationID` per start/stop/kill attempt, `connectionID` per
   GABP attachment lifetime, and `generation` used ONLY as the CAS
   revision for state writes. Under the lock: lifecycle completions
   validate launchID + operationID + action + expected phase, then merge
   into the latest claim even if attachment fields changed meanwhile
   (bridge disconnect during stop verification is the ordinary case);
   connect/disconnect/delivery callbacks validate launchID +
   connectionID, so an old disconnect never clears a newer connection.
4. **Hook runner**: `exec.CommandContext` with hook timeout; process group
   on Unix (Setpgid, kill via `-pgid`), Job Object on Windows (assign at
   create, TerminateJobObject on timeout); confirm direct child reaped;
   capture stdout/stderr capped 16 KiB; record treeKillWarning when a
   timeout kill fired. On Windows, reject non-executable hook commands at
   validation (users configure `cmd.exe` + `/c` explicitly for scripts;
   never implicitly wrap — avoids cmd quoting injection).
5. **Liveness**: exact 3-step precedence (GABP live / attachment lease →
   status hook → PID fingerprint / stopProcessName). Fingerprint: /proc
   start time (Linux), sysctl kern.proc (macOS), GetProcessTimes
   (Windows); mismatch = not our process.
6. **History store + failure classifier** (new):
   - `history.json` in the per-game dir: 0600, same tmp+rename discipline,
     survives claim removal and restart. Schema: per profile key ("" =
     unprofiled): {contextHash, workloadStarts, bridgeConnects,
     deliveriesVerified, cleanStops, lastSuccessAt, consecutiveFailures,
     lastFailure {outcome, class, at, inputNames}}; plus lastGood per
     profile {contextHash, entrySnapshot, at}; plus a per-game random
     bucket key for value digests. Missing counters in an older file
     default to 0 (no migration step). entrySnapshot may contain env
     values — it stays in this 0600 file and is printed only by
     `doctor --show-last-good`, never in MCP results. Every history
     read-modify-write runs under the per-game transition lock: atomic
     rename prevents torn reads but not lost updates, and a server-side
     delivery callback racing a CLI stop must not overwrite each other's
     counters.
   - contextHash = sha256 over canonical JSON of the resolved spec
     (target, mode, argv, config-layer env, cwd, resolved hooks). Managed
     env and launch inputs are excluded so caller-chosen inputs and
     unrelated edits never reset proof; hash granularity is the mechanism
     that keeps profile A proven while profile B is edited.
   - Input-combination buckets: per context, successes are additionally
     counted per sorted supplied-input-name set + sha256 of those inputs'
     declarations + a keyed digest (per-game bucket key) of the sorted
     name=value pairs, so scenario=arena and scenario=tutorial are
     distinct. Distinct value combinations per input set are capped (16,
     LRU eviction) to bound file growth. Classifier rule: an unproven
     combination adjusts confidence, not class — the failure keeps its
     outcome-implied class (crash → game) and the result carries the
     secondary note "first run with this input combination; the input is
     a candidate cause". `config` stays reserved for static
     binding/substitution failures. Editing a declaration changes its
     hash and resets only that input's buckets, leaving base proof
     intact.
   - Update points: Stage 4 verified → workloadStarts++,
     consecutiveFailures = 0, refresh lastGood; Stage 5 connected →
     bridgeConnects++; fully verified welcome delivery report →
     deliveriesVerified++; verified stop → cleanStops++; a terminal
     failure of an accepted attempt with a resolved context → lastFailure
     + consecutiveFailures++. `call`-class (pre-resolution) errors and
     `config_invalid` never mutate history — a caller typo must not
     distort proof or arm the edit notice.
   - Classifier: pure function (outcome, evidence, track record) →
     `call|config|environment|game|state` per 08-track-record.md.
     Proof-adjusted rules live here: unresolvable target/cwd/hook or
     `unobserved` on a proven context → environment; same on never-proven
     → config.
   - Edit notice: after a reload changes a game, the lifecycle manager
     compares the new resolved context hash against history; if the old
     context was proven and its lastFailure class was not `config`,
     attach the one-line notice to the game's next result. Fires once per
     edit, not on every call (record noticeShownForHash in history.json).
   - Result rendering: every failure result gets `causeClass` and one
     track-record line ("started 14×, last 2h ago; bridge connected
     14×"). nextActions templates are class-keyed; no non-`config`
     template may mention editing config — assert this in tests.
7. **Context delivery** (new):
   - `GABS_FORWARD_ENV`: computed in `setupEnvironment` as the union of
     the managed layer's key names and all config-context env key names
     (game + profile + supplied input bindings). Test asserts the list
     equals the actually-injected key set, so it cannot drift.
     `GABS_ABSENT_ENV`: the effective `unsetEnv` result names. The
     welcome comparison encodes present-with-value vs absent explicitly:
     a `GABS_ABSENT_ENV` name reported present (reintroduced by a
     container image) fails the env channel exactly like a wrong value;
     expected absences are part of the spawn-time digest record.
   - `bridge.json` gains optional `profile`, `configRevision`,
     `startedAt`, written at spawn under its existing lifecycle —
     **diagnostic only**. The live bridge contract stays env-only:
     nothing game-side may use the file for endpoint or context discovery
     (stale files mis-attribute generations), and INTEGRATION.md states
     this explicitly. Readers ignore unknown fields (verify current
     readers tolerate them). GABS-side attachment dials the endpoint
     persisted in the runtime claim, with exactly one exception: a claim
     **lacking the runtime schema marker** lets `games_connect` read the
     legacy `bridge.json` once, under the transition lock, validate by
     connecting + confirming liveness, and migrate the endpoint into the
     claim — the upgrade-with-running-game path. Endpoint absence is NOT
     the predicate: a freshly created claim (pre-endpoint-allocation) and
     an external snapshot both lack endpoints and carry the marker, so
     they can never enter this path. The first lifecycle touch also runs
     the full legacy normalization from 07-runtime-state.md (launch
     ID/generation minted, fallback pinned from the old claim's
     stopProcessName/PID, launch mode from current config recorded as
     the one sanctioned consult, normalizedFromLegacy flag).
   - Attachment record maintenance: the owning process persists
     connect/disconnect plus the lease refresh (piggybacked on the
     existing heartbeat) under the transition lock — see 04-liveness.md
     for the evidence semantics. Cheap: a few transitions per bridge
     lifetime plus low-frequency lease renewal.
   - MCP integer decoding: handlers use json.Number (decoder.UseNumber)
     end-to-end for launchInputs; float-rounded values above 2^53 are
     detectably non-integral and rejected.
   - Expected-context digests, persisted in runtime.json at spawn: a
     random per-launch salt, plus salted sha256 of the argv **payload**
     (elements after argv[0], length-prefixed encoding so element
     boundaries are unambiguous — argv[0] legitimately differs across
     wrapper hops), of the canonicalized cwd, and of each env value for
     keys named in `GABS_FORWARD_ENV`. Raw input values never persist;
     digests cannot reconstruct them. Salting and hashing happen only on
     the GABS side — the bridge always reports raw observed values.
   - GABP direction: GABS is the client and sends SessionHelloParams; the
     game-side bridge is the server and replies with
     SessionWelcomeResult. The optional `observed` field therefore goes
     in the **welcome result**, with an explicit three-state env encoding
     so SDKs cannot diverge: `observed: {argv: [...], cwd: "...",
     envValues: {<name>: value}, envAbsent: [<name>, ...]}`. A name in
     `envValues` was observed with that value; a name in `envAbsent` was
     checked and is absent; a name (from
     `GABS_FORWARD_ENV`/`GABS_ABSENT_ENV`) in neither list was *not
     reported* and makes the env channel `unknown` — never a pass, never
     a fail. No follow-up method — welcome time is sufficient and keeps
     the wire surface at one field. Confirm the welcome decoder tolerates
     unknown fields; touchpoints beyond this repo: the GABP session
     schema and the bridge SDKs (e.g. RimBridgeServer) to populate it —
     optional for all of them. GABS hashes and compares per channel
     (argv, cwd canonicalized, managed env, context env) against the
     persisted digests — never against current config, which keeps it
     correct after a CLI start, a server restart, or a config edit. The
     aggregation matrix in 03-context-delivery.md is normative. Stored on
     the runtime claim, reported in start/status, `deliveriesVerified++`
     only on fully verified.
   - Cwd canonical helper (used for both spawn digest and reported
     value): `filepath.Abs` + `filepath.EvalSymlinks` + case/separator
     folding on Windows; on canonicalization error the channel is
     `unknown`.
   - Per-launch credential: mint a fresh GABP token on every
     runtime-claim creation, even with `resetEndpoint: false` (the flag
     governs only port reuse); persist it with the claim; the GABP server
     rejects a connection presenting a non-current token and the
     rejection surfaces as `stale_bridge_credential` in status/start
     diagnostics.
   - macOS: resolver maps a `.app` directory target to
     `Contents/MacOS/<CFBundleExecutable>` (parse `Info.plist`; fall back
     to the bundle name); doctor checks `com.apple.quarantine` xattr and
     warns about translocation with relative paths. Never `open` for
     propagation-capable modes.
   - Windows: rely on Go's standard argv quoting (CommandLineToArgvW
     round-trip); detect `ERROR_ELEVATION_REQUIRED` (740) from
     CreateProcess and map to `spawn_failed` with the no-elevation hint.
     Never ShellExecute for propagation-capable modes.
   - Env-block/command-line size: two distinct thresholds. Exceeding the
     platform hard limit (32 KiB env block on Windows, OS argv limits) is
     a structured pre-spawn **error** at Stage 2 naming the oversized
     part — never allowed through to an opaque CreateProcess/E2BIG
     failure. Unix accounting follows the running kernel, not a shared
     heuristic: Darwin uses `kern.argmax` and charges strings, argv/envp
     pointer entries, both NULL terminators, and alignment; Linux derives
     the combined ceiling from the current `RLIMIT_STACK`, applies the
     32-page per-string limit using the current page size, and charges
     pointer entries plus the kernel's executable-path copy. If an exact
     hard limit is unavailable, do not invent a smaller rejecting limit.
     An optional conservative warning threshold below the hard limit may
     additionally warn, but it can never become a pre-spawn refusal.

## Behavior details worth pinning

- Start sequence: snapshot → resolve (Stage 1 errors here; resolution is
  pure and complete BEFORE claiming, so the claim can be written whole) →
  claim: the atomic create-exclusive publication IS the complete
  pre-spawn snapshot — schemaVersion, phase=starting,
  spawnState=preflight, operation {operationID, action, executor,
  attemptStartedAt, deadline}, profile, hook snapshot, owner fingerprint
  — never a bare marker. On ErrRuntimeStateExists: read the existing
  claim's operation timing first → operation_in_progress if an attempt is
  in flight with a live owner; else liveness on the claimed context —
  running→already_running, unknown→blocked_unknown_state, stopped→remove
  + retry claim once; dead owner + spawnState=preflight → remove safely
  (process creation was provably never attempted). → probes, holding the
  claim (every profile's resolved status hook concurrently, each with
  that profile's normal GABS_PROFILE set — no dedupe shortcut;
  stopProcessName — a unique match, or a running hook probe, rewrites our
  own claim under the transition lock into the external snapshot — phase
  active, source external, observed or unknown profile, detected
  profile's hooks or the built-in name/PID fallback pinned — before
  returning external_instance_detected) → endpoint alloc persisted into
  the claim → spawnState=spawning under the transition lock → spawn →
  persist spawnState=spawned + PID/fingerprint + expected-context digests
  (or failed) → Stage 4 verification under the per-mode budget → Stage 5
  GABP wait (existing timeout semantics, background attach preserved).
  Every asynchronous publication is fenced by its domain identities
  (06-stop-lifecycle.md) — there is no universal counter guard.
- `unobserved` keeps the claim in phase=starting. Passive reconciliation
  only: background bridge attach success promotes to active; any later
  status/start/stop observation promotes (running seen) or clears
  (definitively stopped). No poller goroutine.
- Steam-client handling: URL modes and non-macOS behavior retain the
  best-effort process-name advisory. On macOS SteamManaged, prove an
  app-specific client-library pipe/global-user/app-state/API-init proof before spawn; failure
  is typed `store_client_not_ready`, releases the claim, and starts nothing.
- Stop sequence: TryLock → read snapshot → persist phase=stopping (+
  deadline) → resolve action (profile hook → game hook → built-in) → run
  under timeout → verify (status hook poll ≤ verifyTimeoutSeconds,
  default 15, or process-exit evidence; each individual probe's context
  is clipped to min(statusHookTimeout, remaining verification budget), so
  a 60 s hook started 3 s before the window ends gets 3 s — operations
  stay strictly bounded and the persisted deadline is honest) → verified:
  reap launcher child if live, close bridge, remove claim; failed: phase
  back to active + LastActionResult persisted; unverified: claim kept,
  termination_unverified.
- Kill during stopping: TryLock fails → operation_in_progress with the
  stop attempt's deadline. Kill never runs the stop hook.
- games_status takes no operation lock: read runtime.json, run bounded
  probes, report phase + evidence + lastActionResult. Multi-game:
  errgroup, per-hook timeout. Lock-free reads are safe because BOTH
  publication paths are atomic: later saves via tmp+rename, and the
  initial claim via tmp+fsync+`os.Link` into place (link fails with
  EEXIST when a claim exists, preserving create-exclusivity; a failed
  write publishes nothing; the temp file is always unlinked). Fallback
  for filesystems without hard links: O_EXCL+write performed under the
  transition lock, with status readers on that platform taking the lock
  for the (millisecond) read.
- Interrupted-phase normalization: on recovery with phase
  stopping/killing whose executor is provably gone or whose pinned
  deadline expired, under the transition lock: record
  lastActionResult{outcome: interrupted}, clear Operation, then phase per
  liveness (running → active; stopped → remove claim; unknown → active
  with unknown reported). A dead attempt never renders as
  operation_in_progress, a late completion from the old executor is
  rejected by operationID, and a fresh stop/kill is allowed immediately.
- Cross-process reconciliation: before trusting in-memory controller
  state, handlers cheaply re-stat the claim file (mtime/existence); a
  divergence (CLI removed/rewrote it) triggers fresh liveness evaluation.
  Every asynchronous publication (hook completion, bridge callback,
  delivery report) additionally reacquires the transition lock and
  validates its domain identities against the persisted claim before
  writing — launchID + operationID + action + phase for lifecycle
  completions, launchID + connectionID for attachment callbacks — because
  claim existence plus fresh liveness can never prove a callback belongs
  to the current launch (the claim may have been deleted and recreated
  while the callback was pending), and a universal generation check would
  false-invalidate legitimate work.
- Warnings surface: snapshot warnings render in games_show (full, per
  game), games_list structured (count), doctor (full + hints, including
  the docker stopped/error-conflation lint when a status hook command
  basename matches known conflating tools — advisory only).
