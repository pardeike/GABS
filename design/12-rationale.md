# Rationale, worked example, and rejected alternatives

This file exists so the implementing agent understands *why* the design is
shaped this way — and, critically, what was considered and rejected, so
rejected ideas are not reinvented in good faith. Earlier drafts
(ProfileFeature-v1 … v4 in git history) show the full evolution: a
machinery-heavy draft (v3) was deliberately replaced by contracts, then six
review rounds added back only the mechanisms that proved load-bearing,
each with a recorded reason.

## Worked example (the issue's case)

One `adventure`-style entry, `DirectPath` to a wrapper script, one profile
per isolated data root passing `--data-root <path>` (generic args — no
`dataDir` concept), and lifecycle hooks invoking the container runtime with
`name-${profile}`. The status hook is a three-line wrapper that first
checks the container runtime is reachable (exit 2 = unknown if not), then
maps container running/absent to exit 0/1 — this wrapper ships in the docs
as the canonical pattern. `games_start` with `profile: "combat-test"`
launches that container; the wrapper may exit while the status hook keeps
reporting running; `games_stop` runs the stop hook with the snapshotted
profile and verifies via the status hook. The duplicated per-profile game
IDs collapse into one entry.

## Profile design assessment

**Why this shape.** The alternatives were: separate game IDs per context
(status quo — clutters discovery, splinters identity, no shared tooling),
free-form launch parameters (an uncontrolled escape hatch), config
templating/includes (complexity without a runtime concept), or a full
workspace/instance system (far beyond the need). Profile-as-named-overlay
gives tooling a stable identity while context varies. The load-bearing
line is *lifecycle visibility*: anything status/stop/kill must know to
find the workload is a profile; anything that only changes what the
workload does at startup is a launch input. That line is principled — it
is exactly "what must survive in the runtime snapshot" — and it decides
every borderline case mechanically.

**Future-proofing.** Profiles are a pure config concept; runtime identity
remains the game ID. That orthogonality is what keeps the design open:

- *Concurrent instances* (client + server, two profiles at once) are not
  supported — the constraint is structural (one GABP endpoint, one tool
  namespace, one runtime dir per game ID), not a profile-schema limit. If
  concurrency is wanted later, it is an additive runtime-side change
  (instance handles: per-instance runtime dir/endpoint,
  instance-qualified tool routing) with **no change to the profile
  schema**. Until then, concurrency remains the separate-game-IDs pattern,
  stated plainly in docs.
- *Dynamic environments* (a fresh isolated profile per test run or PR) are
  handled by config editing plus hot reload: an agent adds a profile,
  starts it, tears it down, removes it — no restart anywhere. If that
  proves clunky, parameterized profile templates (a declared variable
  usable in hooks, i.e. lifecycle-visible by declaration) are an additive
  extension that preserves the lifecycle-visibility line. Not built now.
- *Orthogonal context dimensions* (data-root × renderer) multiply into
  profiles only when both dimensions are lifecycle-relevant; otherwise the
  second dimension is a launch input. The M×N enumeration case is
  accepted — inheritance/composition between profiles was considered and
  rejected as complexity that mostly serves hypothetical configs.
- *Remote/wrapped control* needs no new concepts: hooks are commands, so
  ssh/kubectl/podman wrappers already cover remote and non-Docker
  runtimes.

**Known limits, stated honestly.** URL launch modes get no profiles at all
(all three delivery channels are severed — the exclusion is principled,
per the chain-ownership table in 03-context-delivery.md); Steam titles
that force a relaunch through the Steam client can drop injected context —
no longer *silently*, since adoption plus delivery verification exposes
it, and `steam_appid.txt` or Steam launch options work around it, but GABS
cannot fix it; and the single-active-instance rule is the one place the
issue author's container-per-profile setup is more capable than GABS's
model — containers isolate perfectly well concurrently, and GABS still
serializes them per ID. That is the price of one bridge identity per game,
and the reserved instance-handle extension is the honest path out if it
ever matters.

**Edge-case checklist:** container-per-profile with profile-aware stop ✓;
wrapper exits while game lives ✓; test automation via CLI + typed inputs
✓; dev loop with config hot-reload ✓; remote control via hook wrappers ✓;
Steam titles ~ (SteamManaged with re-exec caveat; URL modes excluded);
concurrent profiles ✗ (separate IDs; reserved extension).

## Decisions and rejected alternatives (do not re-add)

- **`exited_during_start` cause class: no launch-mode heuristic, no
  classification-only config flag.** A post-spawn exit is `game` by the
  evidence-based default (05-start-pipeline.md §"Why exited_during_start is
  always game"). GABS observes only the first process it creates and cannot
  distinguish a game binary from a user-owned wrapper/container launcher.
  Two signals were considered and **rejected**: (1) a launch-mode heuristic
  (`CustomCommand`/`SteamManaged` → environment) would misclassify the common
  case — a real game crash under those modes — as an environment problem,
  sending agents to "fix the environment" for a game-side bug; false
  attribution is worse than the honest default. (2) A classification-only
  config flag (`launchKind: container`) would push a cause GABS cannot observe
  onto the user to declare, and would drift the moment the same command is
  reused for a non-container target. The wrapper's own stderr — which says what
  actually failed — is preserved in `outputTail`, and the guidance tells the
  caller to read it. (An OS process-**creation** failure stays `spawn_failed` /
  environment.)
- **No public operation IDs, journal, or background operation model.**
  Operations are bounded by configured hook timeouts plus the verify
  window; progress is observable via the persisted `phase`, and the
  single `lastActionResult` field replaces the journal. An *internal*
  per-attempt `operationID` does exist — purely as a domain-scoped
  fencing identity alongside `launchID` and `connectionID` — but it is
  not a queryable API concept and nothing is journaled by it.
- **No at-most-once action machinery** (invocation states, hazard
  tracking). Replaced by the idempotent-hook contract plus tree-kill, the
  recorded straggler warning, and the pre-start probes as backstop.
- **No probe-budget machinery.** When no claim exists, GABS does probe all
  profiles concurrently before a start — the one-active-instance
  invariant demands it — but each probe is bounded only by its own hook
  timeout: no derived-budget calculus, no probe planning, no probing
  outside the pre-start moment. (A byte-identical-spec dedupe shortcut
  was tried and removed in final review: a hook can read `GABS_PROFILE`
  directly, so identical specs are not equivalent invocations. An
  explicitly declared game-scoped optimization may be considered later.)
- **The transition lock is the one concession to new coordination
  machinery.** The claim + idempotent hooks alone permit last-writer
  state resurrection between CLI and server (a failed stop finishing
  after a kill re-persisting `active`). A millisecond-scale advisory lock
  around state writes plus domain-scoped fencing closes that; everything
  held-across-waits from the v3 draft stays rejected.
- **No `GABS_PROFILE_STATE` marker or environment-inspection recovery.**
  The handshake delivery report proves propagation positively; adoption
  plus a missing bridge (or an unverified delivery) is the
  counter-signal.
- **No pre-launch "prepare" hooks.** A wrapper script subsumes them: it
  materializes per-profile state (config files, registry keys,
  directories) under the user's control and is testable without GABS.
- **No `open`, no ShellExecute fallback, no elevation — for
  propagation-capable modes.** (URL-only modes use OS URL openers by
  definition and promise nothing.) Each would silently sacrifice env
  injection, argv, or PID tracking to make a launch "succeed"; GABS
  prefers a precise failure over an untracked success.
- **No migration planner / JSON-patch emitter.** Validation errors,
  `profiles_not_configured`, doctor, and the documented recipe +
  checklist are the migration story.
- **No schema version bump.** New fields are optional; strictness applies
  where it has no legacy blast radius.
- **No new MCP tools** (`reload_config`, `doctor`, `recover` rejected);
  value folded into existing results, the CLI, or automatic behavior.
- **No workload auto-retry and no launcher-UI automation.** GABS reports
  evidence and next actions; the operator (or agent) decides whether to make
  another game-start attempt. The macOS SteamManaged pre-spawn readiness gate
  is not a workload retry: one accepted start operation may open Steam once and
  poll its low-level client, prove a process-local Steamworks API init for the
  declared App ID, query read-only app state, and shut the API down, until the
  caller's bounded readiness deadline. It never creates a workload
  process before the app-specific proof.
- **Required `defaultProfile`** eliminates the `profile_required` error
  path; the "explicit profile always" mode was considered again and
  rejected — callers wanting that discipline pass a profile explicitly.
- **No blocking on possible hook stragglers.** Warning + probe backstop is
  proportionate; a hard block would trade a rare hazard for a common
  operational annoyance.
- **No config write-protection, lock files, or `protected: true` flags.**
  GABS does not own the editor and cannot enforce file immutability; a
  declared protection flag would be flipped by the same agent it is meant
  to stop, and it rots. Protection is evidence-based: the earned track
  record, `causeClass` attribution, visible proof reset on edits, and the
  skill contract.
- **No automatic restore of last-known-good config.** Doctor can show it;
  a human decides. Auto-restore would let GABS and an agent fight over
  the file.
