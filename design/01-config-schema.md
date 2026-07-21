# Configuration schema

All new fields are optional additions to the existing game entry. The config
version stays `1.0`; existing configs remain valid, untouched, and
warning-free.

```json
{
  "id": "adventure",
  "name": "Adventure Game",
  "launchMode": "DirectPath",
  "target": "/opt/example/adventure",
  "args": ["--bridge"],
  "env": { "LOG_FORMAT": "json" },
  "defaultProfile": "vanilla",
  "profiles": {
    "vanilla": {
      "description": "Untouched user data",
      "args": ["--data-root", "/srv/adventure/vanilla"]
    },
    "combat-test": {
      "description": "Isolated combat-test data",
      "args": ["--data-root", "/srv/adventure/combat-test"],
      "env": { "CONTENT_SET": "combat" },
      "workingDir": "/srv/adventure/combat-test"
    }
  },
  "launchInputs": {
    "quickStart": {
      "description": "Skip menus and load the configured quick-start path",
      "type": "boolean",
      "args": ["--quick-start"]
    },
    "scenario": {
      "description": "Select a configured startup scenario",
      "type": "string",
      "enum": ["arena", "tutorial"],
      "profiles": ["combat-test"],
      "args": ["--scenario", "${value}"]
    }
  },
  "lifecycle": {
    "status": { "command": "adventure-status", "args": ["${profile}"] },
    "stop":   { "command": "containerctl", "args": ["stop", "adventure-${profile}"] },
    "kill":   { "command": "containerctl", "args": ["kill", "adventure-${profile}"] }
  }
}
```

New game-level fields: `env`, `unsetEnv`, `defaultProfile`, `profiles`,
`launchInputs`, `lifecycle`.

## Profiles

- A profile is a launch-context overlay: `description`, `args`, `env`,
  `unsetEnv`, `workingDir`, and complete per-hook `lifecycle` overrides. It
  cannot change identity or transport: `id`, `name`, `target`,
  `launchMode`, `stopProcessName`, `gabpMode`, and bridge/runtime identity
  stay at game level. Profile names never appear in mirrored tool names or
  discovery.
- Game-level `args`/`env` are common base context; profile `args` append,
  profile `env` overrides matching keys, profile `workingDir` replaces the
  game `workingDir` when set.
- When `profiles` is non-empty, `defaultProfile` is **required** and must
  name a configured profile — enforced at config load. Consequence:
  `games_start <id>` without a profile always works, and there is no "you
  must pick a profile" error state. Guideline (docs): make the default your
  *safest* profile, since it is what a bare start gets. Trade-off accepted:
  there is no "explicit profile always required" mode; callers wanting that
  discipline pass `--profile` always.
- Exactly one profile (or the unprofiled launch) can be active per game ID.
- Games without `profiles` behave bit-for-bit as today.

## Launch inputs

The "start with quickstart" request, made safe by declaration: the config
author declares named, typed inputs; callers may supply only those names and
values.

- Types: `boolean`, `string`, `integer`. Strings may declare `enum`;
  integers may declare `minimum`/`maximum` (inclusive). An optional
  `profiles` array restricts an input to specific profiles. Strings without
  `enum` are still bounded: default `maxLength` 1024, declarable up to
  65536, optional `pattern`. Supplied values are rejected at call time for
  NUL bytes, invalid UTF-8, or length/pattern violations — "typed" means
  bounded, not merely stringly. Exact semantics, pinned so no SDK or agent
  guesses: `maxLength` counts Unicode code points; `pattern` is RE2 syntax
  (Go `regexp`) matched against the **entire** value (full-match anchoring
  — deliberately stricter than JSON Schema's unanchored ECMA convention,
  and stated wherever the schema-style map is rendered); an invalid
  pattern is a config validation error. Integers are signed 64-bit,
  decoded exactly (`json.Number` — a floating intermediary that would
  round values above 2^53 is rejected as non-integral), with
  `minimum`/`maximum` compared in int64; `${value}` substitutes the
  canonical base-10 form (optional leading minus, no leading zeros, no
  exponent).
- Each input declares `description` (required) and `args` and/or `env`
  bindings. `${value}` in a binding is replaced by the supplied value;
  substitution never crosses argv boundaries and never invokes a shell.
  String/integer inputs must use `${value}` at least once; boolean bindings
  must not contain it. A boolean applies its bindings only when supplied as
  `true`; `false` equals omission.
- Inputs apply only when explicitly supplied — no implicit defaults.
  Supplied inputs apply in lexical name order.
- Inputs must be lifecycle-neutral: a value that status/stop/kill would
  need to identify the workload (container name, service, data root) must
  be a profile instead. GABS documents this contract; it cannot infer
  executable semantics.

## Lifecycle hooks

Structured commands replace the shell-string `stopCommand` from the issue.

- Hooks: `status`, `stop`, `kill`. Each supports `command` (required),
  `args`, `workingDir`, `env`, `unsetEnv`, `timeoutSeconds`; stop/kill
  additionally support `verifyTimeoutSeconds`.
- Execution is exact executable-plus-argv — never a shell. `command` is an
  absolute path or a name resolved via PATH at launch time. Pipelines and
  shell logic belong in a wrapper script the user configures. On Windows,
  `command` must be an executable; batch files must be configured
  explicitly as `cmd.exe` with `/c script.cmd ...` args (GABS never
  implicitly wraps in a shell, avoiding cmd.exe quoting surprises).
- Placeholders `${gameId}` and `${profile}` are substituted in `args`,
  `env` values, and `workingDir`. `${profile}` is valid only for games with
  profiles. Unknown placeholders are validation errors.
- Exit-code contract: status — 0 = running, 1 = stopped, anything else,
  timeout, or failure to execute = unknown; optional `runningExitCodes` /
  `stoppedExitCodes` (non-empty, disjoint) override the defaults.
  Stop/kill — 0 = success, anything else = failure.
- **Status hooks must not conflate "stopped" with "cannot determine".**
  Many tools do exactly that: `docker inspect` exits 1 both when the
  container is absent and when the daemon is unreachable. Such tools need a
  small wrapper that checks reachability first and exits with an
  unclassified code (e.g. 2) when it cannot tell. The documented container
  example ships this wrapper; a raw `docker inspect` status hook is the
  canonical misconfiguration, and its consequence (a daemon hiccup reads as
  "stopped" and unblocks a duplicate start) is called out in docs and
  doctor.
- Timeouts (integral seconds): status 1–60, default 5; stop 1–600, default
  30; kill 1–600, default 10; `verifyTimeoutSeconds` 1–600, default 15 (how
  long GABS polls for termination after a stop/kill action succeeds — games
  that save on exit need more than the default).
- On hook timeout GABS terminates the hook's process tree (process group on
  Unix, Job Object on Windows), confirms its direct child is reaped, and
  reports unknown (status) or failure (stop/kill). Residual risk, accepted
  and documented: a grandchild that detached from the group can survive and
  act late; GABS records a warning in the result and in runtime state, and
  the pre-start probes (05-start-pipeline.md, Stage 2) are the backstop.
  GABS does not block future starts on this possibility.
- Hook environment: sanitized inherited environment (inherited `GABS_*`/
  `GABP_*` removed) → hook `unsetEnv` → hook `env` → `GABS_GAME_ID` +
  `GABS_PROFILE` (when a profile is selected). Hooks never receive
  `GABP_TOKEN`/`GABP_SERVER_PORT`.
- Hook stdout/stderr are captured (capped at 16 KiB per stream); the stderr
  tail and exit code are included in failure results and logs.
- Per-hook resolution order: profile override (complete replacement, no
  field merge) → game-level hook → built-in behavior (tracked PID, then
  `stopProcessName`).
- Contract: hooks must be idempotent and self-contained. GABS may run a
  hook again after a crash, a retry, or a second stop request.

## Validation rules

Config validation reports exact JSON paths. Rules:

- Profile and input names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`,
  case-sensitive.
- `defaultProfile` required with profiles, must exist. An input's
  `profiles` list must name existing profiles.
- Config-declared environment keys (game, profile, input, hook — `env`
  and `unsetEnv` alike) must match the portable grammar
  `^[A-Za-z_][A-Za-z0-9_]*$` — what every platform accepts, and what makes
  `GABS_FORWARD_ENV` and its documented shell loop unambiguous. Keys with
  reserved prefixes `GABS_`/`GABP_` (ASCII case-insensitive) are rejected
  everywhere; NUL in values is rejected; on Windows, keys colliding after
  case folding are rejected. A key may not appear in both `unsetEnv` and
  `env` of the same layer. `unsetEnv` exists because isolation requires
  *absence*, not just replacement: an inherited host `CONTENT_SET` must be
  removable from a profile, and an empty string is observably different
  from an unset variable.
- Working directories declared in the new subtrees (profiles, hooks) must
  be absolute paths — this design defines no relative base, and delivery
  verification needs an unambiguous expected value. A legacy relative
  game-level `workingDir` keeps its historical behavior; its cwd channel
  is reported `unverifiable` (capping overall delivery at `partial`) and
  doctor flags it.
- Two inputs that could write the same env key are a validation error.
- `SteamAppId` and `EpicAppId` launch via launcher URLs and cannot
  propagate args/env/cwd to the game: these modes reject `profiles`,
  `launchInputs`, and the new game-level `env`/`unsetEnv` entirely
  (pointing at SteamManaged, DirectPath, CustomCommand) — context that
  would reach only the URL helper, never the game, must not validate
  silently. Pre-existing launcher-only fields on such entries (`args`,
  `workingDir`) stay loadable unchanged for compatibility, but doctor
  reports them as launcher-only (a doctor finding, not a load warning,
  preserving the warning-free promise for legacy entries). Game-level
  lifecycle hooks remain valid for every mode — and they relax the
  existing rule that URL modes must declare `stopProcessName`: a
  game-level `status` hook plus a `stop` or `kill` hook satisfies the
  observation/control requirement instead, since hooks are stronger
  evidence than a name match. One of the two mechanisms remains mandatory
  for URL modes, because the URL-opener helper PID proves nothing about
  the workload.
- Duplicate JSON object members anywhere in the config file are
  `config_invalid` with the path. Both struct and map decoding silently
  keep the last member, so a duplicated profile name, hook field, or env
  key would validate while launching a different context than the one a
  reviewer read — unacceptable with automatic reload. Detection is a
  token-level scan before any decoded form is accepted. (Release-notes
  item: such files previously loaded last-value-wins.)
- Unknown keys in the new subtrees (`profiles`, `launchInputs`,
  `lifecycle`) are errors; unknown keys elsewhere produce a warning naming
  the path. Warnings are not log-only: they appear in `games_show` (per
  game), in `games_list` structured content (count per game), and in
  doctor output. Warnings with no owning game — a top-level unknown key, a
  misspelled `timeouts` member — appear in a global `configWarnings` array
  in structured list/status results, so MCP callers without a CLI still
  see them.
