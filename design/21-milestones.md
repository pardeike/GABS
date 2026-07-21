# Milestones (independently mergeable)

Work strictly in milestone order. Each milestone merges only when every one
of its PROGRESS.md items is done with green tests.

## Milestone 1 — Schema + resolver + reload + strict MCP

Config types, validation, warnings, ConfigStore, resolver with static
checks (incl. macOS bundle resolution), `games_start`
profile/launchInputs plumbing, the managed env layer + `GABS_FORWARD_ENV`,
platform spawn rules (no open/ShellExecute for propagation-capable modes,
elevation mapping), the conformance-suite probe + direct/wrapper cells,
show/list/status metadata, strict argument validation on all tools.

Exit state: foreground profiled launches work; reload works; Stage 1–3
outcomes (config errors, spawn_failed) exist.

**Feature gate:** `lifecycle` fields are rejected at validation with a
not-yet-supported error until milestone 2 lands — a config must never
validate against semantics that do not execute yet. Profiles without hooks
are fully functional in M1 under the old stop behavior.

## Milestone 2 — Lifecycle + liveness + start taxonomy

Hook runner, liveness rule, phases, Stage 4/5 outcomes (adopted,
exited_during_start with output capture, unobserved + passive
reconciliation, started_bridge_pending), stop/kill + verification +
lastActionResult, restart recovery, runtime.json extensions + atomic 0600
writes + atomic claim publication, the transition lock + domain-scoped
fencing (launchID/operationID/connectionID; generation as CAS only),
all-profile pre-start probing + external snapshots, the unobserved-claim
policy, expected-context digests, per-launch credentials, the attachment
lease record, interrupted-phase normalization, legacy claim
migration/normalization, repair --forget-runtime, history store + failure
classifier (incl. input-combination buckets) + causeClass/track-record in
results + edit notice, bridge.json diagnostic fields + handshake delivery
verification + the remaining conformance cells (env-dropping, filtering,
detached). Removes the M1 lifecycle feature gate.

Exit state: completes issue #66.

## Milestone 3 — CLI + docs + skill

CLI start/status/stop/kill on the shared manager, profile-aware doctor
incl. --show-last-good and track-record display,
README/CONFIGURATION/INTEGRATION updates, example-config.json, the
status-hook wrapper example, consolidation checklist, `skills/gabs-mcp`
guidance update incl. the agent edit contract (see 31-docs-and-skill.md).

Exit state: the final regression gate in 30-test-plan.md passes on all
three OSes.

## Sequencing guardrails

- Do not implement hooks only inside status/stop, and do not add CLI
  commands by duplicating MCP handler logic — both frontends call the
  lifecycle manager.
- Do not ship a milestone with contradictory or skipped tests: delete
  superseded test expectations when a spec section changes, never keep
  alternate interpretations alive.
- Partial implementations that "look complete in simple tests while
  failing the detached, concurrent, and cross-session cases" are the
  known failure mode of this feature — the conformance suite and fencing
  tests exist precisely to catch them. Run them from day one, not at the
  end.
