# Troubleshooting

Every GABS start/stop result carries a stable **code** and a **cause class**
(`config` / `environment` / `game` / `state` / `call`). The class tells you
*where the problem is* — and, crucially, whether editing the config is the
right response. On a configuration with a positive track record, an
`environment`, `game`, or `state` failure is **not** a config problem: follow
the result's next actions, retry within reason, and report to the user if it
persists — do not edit a proven config in response to one bad run. Edit the
config only when the class is `config`, when setting up a game that has never
launched successfully, or when the user explicitly asked for a change.

Run `gabs games doctor <id>` for a profile-aware diagnosis (config validity,
resolved launch target and hooks, the docker-style status-hook conflation lint,
broadly-readable-file warnings, and the full track record). Add
`--show-last-good` to print the last-known-good context for a game whose proven
configuration was edited, so you can compare or restore it by hand.

## Bad-case map

This table is the authoritative start-pipeline outcome map (design/05). It
doubles as support documentation: find the situation, read the class, follow
the caller guidance.

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
| Steam not functionally ready (macOS SteamManaged) | installed client-library pipe/global-user probe | `store_client_not_ready`, no process spawned | environment | retry the same start (optionally with a longer timeout); investigate Steam only if the non-timeout failure persists |
| Steam re-exec drops context | adoption, delivery not verified | `adopted` warning, `started_bridge_pending` | environment | `steam_appid.txt`, Steam launch options, or wrapper |
| Steam/Epic updating or dialog (URL modes) | nothing observable | `unobserved`, claim kept `starting` | environment | check desktop; re-check status |
| Wrong app ID / not installed | nothing observable | `unobserved` | config; environment when proven | verify target in store |
| Online server down | runs, no bridge or exits | `started_bridge_pending` / `exited_during_start` | pending / `game` on exit | game-side issue; read output tail |
| Container image missing / name conflict | wrapper exits fast | `exited_during_start` + wrapper stderr in `outputTail` | `game` (evidence-based default) | stderr in output tail says exactly what; GABS cannot tell a wrapper exit from a game crash |

## Why `exited_during_start` is always `game`, not a launch-mode guess

A post-spawn `exited_during_start` is classified `game` whenever no stronger
producer evidence exists — and no such evidence exists at the process boundary
GABS controls. GABS observes only the *first* process it creates; it cannot
reliably distinguish a game binary from a user-owned wrapper or container
launcher, and neither the launch **mode** (`CustomCommand`, `SteamManaged`),
the **target** shape, nor a **status-hook** "stopped" result is cause evidence —
a `SteamManaged` or `CustomCommand` game that genuinely crashes exits exactly
like a wrapper that failed.

So the honest contract is the evidence-based default: attribute the exit to the
workload, and surface the wrapper/container's own stderr verbatim in
`outputTail` so you can read what actually failed. An OS process-**creation**
failure — where the child never ran — is a distinct outcome (`spawn_failed`,
`environment`) and is unaffected.

## Outcomes that are not failures

- `started_bridge_pending` — the workload verified but the GABP bridge has not
  connected yet. Not a failure: poll `games_status`, then `games_connect` once
  the bridge is ready. Never relaunch or switch profiles in response.
- `started_attachment_deferred` — a CLI `gabs games start` verified the workload
  and left the runtime claim active without attaching. Attach later from a
  server session with `games_connect`.
- `unobserved` — nothing was observable within the start budget (a store
  launcher may be updating, or a dialog is open). The claim is kept; check the
  desktop and re-check `games_status`.
- `operation_in_progress` — another lifecycle operation is in flight. Re-check
  after the reported deadline; never start a duplicate.

## Common status-hook mistake: conflating "stopped" with "cannot determine"

A status hook that exits the same non-zero code both when the target is
genuinely stopped and when it merely cannot tell (a raw `docker inspect` exits
1 for an absent container *and* for an unreachable daemon) will let a transient
daemon hiccup read as "stopped" and unblock a duplicate start. Wrap such tools
so they check reachability first and exit an unclassified code (e.g. 2) when
they cannot tell, otherwise running=0 / stopped=1. `gabs games doctor` flags a
known conflating tool used directly as a status hook. See
docs/CONFIGURATION.md for the full wrapper pattern.
