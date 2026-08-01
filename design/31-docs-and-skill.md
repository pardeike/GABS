# Documentation and skill deliverables (Milestone 3)

- README: user-level model, one discovery→start example.
- docs/INTEGRATION.md: the wrapper contract (forward argv, preserve/map
  env, `GABS_FORWARD_ENV` loop for containers, do not reintroduce
  `GABS_ABSENT_ENV` names), the env-only live-bridge rule (`bridge.json`
  fields are diagnostic; never a discovery fallback — stale files
  mis-attribute launches), and the optional session-welcome `observed`
  field spec for bridge implementers (argv, cwd, raw observed values for
  `GABS_FORWARD_ENV` names plus present/absent status for
  `GABS_ABSENT_ENV` names — GABS hashes locally; bridges never hash;
  entirely optional).
- docs/CONFIGURATION.md: full schema, resolver order, exit-code contract
  with the **status-hook wrapper pattern** (reachability check → exit 2
  unknown; container running/absent → 0/1) as the canonical container
  example; idempotent-hook contract; verifyTimeoutSeconds guidance for
  save-on-exit games; Windows script-hook rule (`cmd.exe /c` explicit);
  Steam re-exec caveat + `steam_appid.txt` and launch-options/`%command%`
  workarounds; the legacy→profile conversion recipe; the ID-consolidation
  checklist (tool namespace changes, script references, loss of per-ID
  concurrency); old-binary warning.
- docs/TROUBLESHOOTING (or a section): the bad-case map table from
  05-start-pipeline.md, verbatim — it doubles as support documentation.
- docs/releases/v1.1.0.md: user-facing compatibility notes for strict unknown
  MCP arguments, bounded `timeout`, duplicate-member rejection, and the
  pre-1.1.0 silent profile-field drop; the release workflow prepends this
  reviewed note to generated commit/PR notes.
- example-config.json: one neutral profiled game with hooks incl. the
  wrapper-based status hook.
- skills/gabs-mcp/SKILL.md (concise additions): check games_show before
  choosing a profile/input; prefer profiles over duplicate IDs only when
  target+launchMode match; pass a launch input only when the user
  explicitly asked; never invent inputs or use them instead of GABP
  tools; config edits apply automatically — verify via games_show;
  stop/kill never need a profile; `started_bridge_pending` and
  `unobserved` are not failures — follow next actions, never relaunch or
  switch profiles; on `operation_in_progress` re-check after the reported
  deadline; on `unknown` liveness follow the returned next action, never
  start a duplicate.

  **Edit contract (verbatim rules):** edit GABS config only when (a) the
  failure's `causeClass` is `config`, (b) you are setting up a game that
  has never launched successfully, or (c) the user explicitly asked for a
  config change. A failure with class `environment`, `game`, or `state`
  on a context with a positive track record is never a config problem:
  follow nextActions, retry within reason, then report to the user. Treat
  a result's "this context has started N×" line as authoritative history
  — do not second-guess a proven config because of one bad run.

Public docs and examples must use neutral names (the repo genericity scan
enforces this).
