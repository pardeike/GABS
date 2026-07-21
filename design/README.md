# GABS Launch Profiles — Design

**Status: implementation-final.** Six design-review rounds concluded. The
remaining questions are implementation-and-testing matters, not design
review. Earlier drafts (ProfileFeature-v1 … v4) live in git history; this
folder is the sole binding source.

Resolves issue #66 (https://github.com/pardeike/GABS/issues/66): launch one
configured game under named, isolated launch contexts; let callers request
pre-declared startup options; make wrapped/containerized launches observable
and stoppable; and make config edits effective without restarting GABS or
the MCP client.

## The model

    stable game identity  ->  optional named profile  ->  optional declared, typed launch inputs

Profiles select repeatable launch context. Launch inputs add bounded,
explicit per-launch variation. Lifecycle hooks make the selected context
observable and controllable. None of these is a second control channel for
the running game — that remains GABP tools.

## How to work with this folder

These files are the contract. Read this file first, then work through
[PROGRESS.md](PROGRESS.md), which is the single source of truth for what is
done and what is next.

**Rules for the implementing agent — not optional:**

1. **Test-driven.** For every PROGRESS item: read its spec file(s) first,
   write the tests listed for it in [30-test-plan.md](30-test-plan.md),
   watch them fail, then implement. An item is done only when its tests
   pass.
2. **Track progress in the same commit.** Every commit that advances an
   item updates its checkbox in PROGRESS.md. Never batch progress updates
   separately from the work.
3. **Deviations are recorded, never silent.** If implementation reveals
   the spec is wrong or impossible, do not quietly diverge: implement the
   closest sound behavior, and record the deviation in the Deviations
   section of PROGRESS.md with the spec reference and reasoning. The specs
   survived six adversarial review rounds — assume a conflict means you
   are missing context before assuming the spec is wrong.
4. **Do not re-add rejected machinery.** [12-rationale.md](12-rationale.md)
   lists everything that was considered and rejected, with reasons. If an
   idea you have appears there, the answer is already no.
5. **Do not "simplify away" load-bearing pieces.** The transition lock,
   domain-scoped fencing, spawnState, atomic claim publication, and the
   attachment lease each closed a demonstrated race. Their reasons are in
   the spec text where they are defined.
6. **Exhaustive result codes.** Every terminal branch maps to exactly one
   stable code from [10-mcp-surface.md](10-mcp-surface.md). Adding a
   branch means adding a code — never overload a neighbor or invent one
   ad hoc.

**Reading order for a first full pass:** this file → 12 → 01 → 02 → 03 →
04 → 05 → 06 → 07 → 08 → 09 → 10 → 11 → 20 → 21 → 30 → 31. For a work
session: PROGRESS.md → the item's spec file(s) → its tests in 30.

## File index

| File | Contents |
| --- | --- |
| [01-config-schema.md](01-config-schema.md) | Config schema: profiles, launch inputs, lifecycle hooks, validation rules |
| [02-launch-resolution.md](02-launch-resolution.md) | The pure resolver: selection, argument order, environment precedence |
| [03-context-delivery.md](03-context-delivery.md) | One-hop guarantee, wrapper contract, credentials, delivery verification, platform rules |
| [04-liveness.md](04-liveness.md) | The liveness rule, its invariants, the attachment lease |
| [05-start-pipeline.md](05-start-pipeline.md) | Start stages 1–5, branch-to-code table, bad-case map |
| [06-stop-lifecycle.md](06-stop-lifecycle.md) | Phases, transition lock, domain-scoped fencing, stop verification matrix |
| [07-runtime-state.md](07-runtime-state.md) | runtime.json contract, external snapshots, restart recovery, legacy normalization |
| [08-track-record.md](08-track-record.md) | history.json, failure attribution (causeClass), edit visibility, agent contract |
| [09-config-reload.md](09-config-reload.md) | ConfigStore, last-known-good, hot vs startup-only, revisions |
| [10-mcp-surface.md](10-mcp-surface.md) | MCP tool changes, result fields, the exhaustive stable-code list |
| [11-cli-surface.md](11-cli-surface.md) | CLI commands, CLI-specific outcomes, doctor, repair |
| [12-rationale.md](12-rationale.md) | Design stance, worked example, profile-design assessment, rejected alternatives |
| [20-implementation-map.md](20-implementation-map.md) | Codebase touchpoints, new components, pinned behavior details |
| [21-milestones.md](21-milestones.md) | The three milestones, feature gate, sequencing guardrails |
| [30-test-plan.md](30-test-plan.md) | Full test catalog by area + final regression gate |
| [31-docs-and-skill.md](31-docs-and-skill.md) | User documentation and gabs-mcp skill deliverables |
| [PROGRESS.md](PROGRESS.md) | Progress protocol and the milestone checklists |

## Goals

1. `games_start` with `profile: "combat-test"` launches the game under that
   named context; nothing bleeds into other profiles.
2. Callers can request declared startup options without GABS ever exposing
   raw argument/environment passthrough on MCP.
3. Wrapped and containerized launches get working status/stop/kill via
   configured commands.
4. Config edits take effect on the next call — no GABS or client restart.
5. Every way a start can fail — bad config, dead target, store launcher
   trouble, early crash, missing bridge — produces a distinct, evidence-
   carrying result that tells the caller what happened and what to do.

## Non-goals

- Game-specific concepts (`dataDir`, `modList`). Isolation is expressed
  through generic args/env/workingDir; enforcement belongs to the launcher,
  wrapper, or container.
- Raw per-call args/env on the MCP surface.
- Multiple concurrent instances of one game ID (see 12-rationale.md for why
  and for the reserved extension path).
- Automatic migration, config rewriting, or a migration planner.
- Shell interpretation, tilde/variable expansion, or command substitution
  anywhere.
- New MCP tools, dynamic per-profile tools, or `tools/list` churn.
- Breaking GABP wire changes. (One optional, backward-compatible field in
  the bridge's session-welcome response is added for context-delivery
  verification; old bridges simply omit it.)

## Design stance

This design deliberately replaces machinery with contracts where a contract
is enough:

- **Hooks must be idempotent and honest.** Stop/kill/status commands must be
  safe to run more than once, and a status hook must distinguish "stopped"
  from "cannot determine" (exit unknown, don't guess). GABS may re-run a hook
  after a crash or retry instead of tracking at-most-once invocation state.
- **The MCP caller is the operator.** GABS is a developer tool. Hook stderr,
  child-process output, exit codes, and config paths are shown to the caller
  because they are the debugging signal, not hidden as secrets.
- **Reuse existing mechanisms, add exactly one.** Cross-process safety
  uses the existing runtime-state claim (atomic create-exclusive) and
  owner-lease model, plus one new primitive: a per-game transition lock (an
  OS advisory file lock) held for milliseconds around state reads and
  writes, never during hooks or waits. The claim file plus its launch ID
  and generation is authoritative between processes. No operation journals,
  no public operation IDs.
- **Unknown is a first-class answer.** When GABS cannot prove liveness it
  says `unknown`, keeps its state, refuses to start a duplicate, and tells
  the caller how to resolve it. It never invents certainty.
- **Proof is earned, not declared.** GABS tracks which launch contexts have
  actually worked and uses that history to attribute failures and to deter
  "fixing" proven configuration after a transient problem. It never
  write-protects the config file; protection is evidence, not locks.
- **GABS reports; it does not improvise.** No auto-retry of a failed start,
  no automatic relaunch when a bridge is slow, no driving of store-launcher
  UI dialogs. Every result carries evidence and a concrete next action.

## Compatibility promises

- Existing configs load, validate, and launch exactly as before; no new
  warnings for ordinary legacy entries.
- Existing MCP behavior preserved except three intentional, release-noted
  changes: unknown arguments are now rejected (previously silently
  ignored), `timeout` is range-checked, and config files containing
  duplicate JSON members are rejected (previously last-value-wins).
- `stopProcessName` remains a supported fallback; GABS warns when multiple
  configured games share a process name.
- Upgrading GABS while a pre-upgrade launch is still running is supported
  by a one-time validated migration and full claim normalization — the
  complete rules live in [07-runtime-state.md](07-runtime-state.md)
  (Legacy claims).
- Older GABS binaries ignore unknown config fields, so a profile-enabled
  config must not be used with a pre-profile binary — release-notes item,
  not a schema mechanism.
- Docs include a consolidation checklist for users collapsing per-profile
  game IDs into one entry: mirrored game-tool names change (namespace
  follows the surviving ID), scripts referencing removed IDs must be
  updated, and formerly independent IDs could run concurrently while one
  ID is single-instance. GABS does not automate this; it warns about it.
