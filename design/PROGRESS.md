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

- [x] M1.1 Config types: Env/UnsetEnv/DefaultProfile/Profiles/
      LaunchInputs/Lifecycle on game, profile, hook structs — spec: 01;
      tests: T-VAL
- [x] M1.2 Duplicate-JSON-member token scan + unknown-key error/warning
      split + global configWarnings — spec: 01; tests: T-VAL
      (warnings carried on GamesConfig.Warnings; MCP surfacing lands with
      M1.11)
- [x] M1.3 Full validation rules (names, env grammar, unsetEnv conflicts,
      absolute workingDir, URL-mode rejections + hook relaxation, input
      constraints incl. string/integer semantics) — spec: 01; tests: T-VAL
      (hook validation and the URL-mode stopProcessName relaxation are both
      implemented and tested behind AllowLifecycle, incl. the legacy
      Validate() branch; the gate keeps them unreachable from the load
      path until M2.14 lifts it)
- [x] M1.4 M1 lifecycle feature gate (reject `lifecycle` until M2) —
      spec: 21; tests: T-VAL
- [x] M1.5 ConfigStore: hash-per-call reload, last-known-good, revisions,
      snapshot immutability; replace captured config pointers — spec: 09;
      tests: T-RELOAD
      (all 13 core handlers now fetch per-call config via
      currentGamesConfig/currentSnapshot; the store self-primes at
      SetConfigStore so last-known-good exists from startup; without a
      store — tests — the startup config doubles as a fixed snapshot)
- [x] M1.6 Pure resolver: selection, arg order, env merge (unsetEnv
      layers, managed layer, GABS_FORWARD_ENV/GABS_ABSENT_ENV), cwd, hook
      resolution, static resolvability, platform-size check — spec: 02,
      03; tests: T-RES
      (managed layer + GABS_FORWARD_ENV/GABS_ABSENT_ENV emitted by
      controller buildEnvironment; static resolvability covers all three
      propagation-capable modes — DirectPath/CustomCommand targets incl.
      .app resolution, relative targets resolved against the effective
      workingDir, empty-target rejection, SteamManaged via the read-only
      Steam resolver — plus working directories; hook commands PATH-pin
      to absolute at resolution)
- [x] M1.7 Platform spawn rules: macOS .app inner-binary resolution, no
      open/ShellExecute for propagation-capable modes, elevation → hint,
      Windows quoting — spec: 03; tests: T-DELIV (Windows/macOS cells)
      (.app inner-binary resolution wired into DirectPath spec
      construction; propagation-capable modes exec directly — no
      open/ShellExecute paths exist for them; elevation errno 740 maps to
      a precise hint, unit-tested cross-platform; Windows quoting counted
      exactly in CheckProcessSize)
- [x] M1.8 Child I/O to per-launch log file (no parent-owned pipes) —
      spec: 05 Stage 3; tests: T-DELIV (I/O survival)
      (child stdout/stderr inherit a 0600 launch.log descriptor in the
      per-game runtime dir, truncated at spawn; LaunchLogTail provides
      the capped evidence tail; resolved launches only add
      platform-appropriate managed vars — the legacy unconditional
      SystemRoot injection stays legacy-path-only)
- [x] M1.9 Strict MCP argument validation on all core tools
      (additionalProperties:false + shared helper) — spec: 10; tests:
      T-MCP
- [x] M1.10 games_start profile/launchInputs plumbing + Stage 1
      branch-to-code mapping — spec: 05, 10; tests: T-START (Stage 1),
      T-MCP
      (resolver-integrated start with structured codes incl.
      launch_spec_unresolvable with JSON+fs paths, config_invalid start
      refusal on stale config, activeProfile/appliedLaunchInputs/
      configRevision in results; end-to-end test proves profile args
      reach the child process)
- [~] M1.11 show/list/status metadata: profiles, input constraints incl.
      maxLength/pattern, warnings, revisions — spec: 10, 09; tests: T-MCP
      (complete except activeConfigRevision, which requires the persisted
      launch revision from M2.1's runtime-claim extension; reopened per
      review — currentConfigRevision alone cannot distinguish what is
      running from what the next start would use)
- [x] M1.12 Conformance probe helper + direct/forwarding-wrapper cells —
      spec: 03; tests: T-DELIV
      (probe records argv/env/cwd; direct cell asserts all three channels
      + the GABS_FORWARD_ENV drift assertion; forwarding wrapper cells
      exist for both platforms — sh on unix, a build-tagged cmd.exe /c %*
      variant for Windows CI; the remaining cells — env-dropping,
      filtering, absent-reintroduction, detached — are M2.12 as planned)

## Milestone 2 — Lifecycle + liveness + start taxonomy

- [x] M2.1 RuntimeState extension (full field contract) + atomic
      tmp+rename saves + atomic tmp+link claim publication + 0600/0700 +
      legacy chmod-tighten — spec: 07, 05 Stage 2; tests: T-RT, T-FENCE
      (atomic publication)
      (full schema landed incl. Operation/Attachment/ActionResult/
      Digests/Delivery types — later M2 items populate them; claims stamp
      schemaVersion=2, launchID, generation=1, phase, spawnState,
      pidRole; hammer + exactly-one-winner + hardlink-fallback tests)
- [~] M2.2 Transition lock + domain-scoped fencing
      (launchID/operationID/connectionID; generation as CAS) — spec: 06;
      tests: T-FENCE
      (lock primitive done: flock on unix, exclusive-share CreateFile on
      Windows, stable never-deleted file, bounded acquisition, no lost
      updates under 8-way contention; TransitionRuntimeState bumps the
      CAS generation; NewFencingID mints 128-bit identities; the
      completion-side validation — launchID+operationID for lifecycle,
      launchID+connectionID for attachment callbacks — lands with the
      operations that produce completions, M2.5/M2.6)
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

- 2026-07-21, M1.6 + M1.11, protocol §rules: both items were marked [x]
  with parenthetical scope-narrowing notes instead of remaining open or
  carrying Deviations entries — caught in review. Resolution: M1.6's
  missing scope (SteamManaged/CustomCommand/empty-target checks,
  relative-target resolution, hook PATH pinning) was implemented rather
  than deferred; M1.11 reopened to [~] until M2.1 supplies the persisted
  activeConfigRevision. A parenthetical note is not a Deviations entry.
- 2026-07-21, M1.3, protocol §rules: the first checkpoint reworded M1.3's
  item text to drop "URL-mode ... hook relaxation" instead of marking the
  deferral, violating the never-reword rule. Caught in review. Resolution:
  original wording restored, and the relaxation implemented behind the
  AllowLifecycle gate (both in extension validation and in the legacy
  GameConfig.Validate branch) rather than deferred.
