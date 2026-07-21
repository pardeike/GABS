# Launch resolution

A pure resolver takes (immutable config snapshot, game ID, optional profile,
supplied input values) and returns an immutable process spec plus a resolved
lifecycle snapshot. Deep-copied; no side effects; exactly one config
snapshot per start.

- Profile selection: explicit profile if given (unknown →
  `profile_not_found` with sorted candidates); else `defaultProfile`; games
  without profiles reject a requested profile with
  `profiles_not_configured`.
- Argument order: game `args` → profile `args` → supplied input arg groups
  in lexical input-name order.
- Environment (later wins): inherited environment with inherited `GABS_*`/
  `GABP_*` keys removed → game `unsetEnv`, then game `env` → profile
  `unsetEnv`, then profile `env` → supplied input `env` bindings (lexical)
  → GABS-managed variables (`GABS_GAME_ID`, `GABP_SERVER_PORT`,
  `GABP_TOKEN`, `GABS_BRIDGE_PATH`, `GABS_PROFILE` when a profile is
  selected, plus platform/launch-mode requirements such as Windows
  `SystemRoot`). Deterministic map merge, case-insensitive keys on
  Windows.
- Working directory: profile `workingDir` if set, else game `workingDir`.
  Paths are literal; no expansion.
- `GABS_PROFILE=<name>` is exported to the launched process and to
  lifecycle hooks whenever a profile is selected; absent for unprofiled
  launches.

The resolver is the only place launch context is computed; MCP and CLI both
call it. It also owns static resolvability checks (Stage 1 of
[05-start-pipeline.md](05-start-pipeline.md)) and PATH resolution of hook
commands to absolute paths.
