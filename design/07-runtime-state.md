# Runtime state

`runtime.json` (existing file, extended) records atomically (same-directory
temp file + rename, 0600; directory 0700). The full field contract:

- `phase` (`starting|active|stopping|killing`), the immutable random
  launch ID, and the monotonically increasing transition generation;
- `source` (`gabs`, or `external` for an adopted external instance);
- selected profile (or none) and applied launch-input **names** (never
  values);
- config revision used;
- the effective launch mode and the PID's role (`workload` for direct
  modes, `helper` for URL modes) — pinned so restart recovery applies the
  right liveness rule without consulting mutable config: a helper PID
  never counts as workload liveness, a workload PID is authoritative;
- `spawnState` (`preflight|spawning|spawned|failed`);
- workload PID + start-time fingerprint (when available) and `adopted`
  flag;
- the per-launch `contextDelivery` verdict (per-channel and overall, with
  unknown/unverifiable reasons) once reported, so `games_status` renders
  it after a restart without re-deriving or silently downgrading it;
- the resolved lifecycle hook snapshot — every field that affects
  execution *or result interpretation*: absolute command, args, env,
  `unsetEnv`, workingDir, timeouts, `verifyTimeoutSeconds`, and the
  running/stopped/success exit-code sets, all after placeholder
  substitution (a custom stopped code must not degrade to `unknown` after
  a restart or profile edit);
- the built-in fallback contract, pinned like the hooks: effective
  `stopProcessName` and the graceful/force strategy. An adopted or
  URL-mode launch may depend on nothing else once the helper exits, and
  recovery is forbidden from consulting current config — external
  snapshots pin this too;
- a runtime schema version stamped into the file at claim creation — the
  legacy-migration discriminator. Endpoint absence is **not** the
  predicate: a new claim between creation and endpoint allocation, and an
  external snapshot, both lack an endpoint and must never match the
  migration path;
- the in-flight operation: `operationID`, action kind, **executor**
  instance ID and PID/start fingerprint (the executor is distinct from
  the claim or attachment owner — a CLI stop executes without taking
  ownership from the server that owns the live bridge), attempt start
  time, and pinned absolute deadline (what `operation_in_progress`
  renders, from any process, after any restart), plus — for `starting`
  claims — the immutable process-start deadline that governs
  `unobserved` reclamation;
- the GABP endpoint (port) alongside the per-launch token — the claim is
  the normal attachment source for `games_connect` after a CLI start or a
  server restart; `bridge.json` stays diagnostic;
- the attachment record: `connectionID`, attachment-owner instance +
  process fingerprint, `observedAt`, and a renewable lease deadline
  refreshed while connected; running-evidence for other processes only
  while fresh and fingerprint-matched;
- non-reversible expected-context digests (per-launch salt; salted hashes
  of the argv payload excluding argv[0], the canonical cwd, and each
  `GABS_FORWARD_ENV`-named env value), pinned at spawn for delayed
  delivery verification;
- `lastActionResult` (see 06-stop-lifecycle.md);
- the existing ownership/lease fields.

Stop/kill/status use this snapshot: callers never repeat or guess the
profile, and editing or deleting a profile in config never changes how the
already-running launch is observed or stopped. An active game removed from
config stays addressable by ID (`configured: false`) — and aggregate
discovery must surface it: no-argument `games_status` unions configured
entries with persisted runtime claims, reporting `configured: false` and
the persisted phase, so a fresh agent can find and stop a launch whose
config entry was edited away. `games_list` remains the configuration
surface; the runtime surface is status.

## External snapshots

An external snapshot (`source: external`) truthfully lacks everything GABS
never created: no endpoint, no token, no context digests, no process-start
deadline; `appliedLaunchInputsState: unavailable` (not empty) and
`contextDelivery: unknown`. It supports status, stop, and kill only —
`games_connect` reports attachment `unavailable`, because the workload
never received this GABS's bridge environment. Its stamped runtime schema
version keeps it out of the legacy-migration path. Handlers must treat
these fields as absent-by-nature, never as missing data to be filled from
current config.

## Restart recovery

Restart recovery is just liveness: read the snapshot, evaluate the rule
(04-liveness.md). Stopped → clear. Running → resume management (bridge
starts disconnected; `games_connect` reattaches). Unknown → report, keep
claim, block starts. A snapshot found in phase `stopping`/`killing` after
a restart is not replayed. Once the attempt's **executor** is provably
gone or its pinned deadline has expired, recovery **normalizes** the
interrupted attempt under the transition lock: the orphaned attempt is
recorded as `lastActionResult` (outcome `interrupted`, facts unknown where
unknown), the `Operation` field is cleared, and the phase follows liveness
— running → `active`; stopped → claim removed; unknown → `active` with the
unknown verdict reported by status. Status therefore never reports
`operation_in_progress` for a dead attempt, a late completion from the old
executor is rejected by its `operationID`, and a fresh stop/kill is
immediately permitted — retrying is exactly what the idempotent-hook
contract exists for. Escape hatch:
`gabs games repair <id> --forget-runtime` prints the evidence and removes
the claim after confirmation. No MCP tool can forget state.

Spawn-state recovery cases are defined in 05-start-pipeline.md, Stage 3.

## Legacy claims (upgrade with a running launch)

Upgrading GABS while a pre-upgrade launch is still running is supported by
a narrowly scoped migration: when the runtime claim lacks the runtime
schema marker (the explicit discriminator — endpoint absence is not the
test, since a freshly created claim or an external snapshot also has
none), `games_connect` may read the legacy `bridge.json` endpoint once,
under the transition lock, validate it by actually connecting and
confirming liveness, and migrate it into the claim — after which the file
returns to diagnostic-only status. This is the sole live-attach read of
`bridge.json`, and it can never apply to a marker-stamped claim. The first
lifecycle touch (connect, stop, kill, or a start's duplicate check — not
read-only status) also performs a one-time **full normalization** under
the transition lock, because the legacy claim lacks far more than the
endpoint — phase, launch ID, generation, fingerprint, and pinned
snapshots: stamp the schema version, mint a launch ID and generation
(fencing valid from then on), set phase `active` and profile unprofiled,
pin the built-in fallback from the legacy claim's own `stopProcessName`
and PID (no fingerprint exists, so that PID remains weak evidence), and —
the single recorded exception to never-consult-config — take the launch
mode/PID role from the current entry, persisting
`normalizedFromLegacy: true` plus the revision used. Before normalization,
a legacy claim supports only degraded status and repair.

## Privacy qualifications

Launch inputs are not a secret store. GABS-authored state, results, and
log fields never store raw input values — only salted or keyed digests —
but two qualifications are stated honestly: digests of low-entropy values
(booleans, enums, bounded integers) can be brute-forced by a local reader
of these 0600 files, and captured process output is returned unredacted,
so a wrapper running `set -x` or a game echoing its startup configuration
places values into evidence. Secrets do not belong in launch inputs.
