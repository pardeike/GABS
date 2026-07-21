# Implementation progress

This file is the single source of truth for implementation state. It is a
contract, not a scratchpad.

## Protocol

- States: `[ ]` not started · `[~]` in progress · `[x]` done (its tests
  pass) · `[!]` blocked or deviating (must have a Deviations entry).
- **Test-driven:** for each item, first read its spec file(s), then write
  its tests from [30-test-plan.md](30-test-plan.md) (the listed T-IDs),
  watch them fail, then implement. `[x]` requires the listed tests green.
- **Same-commit updates:** every commit that advances an item updates this
  file in that commit. Never batch progress updates separately.
- **Discovered work** gets added as new items marked `(added)` — never
  done silently, and existing items are never deleted or reworded to fit
  the code.
- **Deviations** from spec go in the Deviations section below with spec
  reference and reasoning, and the item is marked `[!]` until resolved.
  The specs survived six adversarial review rounds; assume a conflict
  means missing context before assuming the spec is wrong.
- Work milestones strictly in order. A milestone is complete when all its
  items are `[x]` and its exit state (21-milestones.md) holds.

## Milestone 1 — Schema + resolver + reload + strict MCP

- [ ] M1.1 Config types: Env/UnsetEnv/DefaultProfile/Profiles/
      LaunchInputs/Lifecycle on game, profile, hook structs — spec: 01;
      tests: T-VAL
- [ ] M1.2 Duplicate-JSON-member token scan + unknown-key error/warning
      split + global configWarnings — spec: 01; tests: T-VAL
- [ ] M1.3 Full validation rules (names, env grammar, unsetEnv conflicts,
      absolute workingDir, URL-mode rejections + hook relaxation, input
      constraints incl. string/integer semantics) — spec: 01; tests: T-VAL
- [ ] M1.4 M1 lifecycle feature gate (reject `lifecycle` until M2) —
      spec: 21; tests: T-VAL
- [ ] M1.5 ConfigStore: hash-per-call reload, last-known-good, revisions,
      snapshot immutability; replace captured config pointers — spec: 09;
      tests: T-RELOAD
- [ ] M1.6 Pure resolver: selection, arg order, env merge (unsetEnv
      layers, managed layer, GABS_FORWARD_ENV/GABS_ABSENT_ENV), cwd, hook
      resolution, static resolvability, platform-size check — spec: 02,
      03; tests: T-RES
- [ ] M1.7 Platform spawn rules: macOS .app inner-binary resolution, no
      open/ShellExecute for propagation-capable modes, elevation → hint,
      Windows quoting — spec: 03; tests: T-DELIV (Windows/macOS cells)
- [ ] M1.8 Child I/O to per-launch log file (no parent-owned pipes) —
      spec: 05 Stage 3; tests: T-DELIV (I/O survival)
- [ ] M1.9 Strict MCP argument validation on all core tools
      (additionalProperties:false + shared helper) — spec: 10; tests:
      T-MCP
- [ ] M1.10 games_start profile/launchInputs plumbing + Stage 1
      branch-to-code mapping — spec: 05, 10; tests: T-START (Stage 1),
      T-MCP
- [ ] M1.11 show/list/status metadata: profiles, input constraints incl.
      maxLength/pattern, warnings, revisions — spec: 10, 09; tests: T-MCP
- [ ] M1.12 Conformance probe helper + direct/forwarding-wrapper cells —
      spec: 03; tests: T-DELIV

## Milestone 2 — Lifecycle + liveness + start taxonomy

- [ ] M2.1 RuntimeState extension (full field contract) + atomic
      tmp+rename saves + atomic tmp+link claim publication + 0600/0700 +
      legacy chmod-tighten — spec: 07, 05 Stage 2; tests: T-RT, T-FENCE
      (atomic publication)
- [ ] M2.2 Transition lock + domain-scoped fencing
      (launchID/operationID/connectionID; generation as CAS) — spec: 06;
      tests: T-FENCE
- [ ] M2.3 Hook runner (tree-kill, output capture, Windows Job Objects,
      exit-code contract) — spec: 01; tests: T-LIFE
- [ ] M2.4 Liveness rule incl. attachment lease record, inspection-
      failure=unknown, URL helper PID exclusion — spec: 04; tests:
      T-LIFE, T-FENCE (attachment evidence)
- [ ] M2.5 Start pipeline Stages 2–5: complete pre-spawn claim, all-
      profile probing + external snapshots, endpoint alloc + per-launch
      token, spawnState transitions, Stage 4 outcomes (adopted /
      exited_during_start / unobserved policy), Stage 5 attach — spec:
      05, 03; tests: T-START
- [ ] M2.6 Stop/kill: verification matrix, probe clipping,
      lastActionResult, stop_unsupported/kill_unsupported,
      operation_in_progress semantics — spec: 06; tests: T-LIFE, T-FENCE
- [ ] M2.7 Restart recovery: liveness-driven, interrupted-phase
      normalization, spawning-window verdicts, executor-vs-owner —
      spec: 07, 05 Stage 3; tests: T-FENCE, T-START (claim-window),
      T-GATE
- [ ] M2.8 Legacy claim migration + full normalization — spec: 07;
      tests: T-RT
- [ ] M2.9 Expected-context digests + welcome `observed` parsing +
      per-channel aggregation matrix + contextDelivery persistence —
      spec: 03, 07; tests: T-DELIV
- [ ] M2.10 History store + classifier + input-combination buckets +
      edit notice + causeClass/track-record rendering — spec: 08; tests:
      T-TRACK
- [ ] M2.11 bridge.json diagnostic fields; env-only live contract
      preserved — spec: 03; tests: T-DELIV
- [ ] M2.12 Remaining conformance cells (env-dropping, filtering,
      absent-env reintroduction, detached) — spec: 03; tests: T-DELIV
- [ ] M2.13 repair --forget-runtime + no-arg games_status union of
      runtime-only claims — spec: 07, 10; tests: T-RT
- [ ] M2.14 Remove M1 lifecycle feature gate — spec: 21; tests: T-VAL
      update
- [ ] M2.15 EnsureClientRunning demoted to bounded best-effort warning —
      spec: 05 Stage 2, 20; tests: T-START (Steam advisory)

## Milestone 3 — CLI + docs + skill

- [ ] M3.1 CLI start/status/stop/kill on the shared lifecycle manager +
      started_attachment_deferred — spec: 11; tests: T-CLI
- [ ] M3.2 Profile-aware doctor + --show-last-good + track-record
      display + conflation lint — spec: 11, 08; tests: T-CLI
- [ ] M3.3 User docs (README, CONFIGURATION, INTEGRATION,
      TROUBLESHOOTING, example-config.json) — spec: 31; gates: genericity
      scan
- [ ] M3.4 skills/gabs-mcp update incl. the agent edit contract — spec:
      31; gates: skill validation
- [ ] M3.5 Acceptance scenario end-to-end — tests: T-ACC
- [ ] M3.6 Final regression gate on all three OSes — tests: T-GATE

## Deviations

(none yet — add entries as: date, item, spec section, what differs, why,
and how it was resolved or why it is acceptable)
