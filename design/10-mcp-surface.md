# MCP surface

No new tools. Every core tool's schema gains `additionalProperties: false`
and every handler rejects unknown arguments with `unknown_argument`, the
offending path, and the sorted allowed names.

- `games_start` adds `profile?: string` and `launchInputs?: object`
  (values boolean/string/integer; the handler validates names, types,
  constraints, and applicability against the pinned snapshot). Existing
  `gameId`, `timeout`, `resetEndpoint` keep their meaning; `timeout`
  validated 1–3600. Results carry: outcome (per the start pipeline),
  `activeProfile`, applied input names (never values), `adopted` and
  `contextDelivery` when applicable, warnings, evidence (exit code /
  output tail where relevant), `causeClass` plus a one-line track record
  on failure, and next actions.
- `games_show` returns sorted profile names + descriptions,
  `defaultProfile`, launch inputs as a JSON-Schema-style map (name → type,
  description, enum, minimum, maximum, maxLength, pattern, profiles —
  every declared constraint, with the effective default `maxLength` made
  explicit when the config omits it, so an agent can form a valid value
  without trial calls), config warnings for the game,
  `currentConfigRevision` plus `activeConfigRevision` when they differ,
  and per-profile proven status (the track-record counters). Arg/env
  templates omitted — noise, not secret.
- `games_list` keeps compact text output; structured content adds profile
  names, default, and warning counts per game.
- `games_status`, `games_connect`, `games_stop`, `games_kill` report
  `activeProfile` and `phase`. Status is non-blocking and includes
  `lastActionResult` when present. Stop/kill act on the runtime snapshot.
  Multi-game status runs probes concurrently, each under its own timeout.
  No-argument `games_status` unions configured entries with persisted
  runtime claims (see 07-runtime-state.md).
- `profiles_not_configured` includes the game ID, requested profile,
  actual config path, a documentation anchor, and a note that edits apply
  without restart. GABS never generates patches and never rewrites config.

## Stable error/outcome codes

`unknown_argument`, `config_invalid`, `game_not_found`,
`ambiguous_game_reference`, `timeout_out_of_range`,
`launch_spec_unresolvable`, `profiles_not_configured`,
`profile_not_found`, `launch_input_not_declared`, `launch_input_invalid`,
`launch_mode_incompatible`, `already_running`, `blocked_unknown_state`,
`external_instance_detected`, `spawn_failed`, `exited_during_start`,
`unobserved`, `started_bridge_pending`, `started_connected`,
`operation_in_progress`, `kill_unsupported`, `stop_unsupported`,
`termination_unverified`, `stale_bridge_credential`,
`endpoint_unavailable`, `spec_too_large`, `action_failed`,
`action_timed_out`, `action_succeeded_running`, `terminated`,
`started_attachment_deferred` (CLI).

This list is exhaustive by contract: every terminal branch of the start
and stop/kill pipelines maps to exactly one code, and adding a branch
means adding a code — handlers never overload a neighboring code or
invent one.
