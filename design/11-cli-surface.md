# CLI surface

New lifecycle commands share the resolver, lifecycle manager, and runtime
state with MCP — one implementation, two frontends:

```
gabs games start <id> [--profile NAME] [--input NAME=VALUE]...
gabs games status [<id>]
gabs games stop <id>
gabs games kill <id>
gabs games repair <id> [--forget-runtime]
```

- Repeated `--input` values parse per the declared type; repeating a name
  is an error.
- Cross-process safety: the claim + lease plus the per-game transition
  lock and domain-scoped fencing (see 06-stop-lifecycle.md). A CLI stop
  racing a server stop does not double-run the hook — the second operation
  observes phase `stopping` under the lock and reports
  `operation_in_progress`, exactly as over MCP.
- `start` prepares endpoint/runtime state and verifies through Stage 4,
  then exits without keeping a GABP client. Its terminal success outcome
  is `started_attachment_deferred`: workload verified, attachment
  intentionally not attempted (never mislabeled as
  `started_bridge_pending`), claim phase `active`, next action
  `games_connect` from a server session. CLI/MCP parity therefore covers
  Stages 1–4; Stage 5 outcomes are server-only.
- On macOS, a SteamManaged CLI start uses the configured/default GABP timeout
  as an independent pre-spawn Steam-readiness deadline, then receives the
  normal Stage 3/4 budget. It adds no CLI flag. A failed proof renders
  `store_client_not_ready` and never spawns the game.
- `status`/`stop`/`kill` work from the persisted snapshot after the
  original GABS process exited.
- `gabs games doctor <id>` (existing) becomes profile-aware: validates
  profile/input/hook references, resolves hook commands and working
  directories, flags the docker-style stopped/error conflation risk where
  detectable, and warns on broadly readable config/runtime files. It
  prints the full track record, and `doctor --show-last-good` prints the
  last-known-good entry (see 08-track-record.md).
