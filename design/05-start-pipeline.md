# Starting a game

Start is the fragile path: config, filesystem, store launcher, DRM,
anti-cheat, network, and bridge can each fail independently. The pipeline
has five named stages; every stage has defined outcomes, and each failure
carries evidence and a next action. GABS never auto-retries.

## Stage 1 — Validate (nothing has run)

Structured errors, no side effects: `config_invalid` (with the exact
parse/validation error and whether last-known-good is in effect — starts
are blocked on stale config), unknown/ambiguous game ID,
`profiles_not_configured`, `profile_not_found`, `launch_input_not_declared`,
`launch_input_invalid`, `launch_mode_incompatible`, `timeout` out of range.

Static resolvability is checked here so misconfiguration fails fast with a
path instead of a spawn-and-die: DirectPath/CustomCommand target exists and
is executable (or PATH-resolvable); SteamManaged resolved executable
exists; effective working directories exist; hook commands resolvable. Each
failure names the offending JSON path and the resolved filesystem path it
checked.

The Stage 1 branch-to-code mapping is exhaustive — every terminal path has
exactly one code:

| Stage 1 branch | Code |
| --- | --- |
| config parse / schema / semantic / duplicate-member failure | `config_invalid` |
| unknown game ID | `game_not_found` |
| ambiguous game reference | `ambiguous_game_reference` |
| profile requested on unprofiled game / unknown profile | `profiles_not_configured` / `profile_not_found` |
| undeclared / invalid launch input | `launch_input_not_declared` / `launch_input_invalid` |
| mode rejects profiles/inputs/env | `launch_mode_incompatible` |
| `timeout` outside 1–3600 | `timeout_out_of_range` |
| target / cwd / PATH / hook unresolvable (JSON path + filesystem path) | `launch_spec_unresolvable` |

`spawn_failed` is reserved for failures after a valid resolved spec reaches
OS process creation — never for resolvability.

## Stage 2 — Preflight (claim and external checks)

- Claim the runtime state. Publication is atomic *with its full content*:
  the complete claim is written to a same-directory temp file and
  hard-linked into place — the link fails if a claim exists (preserving
  create-exclusivity), a failed write publishes nothing, and a lock-free
  status reader can never observe an empty or partially written claim
  (plain `O_EXCL`-then-write would expose exactly that window). The
  published content is the **complete pre-spawn snapshot**, never a bare
  marker file: schema version, phase `starting`, `spawnState: preflight`,
  operation kind / attempt start / pinned deadline, selected profile,
  resolved hook snapshot, and owner fingerprint — everything is known
  before claiming, because resolution is pure and already done. Two
  consequences: a concurrent start during preflight reads the operation
  timing and correctly returns `operation_in_progress` (not
  `blocked_unknown_state`), and a crash in this window leaves a claim that
  dead-owner recovery can **safely remove**, because
  `spawnState: preflight` proves process creation was never attempted — no
  liveness puzzle, no manual repair. If a claim exists, evaluate liveness
  for the **claimed** context: running → `already_running` (with
  `activeProfile` and `requestedProfile`); unknown →
  `blocked_unknown_state` (evidence + repair guidance; a claim means GABS
  started something, so uncertainty blocks); stopped → clear the stale
  claim and proceed.
- When no prior claim existed, probe before spawning (holding the fresh
  claim, which concurrent starts already see as `operation_in_progress`) —
  the backstop for lost claims and straggler hooks. GABS probes **every**
  configured profile's resolved status hook concurrently, each under its
  own timeout. Probing only the requested profile — or trusting a single
  "global" hook by author convention — would miss a still-running profile
  A while starting B and break the one-active-instance invariant; the
  schema example in 01-config-schema.md has a game-level hook taking
  `${profile}`, so "game-level" does not mean profile-independent, and no
  unenforced author obligation may carry the invariant. There is **no**
  single-probe shortcut: each probe runs with that profile's normal
  `GABS_PROFILE` set, because even a byte-identical command/args/env
  configuration is not equivalent across invocations — the hook can read
  the managed variable directly, and a probe invoked differently from
  normal hook execution yields unrepresentative answers. A few bounded
  probes are cheaper than a violated invariant; an explicitly declared
  game-scoped optimization can be considered later. Any running result →
  `external_instance_detected`, refusing the start. Attribution then
  decides what control is offered: exactly one profile probe running →
  GABS rewrites its fresh claim into an **external snapshot** (phase
  `active`, `source: external`, that profile, its resolved hooks pinned
  from the current config) so status/stop/kill can act on the instance by
  ID, including after a restart. More than one profile running → report
  all names, persist no snapshot, and direct the caller to resolve
  manually — GABS never guesses among candidates. Probes returning unknown
  → proceed, with a warning listing the unprobeable profiles (no claim
  means GABS owns nothing yet; blocking here would brick starts on a
  misconfigured hook — the asymmetry with the claimed case is deliberate).
  Also run the existing `stopProcessName` check where applicable: a unique
  name match is treated like a game-level detection — external snapshot
  with `observedProfile: unknown` and the built-in name/PID fallback
  pinned as its control mechanism, so the refused start leaves the
  instance manageable (status/stop/kill) after a restart or config edit.
  Colliding name matches are reported without a snapshot, like ambiguous
  hook results.
- Allocate the GABP endpoint (port); failure is a structured error and the
  claim is released. The fully resolved spec is also checked against
  platform argv/env-block size limits here — a structured error naming the
  oversized part beats an opaque `E2BIG` at exec.
- Store-client readiness is launch-mode and platform specific. URL modes keep
  the process-presence advisory because `steam://` starts the client itself.
  SteamManaged on macOS is stricter: before the direct executable spawn, GABS
  must prove that the installed Steam client library can create an IPC pipe,
  connect a global user, complete a process-local Steamworks API initialization
  for the explicit App ID, and through that initialized API observe the
  configured app as both subscribed and installed. A running
  `steam_osx` process or connected global user alone is not proof — both can
  exist before Steam has loaded the user's app/library state, which makes a
  direct game process start without usable Steam identity and breaks
  integrations that depend on Steam. GABS probes immediately, asks macOS to
  open Steam once when needed, then retries fresh probes until the independent
  caller readiness deadline. Each native probe runs in a short-lived hidden
  GABS child so a client-library hang, assertion, or crash cannot take down the
  lifecycle owner. It uses Steam's installed `steamclient.dylib`, passes the
  declared `SteamManaged` target to the helper as explicit non-secret probe
  data, sets the helper's process-local App-ID variables from that validated
  value before loading Steam libraries, calls the low-level client interface,
  and performs `SteamAPI_InitFlat`, read-only app-state queries, then
  `SteamAPI_Shutdown` using Steam's bundled API library. It
  never derives identity from ambient App-ID variables, launches the workload,
  or requires online/logged-on state. The game is not spawned until this
  app-specific initialization proof succeeds.

  A readiness timeout returns `store_client_not_ready` with
  `reason: readiness_timeout`, the furthest readiness stage, elapsed/deadline
  evidence, `retryable: true`, and `processStarted: false`. If no probe ever
  loads and invokes the required client interface, the same code uses
  `reason: probe_unavailable` and `retryable: false`. Both are
  `causeClass: environment`, release the fresh preflight claim, create no
  unobserved runtime, and do not mutate launch history because no workload was
  attempted. On success the operation/process-start deadline is restamped
  under the original fencing identity with a fresh normal Stage 3/4 budget.
  The completed readiness proof is authoritative for the readiness timeout:
  the restamp does not reject the same fenced operation merely because
  transition/persistence overhead carries wall-clock time past the old
  readiness deadline. A successor that changed the launch or operation
  identity first still wins and cannot be overwritten. `timeout` therefore
  caps readiness independently, then retains its existing full GABP-wait
  meaning. Windows/Linux SteamManaged behavior is unchanged.

## Stage 3 — Spawn

Immediately before process creation, `spawnState` advances to `spawning`
under the transition lock; immediately after, to `spawned` with the
PID/fingerprint, or `failed`. Dead-owner recovery therefore has three
honest cases: `preflight` → remove safely (creation never attempted);
`spawned`/`failed` → normal liveness; `spawning` without a PID → the
crash-during-spawn window, resolved by the **normal liveness rule** over
the claim's pinned evidence (status hook, attachment record, built-in
fallback), exactly like every other recovery path: running evidence
promotes to `active`, definitive stopped evidence removes the claim, and
only genuinely unknown evidence preserves it occupied.

OS-level process creation fails (missing interpreter, bad architecture,
permission denied, macOS Gatekeeper/quarantine block) → `spawn_failed` with
the OS error; claim released. From spawn onward the child's stdout/stderr
go to a per-launch log file (truncated at each spawn) whose descriptors
remain valid after any GABS process exits — never parent-owned pipes, which
would turn a CLI exit into EPIPE for a logging game. GABS reads a capped
tail (16 KiB) from it — the single best "why did it die" evidence, included
in failure results.

## Stage 4 — Workload verification

Bounded by a process-start budget (defaults: 10 s for
DirectPath/CustomCommand/SteamManaged, 60 s for the URL modes, configurable
under the existing `timeouts.startup` settings). For URL modes the tracked
child is the URL-opener helper, not the workload: its liveness never counts
as workload evidence and its prompt exit is *expected* — it is neither
`exited_during_start` nor a reason to release the claim. URL-mode
verification relies solely on GABP, status hooks, or `stopProcessName`;
none of those by budget expiry → `unobserved`, exactly as documented.
Outcomes:

- **Verified running** — tracked PID alive, or status hook reports
  running, or `stopProcessName` match. Continue to Stage 5.
- **Adopted** — the tracked PID exited but the status hook or process name
  observes the workload. This is normal for launcher chains and for Steam
  titles that re-exec themselves through the Steam client. Verified
  running, with `adopted: true` and a warning: injected args/env
  (including `GABS_PROFILE`) may not have survived the relaunch — Steam
  relaunches with its own configured options. A subsequent bridge
  connection proves at least the managed environment survived (the live
  contract is env-only), and a `verified` delivery report proves the full
  context did; adoption with *no* connection is the signal to suspect
  context loss. Docs: for games that force the Steam re-exec, either place
  `steam_appid.txt` beside the executable (disables the restart, keeping
  the launch a direct child) or configure the context via Steam launch
  options (`%command%` wrapper) — never rely on injection surviving the
  relaunch.
- **Exited during start** — child exited without adoption:
  `exited_during_start` with exit code and output tail; claim released.
  Typical causes surfaced in the hint: crash, missing/corrupt data or
  save, mod loader failure, DRM refusing to start outside its launcher,
  anti-cheat rejecting a modified process, online login required. **Cause
  class is `game` by the evidence-based default** (08-track-record.md): a
  process GABS created and then observed exit is attributed to the workload.
  GABS observes only the *first* process it creates and cannot reliably tell
  a game binary from a user-owned wrapper/container launcher, so a
  post-spawn exit is never re-attributed to the environment on the basis of
  launch mode, target shape, or the status hook — none of those is cause
  evidence. The wrapper/container stderr that would say *what* failed is
  preserved verbatim in `outputTail`; the guidance tells the caller to read
  it. (An OS **process-creation** failure — the child never started — is a
  different outcome, `spawn_failed`, and stays `environment`.)
- **Unobserved** — nothing observable when the budget expires (mostly URL
  modes: wrong app ID, game not installed, store updating the game first,
  a login/EULA dialog waiting on the desktop). Outcome `unobserved`: the
  claim is **kept** in phase `starting` with guidance ("the store launcher
  may be updating or showing a dialog — check the desktop; re-check
  games_status"). Reconciliation is passive — a later bridge connection or
  workload observation promotes the claim to `active` — and asymmetric:
  while a claim is `starting` after `unobserved`, **absence of evidence is
  not stopped**. The same absence produced `unobserved`, and the store may
  still launch the game minutes later, so status never clears this claim
  on a no-evidence observation. It resolves only through positive
  observation, `repair --forget-runtime`, or the explicit supersession
  policy: a new start may reclaim an `unobserved` claim older than its
  process-start budget after fresh probes again find nothing, and that
  result warns that an abandoned store launch could still appear. No
  background poller.
- **Stopped by hook** — status hook definitively reports stopped after
  spawn (e.g. container exited immediately): treated as
  exited-during-start, with hook evidence plus captured output. The hook is
  **liveness** evidence, not **cause** evidence, so this stays `game` by the
  evidence-based default — the caller reads the captured output to see what
  the wrapper reported.

## Stage 5 — Bridge attach

Existing `timeout`/`resetEndpoint` semantics preserved. Outcomes:

- `started_connected` — bridge usable; done.
- `started_bridge_pending` — workload verified running, bridge not yet
  connected within the wait. **Not an error.** Background attachment
  continues; next actions: wait, then `games_connect`. Long modlists take
  minutes; a slow bridge never justifies a relaunch or a profile switch.
  If the bridge never comes up: the game-side bridge/mod may not be
  installed or enabled, an online game may be stuck at a connection
  screen, or (if `adopted`) the injected environment may have been lost —
  the result hint enumerates these in that order.
- Workload died while waiting → failure with exit evidence, claim cleared
  per liveness.

## Bad-case map

| Situation | GABS observes | Result | Class | Caller guidance |
| --- | --- | --- | --- | --- |
| Config invalid / stale | hash+parse on call | `config_invalid`, starts blocked | config | fix file; error names path; reload is automatic |
| Target/cwd/hook missing | static resolution | `launch_spec_unresolvable` + both paths | config; environment when proven | fix config — or restore what disappeared |
| Duplicate start | claim + liveness | `already_running` + both profiles | state | use the active instance or stop it |
| Claim exists, evidence unclear | hook unknown/timeout | `blocked_unknown_state` + evidence | state | check hook/stderr; `repair --forget-runtime` if truly stale |
| Lost claim, workload alive | pre-start probes (all profiles) | `external_instance_detected` + external snapshot | state | stop it by ID — the snapshot enables it |
| Executable broken (arch, deps, Gatekeeper) | spawn error | `spawn_failed` + OS error | environment | fix binary/permissions |
| Crash / bad save / add-on failure | early exit | `exited_during_start` + exit code + output tail | game | read output; fix game state |
| Anti-cheat kills modified process | early exit or no bridge | `exited_during_start` / `started_bridge_pending` | game | hint lists anti-cheat as cause |
| Steam not functionally ready (macOS SteamManaged) | installed client-library pipe/global-user/app-state/API-init probe | `store_client_not_ready`, no process spawned | environment | retry the same start (optionally with a longer timeout); investigate Steam only if the non-timeout failure persists |
| Steam re-exec drops context | adoption, delivery not verified | `adopted` warning, `started_bridge_pending` | environment | `steam_appid.txt`, Steam launch options, or wrapper |
| Steam/Epic updating or dialog (URL modes) | nothing observable | `unobserved`, claim kept `starting` | environment | check desktop; re-check status |
| Wrong app ID / not installed | nothing observable | `unobserved` | config; environment when proven | verify target in store |
| Online server down | runs, no bridge or exits | `started_bridge_pending` / `exited_during_start` | pending / `game` on exit | game-side issue; read output tail |
| Container image missing / name conflict | wrapper exits fast | `exited_during_start` + wrapper stderr in `outputTail` | `game` (evidence-based default) | stderr in output tail says exactly what; GABS cannot tell a wrapper exit from a game crash |

### Why `exited_during_start` is always `game`, not a launch-mode guess

A post-spawn `exited_during_start` is classified `game` whenever no stronger
producer evidence exists, and no such evidence exists at the process boundary
GABS controls. GABS observes only the *first* process it creates; it cannot
reliably distinguish a game binary from a user-owned wrapper or container
launcher, and neither the launch **mode** (`CustomCommand`, `SteamManaged`),
the **target** shape, nor a **status-hook** "stopped" result is cause evidence
— a `SteamManaged` or `CustomCommand` game that genuinely crashes exits exactly
like a wrapper that failed. Two tempting signals were **rejected**:

- A **launch-mode heuristic** (`CustomCommand`/`SteamManaged` → environment)
  would misclassify the common case — a real game crash under those modes — as
  an environment problem, sending agents to "fix the environment" for a bug
  that is game-side. False attribution is worse than the honest default.
- A **classification-only config flag** (e.g. `launchKind: container`) would
  push a cause GABS cannot observe onto the user to declare, and drift from
  reality the moment the same command is reused for a non-container target.

The honest contract is the evidence-based default: attribute the exit to the
workload, and surface the wrapper/container's own stderr verbatim in
`outputTail` so the caller can read what actually failed. An OS
process-**creation** failure — where the child never ran — is a distinct
outcome (`spawn_failed`, `environment`) and is unaffected.
