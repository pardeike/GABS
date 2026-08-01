# Stopping and phases

Runtime state carries a persisted `phase`: `starting`, `active`,
`stopping`, `killing`. Rules:

- `games_status` never blocks on an in-flight operation: it reads the
  persisted phase and runs its own bounded evidence probe. While a stop
  hook runs, status truthfully reports phase `stopping` with the attempt's
  start time and deadline.
- One lifecycle operation per game at a time. A start/stop/kill arriving
  while another operation is in flight does **not** queue and does not
  block: it returns immediately with `operation_in_progress` (phase,
  action, started-at, deadline). Operations are strictly bounded (hook
  timeout + verify window), so the caller re-checks after the deadline. A
  stop arriving during `stopping` reports the in-flight attempt rather
  than running the hook twice concurrently.
- Kill during `stopping` likewise returns `operation_in_progress` until
  the stop attempt resolves (bounded), then may run. Kill never falls back
  to the graceful stop hook; if no force-capable action exists it says so
  (`kill_unsupported`). The inverse exists too: a valid kill-only
  configuration (URL mode with status + kill hooks, no `stopProcessName`)
  makes `games_stop` return `stop_unsupported` with `games_kill` as the
  next action — stop never silently escalates to kill.
- Caller abandonment (MCP transport gives up mid-stop) changes nothing
  server-side: the bounded attempt completes, and its outcome is
  observable. On success the claim is removed (status: stopped). On
  failure the phase returns to `active` and the attempt's result — action,
  exit code, stderr tail, timestamp, tree-kill warning if any — is
  persisted as `lastActionResult` in runtime state and shown by status.
  This replaces an earlier draft's operation journal with one field.
- Built-in actions obey the same persisted deadline as hooks. Every external
  process-table utility and Windows signal utility, every per-PID signal loop,
  and every verification name scan receives the remaining operation context;
  once it expires, no stale scan may continue and no later match may be
  signaled.

## Transition lock and domain-scoped fencing

Phase transitions are cross-process safe. Every read-decide-persist step —
claim creation, phase writes, claim removal, `lastActionResult` — runs
under a per-game OS advisory **transition lock** (flock on Unix, LockFileEx
on Windows, on a stable never-deleted lock file), never held while a hook
runs or anything waits. Fencing identities are **domain-scoped** — one
universal check would let an attachment update invalidate a legitimate
stop completion, or a lifecycle transition invalidate a legitimate
disconnect:

- `launchID` (immutable, random, minted at claim creation) identifies the
  claim's lifetime and closes the ABA case across claim delete/recreate —
  deletion or recreation invalidates all old work;
- `operationID` identifies each start/stop/kill attempt;
- `connectionID` identifies each GABP attachment lifetime;
- the persisted `generation` is only the state revision for
  compare-and-swap writes — never a validity test for unrelated callbacks.

Under the lock: a lifecycle completion validates launchID, operationID,
action, and expected phase, then merges its result into the latest claim
even if attachment fields changed meanwhile — a bridge disconnecting while
stop verification runs is the ordinary case, not a conflict. A
connect/disconnect/delivery callback validates launchID and connectionID,
so an old disconnect can never clear a newer connection. A failed stop
finishing after a kill removed the claim is rejected by launchID +
operationID and cannot resurrect `active` state. An in-process mutex alone
can give none of this between the MCP server and the CLI.

## Post-action verification

The liveness rule runs through the verification window
(`verifyTimeoutSeconds`, default 15) after every successful action — hook
exit 0 may mean "shutdown requested", not "terminated". Hook success is
provisional; at the window's end the existing evidence *sources* decide,
by explicit matrix:

- any source reports running → claim stays; `action_succeeded_running`;
- every existing source reports stopped → claim cleared; `terminated`;
- any existing source reports unknown (status-hook timeout or unclassified
  exit, inspection failure) → claim stays, `termination_unverified` —
  unknown never cleans state, even directly after a successful action;
- no independent source exists at all (no status hook, launcher gone,
  bridge never attached) → hook success alone stands as the
  stop-only-wrapper evidence and clears the claim.

The third row is the one an evidence-*absence* phrasing would get wrong: a
source that answered "unknown" exists and is not agreement. Unverified
termination keeps the claim; a later observation that finds stopped clears
it then.
