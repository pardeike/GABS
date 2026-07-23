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
      to absolute at resolution, and review round 4 made unresolved hook
      commands fail Stage 1 with the hook's JSON path — game-level or
      profile-override — and checked filesystem path; final env absence
      (GABS_ABSENT_ENV) is now computed after the managed layer so a
      config unset of a managed name is never exported as
      present-and-absent)
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
      reach the child process; review round 4 made
      launch_mode_incompatible reachable — a hot edit giving a URL-mode
      game context fields maps to it via issue classification
      (ConfigIssue.Code) instead of collapsing into config_invalid,
      with mixed failures staying generic)
- [x] M1.11 show/list/status metadata: profiles, input constraints incl.
      maxLength/pattern, warnings, revisions — spec: 10, 09; tests: T-MCP
      (was blocked on the persisted launch revision; M2.1 supplies it and
      review round 4 closed the gap: games_show and games_status — single
      game and all-games — surface activeConfigRevision from the claim,
      distinct from currentConfigRevision, tested; warning-path matching
      also now escapes game IDs per RFC 6901 so IDs containing ~ or /
      keep their per-game warnings)
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
      pidRole; hammer + exactly-one-winner tests; review round 3
      completed the contract: the resolved lifecycle snapshot + pinned
      stopProcessName persist in the claim (custom exit codes round-trip,
      T-RT), the link-less fallback is now atomic under the transition
      lock with reader lock-retry on torn claims and cleanup on failure
      (tested via injected link/rename), game dirs are created 0700 and
      loose modes tightened with errors surfaced, and legacy-claim
      chmod failures now propagate instead of being discarded; the
      built-in graceful/force strategy pin lands with M2.6, which
      defines the strategy; review round 4 added the
      appliedLaunchInputsState field ("unavailable" for external
      snapshots, distinct from an empty list) with a round-trip test)
- [x] M2.2 Transition lock + domain-scoped fencing
      (launchID/operationID/connectionID; generation as CAS) — spec: 06;
      tests: T-FENCE
      (lock primitive done: flock on unix, LockFileEx byte-range on
      Windows — review round 3 replaced the initial exclusive-share
      CreateFile, which would false-positive on ordinary readers like
      antivirus — stable never-deleted file, bounded acquisition, no lost
      updates under 8-way contention; TransitionRuntimeState bumps the
      CAS generation; NewFencingID mints 128-bit identities; M2.5 landed
      FencedTransition (launchID+operationID validation with
      ErrFencingViolation) and every start-side completion — endpoint
      stamp, spawnState transitions, promote-to-active — goes through
      it; M2.6 landed the remaining domains: stop/kill completions and
      the fenced claim removal validate launchID+operationID (a failed
      stop finishing after a kill removed or replaced the claim is
      discarded and cannot resurrect state — tested with a mid-hook
      claim replacement and with concurrent stops under -race), and
      attachment callbacks validate launchID+connectionID (an old
      disconnect never clears a newer connection; the lease refresher
      stops on any fencing rejection); bounded lock contention now
      surfaces as ErrTransitionLockBusy and renders as a bounded
      operation_in_progress; M2.9's delivery callbacks reuse these
      primitives when they land. Review round 6 closed the paths that
      still wrote without carrying identity: status-side removal is now
      fenced (RemoveRuntimeStateIfCurrent — launchID must match and no
      unexpired operation may exist, with a mid-probe replacement test),
      attachment publication binds to the launch whose per-launch
      endpoint credential authenticated the handshake (an A-token
      connection can never attach to or promote claim B — the token
      rotation exists precisely for this) and undoes itself when the
      connection died before publication, and ownership refresh /
      failed-connect rollback are fenced to the loaded claim's launchID
      (rollback restores ownership fields only, never a whole stale
      snapshot, and never deletes or recreates over a current-schema
      claim))
- [~] M2.3 Hook runner (tree-kill, output capture, Windows Job Objects,
      exit-code contract) — spec: 01; tests: T-LIFE
      (RunStatusHook/RunActionHook in internal/process/hookrunner.go:
      unclassified/timeout/exec-failure = unknown never stopped; Setpgid
      + kill(-pgid) on unix, Job Object via kernel32 LazyDLL on Windows
      assigned right after Start (the µs pre-assignment window is part
      of the documented residual-straggler risk); direct child reaped
      before reporting, WaitDelay guards pipes held by detached
      grandchildren; 16 KiB tail-keeping capture with truncation marker;
      sanitized env contract incl. GABP-secret exclusion and folded keys
      on Windows; Windows script hooks (.bat/.cmd/.ps1/.vbs/.js)
      rejected at validation with the explicit cmd.exe /c spelling;
      review round 3: Windows behavioral tests written
      (hookrunner_windows_test.go — Job Object tree-kill with grandchild
      marker, exit-code contract, folded env, stderr tail) and a
      windows-latest CI lane added to test.yml; round 4 made that lane
      actually runnable — it runs vet + build + ./internal/process,
      where all Windows lifecycle code lives, with unix-only tests
      (unix binaries, POSIX mode bits, chmod-0000 semantics) gated by
      GOOS skips; the wider suite's unix-pathed fixtures are future
      porting work; flip to [x] when the lane is green — unix cells
      verified locally, Windows cells are written-but-unexecuted until
      CI runs)
- [x] M2.4 Liveness rule incl. attachment lease record, inspection-
      failure=unknown, URL helper PID exclusion — spec: 04; tests:
      T-LIFE, T-FENCE (attachment evidence)
      (EvaluateLiveness in internal/process/liveness.go with exact
      precedence: GABP → fresh fingerprint-matched attachment lease →
      status hook → PID fingerprint → stopProcessName; ProcessStartTime
      per platform — /proc stat field 22 (Linux), sysctl kern.proc via
      std syscall (macOS), GetProcessTimes + STILL_ACTIVE (Windows);
      PID-reuse mismatch = stopped, inspection failure = unknown and an
      empty name scan never downgrades it; helper-role PIDs are never
      workload evidence; expired leases are history; dead/unverifiable
      lease owners are not evidence; DiagnoseHook reports the
      hook-vs-GABP contradiction instead of hiding it; RuntimeAttachment/
      RuntimeOperation gained owner/executor PID start-time fingerprints;
      review round 3 closed the false-stopped paths: a no-claim
      evaluation still probes stopProcessName (lost-claim backstop), a
      zero attachment fingerprint is malformed-never-legacy evidence,
      DiagnoseHook also covers the attachment tier, the Linux process
      scan propagates EACCES-class inspection failures (positive matches
      still win; disappearance races stay silent), and Windows
      GetExitCodeProcess failures surface as unknown)
- [x] M2.5 Start pipeline Stages 2–5: complete pre-spawn claim, all-
      profile probing + external snapshots, endpoint alloc + per-launch
      token, spawnState transitions, Stage 4 outcomes (adopted /
      exited_during_start / unobserved policy), Stage 5 attach — spec:
      05, 03; tests: T-START
      (GateStart in internal/process/start_gate.go: claim gating by the
      liveness rule — already_running with both profiles, blocked_
      unknown_state with evidence, stale-clear-and-proceed; operation
      stamping with executor PID/start fingerprint and pinned deadlines;
      operation_in_progress for live in-flight executors (dead executors
      fall through); the unobserved supersession policy (reclaim only
      past the budget with source=none evidence); all-profile concurrent
      status-hook probing via launch.ResolveProfileLifecycles, single-
      attribution external snapshots (phase active/source external/
      observedProfile/pinned hooks/inputs unavailable), name-based
      detection with observedProfile unknown, multi-candidate refusal
      without snapshot, unknown-probe warnings; endpoint + per-launch
      token persisted into the claim via fenced transition, claim
      released on endpoint failure; Stage 3 spawnState transitions
      bracket cmd.Start via controller spawn observers; Stage 4:
      fenced promote-to-active with re-fingerprinted PID, adopted flag
      persisted + rendered with the context-survival warning,
      exited_during_start with exit code + launch.log tail on both the
      verification and bridge-wait paths, unobserved (URL modes,
      ProcessErrorTypeUnobserved) keeps the claim in phase starting
      with the operation cleared; Stage 5 outcome codes
      started_connected / started_bridge_pending. Review round 5 closed
      the contract holes: supersession now requires the completed-
      unobserved markers (Operation nil + spawnState spawned) so the
      spawning crash window stays occupied; absence-based stopped
      (empty name scan) never clears a completed-unobserved claim
      before its threshold and the reclaim carries a warning; stale-
      claim deletion and external-snapshot publication are fenced on
      the evaluated/held launch identity (a mid-probe replacement can
      no longer be converted or deleted); a failed spawning transition
      ABORTS the spawn (beforeSpawn returns error); the GABP token
      rotates every launch with only the port reusable; the runtime
      claim is the authoritative games_connect endpoint source
      (bridge.json demoted to fallback); one configured budget feeds
      both the claim deadlines and the starter's verification wait;
      Stage 4 assesses through the unified pinned liveness rule (hook
      consulted — wrapper-exit-with-hook-running is adopted-verified,
      hook-stopped is exited with hook evidence, absence is unobserved
      for any mode) and Stage 5 death is judged by liveness (unknown
      keeps the claim as bridge-pending); adoption is defined by
      child-exit + observation, not launch mode; external snapshots'
      hooks execute with the observed profile; GABP evidence requires
      a connected client, not map membership; an uninspectable
      executor blocks instead of reading as dead; claim-race losers
      re-evaluate the winner instead of fabricating a result;
      schema-2 claims are status-resolved by the liveness rule (hook-
      only external snapshots stay addressable; a mutex self-deadlock
      in that path was caught by the suite and fixed by threading
      GABP liveness); operation_in_progress renders phase;
      endpoint_unavailable is a stable code on all endpoint failures;
      exited_during_start carries resolved context, probe warnings,
      hook evidence, and next actions. The passive unobserved→active
      promotion landed with M2.6: a bridge attach promotes a starting
      claim to active, and a status observation with positive running
      evidence promotes a completed-unobserved claim — both fenced, and
      neither touches an in-flight operation. Review round 6 tightened
      the attach-side promotion to require the completed-unobserved
      markers (Operation nil + spawnState spawned), matching the
      status side: a mid-start claim keeps its phase for the start's
      own fenced completion, so phase=active can never coexist with
      operation.action=start. The Steam not-running advisory is
      M2.15's own item (EnsureClientRunning demotion) and is no longer
      carried here)
- [x] M2.6 Stop/kill: verification matrix, probe clipping,
      lastActionResult, stop_unsupported/kill_unsupported,
      operation_in_progress semantics — spec: 06; tests: T-LIFE, T-FENCE
      (ExecuteStopAction in internal/process/stop_gate.go: one-operation
      admission under the transition lock — an in-flight attempt refuses
      with operation_in_progress carrying phase + started-at + deadline
      (kill during stopping included, hook never run twice; exactly-one-
      executor proven under -race with four contenders), an expired or
      provably-dead attempt is recorded as lastActionResult interrupted
      and replaced, and bounded lock contention surfaces as
      operation_in_progress via ErrTransitionLockBusy, never a hang.
      Capability is resolved from the pinned snapshot only: the action
      hook wins, else the built-in fallback needs a pinned workload PID
      (helper PIDs never qualify) or stopProcessName; stop_unsupported
      names the launch mode and points at games_kill when force is
      configured, kill_unsupported never falls back to the stop hook,
      and refusals persist nothing. The built-in graceful/force strategy
      pin (M2.1's last open pin) is stamped at claim creation
      (sigterm/sigkill, taskkill/taskkill_force) and dispatched by the
      executor: fingerprint-verified workload PID first (an unverifiable
      PID is never signaled — PID reuse), then every name match with a
      collision warning; a scan failure is action_failed with detail,
      never silent. Hooks run with the lock released and external
      snapshots' hooks get the observed profile. The post-action
      verification matrix follows design/06 row by row: positive sources
      (status hook / workload fingerprint / name scan) decide —
      running → action_succeeded_running (claim kept, phase promoted
      active), all-stopped → terminated (launcher child reaped first,
      fenced claim removal), any-unknown → termination_unverified with
      the claim kept and phase restored; no-source → hook success clears
      (stop-only wrapper); bridge evidence — a live in-process GABP
      connection or a fresh fingerprint-matched foreign attachment
      lease — keeps the claim as termination_unverified and never
      upgrades to a positive running verdict nor clears under a live
      bridge (T-FENCE), while self-owned records defer to the in-process
      connection state and dead-owner leases are history. Verification
      status probes are clipped to min(hook timeout, remaining window),
      behaviorally tested against a real hanging hook. Failures persist
      lastActionResult{action, outcome, exitCode, stderrTail, detail,
      treeKillWarning, timestamp} — detail is a schema addition for
      builtin/verification facts that have no exit code — and every
      completion is fenced by launchID+operationID (a mid-hook claim
      replacement discards the completion with a warning and cannot
      resurrect state). MCP wiring: games_stop/games_kill route schema-2
      claims through the pipeline (legacy claims keep the old path until
      M2.8 normalizes them), render the stable codes with per-code next
      actions (operation_in_progress is non-error), and terminated
      releases the controller, bridge client, mirrored tools, and
      diagnostic bridge file; games_status reports phases
      stopping/killing with the attempt's timing, surfaces
      lastActionResult, and never removes a claim carrying an in-flight
      operation — closing a latent race where a status call could
      delete a mid-start claim between publication and observability.
      Review round 7 hardened the credential and authority contracts:
      connections are credential-bound end to end — the connector
      refuses to even dial a credential that contradicts the current
      claim's endpoint, attachment publication returns a typed result
      and a client whose credential stopped matching mid-connect is
      closed and removed before it can mirror tools or count as GABP
      evidence, and the stable stale_bridge_credential outcome is now
      emitted by real paths: games_connect fails fast when the observed
      workload provably runs with another launch's credentials
      (process environment corroborates the exact claim endpoint or
      exposes staleness — it never replaces it; the launcher-reuse test
      was inverted accordingly), and games_start surfaces it as a
      warning instead of adopting the stale environment. checkGameStatus
      makes the current-schema claim the FIRST status authority — the
      unified rule over the pinned context runs before any in-memory
      shortcut (a live wrapper PID can no longer hide a pinned hook's
      stopped verdict, and M2.7 recovery is reachable regardless of
      controller state), with GABP evidence only from a
      credential-bound live client (the in-process attachment ref must
      carry the claim's launchID). Start-side cleanup is fenced end to
      end: the deferred release goes through ReleaseStartClaim
      (launchID + own-or-no operation + no live foreign attachment),
      never a bare game-ID removal that could delete a successor; and
      the status-path removal (RemoveRuntimeStateIfCurrent) now refuses
      ANY admitted operation — expired or not; recovery owns those —
      and any live foreign attachment.
      Attachment callbacks (M2.2's remainder) landed here, as did both
      passive promotions (M2.5's remainder). Two adjacent fixes: the
      ownership-lease save was rewritten from a blind whole-claim
      overwrite to an in-place transition after the new connect
      integration test caught it clobbering concurrent fenced writes,
      and RunStatusHook was refactored to share its classifier with the
      new clipped variant. Review round 6 (11 findings, all accepted)
      hardened the remaining unfenced paths: the controller-backed
      status branch consults the claim before any cleanup and never
      deletes one carrying an in-flight operation (the legacy child-
      exited cleanup applies only to claimless/legacy games); stop
      verification reloads the latest claim every poll — an attachment
      appearing while the hook runs is seen, a cleared record stops
      counting, and a removed/replaced claim ends polling as a warned
      termination_unverified — with a final under-lock re-check that
      refuses removal outright under a fresh live foreign lease; the
      live-GABP-versus-stopped-hook contradiction stays running
      (action_succeeded_running) with the design/04 warning, keeping
      the unverified treatment only for the stop-only-wrapper and
      foreign-lease cells; post-terminated cleanup is identity-tied
      (the observed controller/client instances only, never a
      successor's, and never a second runtime-state removal — the
      diagnostic bridge file is also left alone); games_stop/games_kill/
      games_status/games_connect all report activeProfile and phase per
      design/10 (EffectiveClaimProfile exported for the observed-profile
      rule); and no-arg games_status probes all games concurrently under
      their individual timeouts with s.mu released during evidence
      probes and deterministic output order — proven by a
      three-slow-hooks timing test. Review round 8 (12 findings, all
      accepted) completed the credential-binding and safe-deletion
      contracts end to end: attachment publication is an explicit typed
      result and EVERY unpublished connection closes before mirroring
      (claim vanished mid-handshake, unreadable claim, lock/save
      failure, fingerprint failure, or no-longer-current — typed
      superseded; credential mismatch — typed stale), with rollback
      targeting exactly the record the loser created (a concurrent
      publication B survives A's rollback, tested); one common
      claim-bound lookup (live client + in-process attachment ref
      carrying the freshly loaded claim's launchID) now feeds connect's
      already-connected fast path (moved behind the claim load), start
      gating, stop/kill GABP evidence, status, mirroring, attention,
      and tool calls; liveness is caller-aware (self-owned leases defer
      to the caller's actual socket, foreign owners that cannot be
      inspected taint downstream stopped evidence to unknown); all
      final deletion guards are tri-state (only positively dead or
      expired attachments permit removal; self-owned records re-check
      the live socket under the lock; GateStart's stale-claim clearing
      re-checks operation identity and attachments and re-evaluates
      instead of deleting); ReleaseStartClaim requires the exact
      original start operation (the Stage-5 exited path makes its own
      fully fenced operation-free decision); Stage 4 outcomes are
      emitted only after their fenced transitions land (fencing loss →
      supersession, persistence failure → occupied claim + structured
      failure); recovery reports fence loss as an explicit superseded
      result rendered from the CURRENT claim; games_status surfaces the
      liveness observation (verdict/source/detail/hook facts/warnings)
      with contradiction diagnosis enabled under bridge evidence; and
      the detach path uses a no-create transition lock so teardown can
      never be resurrected)
- [x] M2.7 Restart recovery: liveness-driven, interrupted-phase
      normalization, spawning-window verdicts, executor-vs-owner —
      spec: 07, 05 Stage 3; tests: T-FENCE, T-START (claim-window),
      T-GATE
      (RecoverInterruptedClaim in internal/process/recovery.go, invoked
      lazily from the status evaluation path — no poller, no startup
      sweep: recovery happens on the first observation that finds a dead
      bounded attempt (executor provably gone or deadline expired, via
      the shared OperationInFlight predicate that stop admission now
      also uses — a provably dead executor unblocks a retry within the
      window too). Stop/kill attempts normalize under the transition
      lock fenced by launchID + the dead attempt's operationID:
      lastActionResult{outcome: interrupted} recorded, operation
      cleared, phase per liveness — running → active, stopped → fenced
      removal (through the same attachment-guarded remove as stop
      completions: a live foreign lease flips the verdict to kept-
      active instead of clearing under a live bridge), unknown → active
      with the unknown verdict reported; a dead attempt never renders
      as operation_in_progress and an immediate retry is admitted.
      The crash-during-spawn window follows design/05 Stage 3 exactly:
      preflight + provably dead owner is the one safe removal and runs
      no probes (proven with a running-answering pinned hook that is
      never consulted, both in recovery and in GateStart's claim-window
      path); spawning/spawned with a dead attempt resolve by the normal
      liveness rule — running promotes to active, definitive stopped
      removes, genuinely unknown preserves the claim occupied without a
      write. Executor-vs-owner: normalization never touches the
      attachment record (a CLI executor dying leaves the server-owned
      bridge lease intact, tested) and the dead executor's late
      completion stays rejected by its operationID. Recovery never
      replays stop/kill hooks — liveness evidence only, tested via an
      action-hook counter. A start attempt's interruption is not
      recorded as lastActionResult (that field is the stop/kill journal
      replacement, design/06). Recovered claims render truthfully in
      games_status: normalized phase, interrupted lastActionResult, no
      operation)
- [x] M2.8 Legacy claim migration + full normalization — spec: 07;
      tests: T-RT
      (NormalizeLegacyClaim in internal/process/legacy_migration.go: the
      first lifecycle touch — games_connect, games_stop/games_kill via
      the pipeline router, or a start's duplicate check in GateStart;
      never read-only status — fully normalizes a marker-absent claim
      under the transition lock: schema marker stamped, launch ID +
      generation minted (fencing valid from then on), phase active +
      unprofiled + source gabs, built-in fallback pinned from the
      legacy claim's own stopProcessName and PID with a zero
      fingerprint kept as weak evidence, and — the single recorded
      exception to never-consult-config — launch mode + PID role from
      the current entry, with normalizedFromLegacy + the revision
      recorded; idempotent, a marker-stamped claim is returned
      untouched. Review round 7 rejected two interpretations I had
      proposed, and both were reversed: the discriminator is exact
      marker absence (schemaVersion == 0 — any other version is a
      different schema, never legacy), and a pre-profile GABS launch's
      inputs are a KNOWN EMPTY set, never the external-snapshot
      "unavailable" state. The endpoint migration is marker-absent,
      locked, and genuinely one-shot per the contract: the single
      legacy bridge.json candidate is captured while the normalization
      transition still holds the lock (the sole live-attach read of the
      file), validated by actually connecting, persisted through the
      minted launch fence exactly once, and followed by
      attachment-record publication under the migrated credential; a
      failed validation does not reopen the window — the marker was
      stamped on that same touch and the file is never reread (tested:
      a later live bridge.json still refuses). A connect refused by the
      ownership gate does not normalize or burn the candidate (the
      lifecycle touch is the connect that proceeds). bridge.json is
      structurally diagnostic-only everywhere else: no-claim connects
      refuse ("nothing is attachable"), fresh pre-endpoint claims
      refuse ("no attachable endpoint"), and external snapshots refuse
      with "attachment unavailable" — checked before any process-
      environment inspection. Degraded status-before-normalization
      renders no newer-schema-only fields and never normalizes, tested)
- [x] M2.9 Expected-context digests + welcome `observed` parsing +
      per-channel aggregation matrix + contextDelivery persistence —
      spec: 03, 07; tests: T-DELIV
      (ComputeContextDigests/EvaluateContextDelivery in
      internal/process/context_delivery.go: per-launch random salt,
      salted SHA-256 of the argv payload (argv[0] excluded — element
      zero legitimately differs across hops) using the pinned
      length-prefixed encoding (design/20), the platform-canonical cwd
      (absolute, symlink-resolved, case/separator-folded on Windows),
      and each forwarded env value. Channel membership is persisted
      explicitly (managedEnvSha256 vs contextEnvSha256) rather than
      inferred from GABS_/GABP_ prefixes — the managed layer includes
      non-prefixed names (SteamAppId/SteamGameId, SystemRoot/WINDIR),
      so an unprofiled SteamManaged or Windows launch never acquires a
      spurious context channel. The cwd digest state is tri-valued:
      a pinned digest (comparable), CwdUnverifiable (the legacy
      relative workingDir), or neither — the empty-digest case, which
      is a spawn-side canonicalization FAILURE and reports the channel
      unknown; a reported cwd that cannot be canonicalized is likewise
      unknown, never a false mismatch. The gabp client parses the
      binding welcome wire shape observed:{argv, cwd, envValues,
      envAbsent} (design/20) — a name in envValues was observed with
      that value, a name in envAbsent was checked and is absent, a name
      in neither is unreported; a name in both is contradictory and
      never verifies; an expected-present name that arrives in
      envAbsent is a positive mismatch. Raw observed values are hashed
      locally, compared, and DISCARDED via a consume-once
      TakeObservedContext (cleared on every close/failure path too).
      Digests pin at spawn in the same fenced transition as the
      endpoint, AFTER MaterializeSpawnSpec resolves the effective
      executable and working directory (SteamManaged resolves its app
      once; digesting, CheckProcessSize's argv[0], and Controller.Start
      all consume that one immutable spec). Verification compares
      against the spawn-time digests, never current config, proven by
      a restart+fresh-server test. The four channels aggregate by the
      pinned matrix, every row tested. The verdict persists via a
      delivery callback attributed to EXACTLY the connection that
      produced the report (the publication result is carried, never
      reacquired — an A-report/B-replace interleaving test proves A's
      late report cannot overwrite B's verdict), fenced by launchID +
      that connectionID (design/06), in both the connector and
      migration paths. games_start (started_connected) and
      games_status render the persisted verdict; external snapshots
      persist contextDelivery: unknown. deliveriesVerified counting is
      M2.10's history store. Review round 9 (16 findings, all accepted;
      the two proposed interpretations were rejected as instructed):
      the wire shape is envValues/envAbsent (not the private env/absent
      it originally shipped), the argv digest is length-prefixed, the
      SteamManaged default cwd is materialized before hashing,
      connections are bound only after handshake authentication AND
      attachment publication through one atomic client+launch+
      connection binding used by every consumer, delivery reports are
      connection-attributed, mirroring and mirrored-call handlers
      revalidate/resolve the current bound client, GateStart takes a
      dynamic launch-bound BridgeBound callback, the removal guards are
      connection-scoped, contradiction diagnosis is automatic in every
      liveness caller, recovery passes CallerInstanceID and reports
      fence loss as re-evaluation (never phase→status mapping), the
      new transition-failure branches carry stable MCP codes
      (supersededStartRefusal re-evaluates to already_running/
      operation_in_progress/blocked_unknown_state; occupied persistence
      failure is blocked_unknown_state), and the detach s.mu/
      transition-lock inversion is removed)
- [!] M2.10 History store + classifier + input-combination buckets +
      edit notice + causeClass/track-record rendering — spec: 08; tests:
      T-TRACK
      (round 13 reopened. F2: attribution is now a SINGLE central completion
      step at dispatch over every core-management tool (games_status/show/list/
      start/stop/kill/connect/tools/…), not a four-tool whitelist; causeClass,
      track-record line, and nextActions are filled INDEPENDENTLY so a
      partially-attributed result cannot escape; the codeless terminal branches
      (unreadable/unnormalizable stop-kill claims, connect legacy/endpoint/
      ownership/connection failures) now carry authorized codes
      (blocked_unknown_state / endpoint_unavailable); the classifier no longer
      silently defaults an UNMAPPED code to environment — it returns no class,
      so an untaught code fails visibly in the exhaustiveness test and as a
      missing causeClass in the real-handler battery. Handler-level tests
      trigger the real branches (games_status/show game_not_found, corrupt-claim
      stop/kill, connect failure). F4: a real games_start with a status hook
      reporting stopped exercises the production hook-stopped exitedFailure
      branch and asserts game class + preserved hookEvidence + recorded game
      failure. F9/F5 (counter double-count on side-file-first transitions)
      remains — see the round-13 F5 commit, which keeps this at [!] pending
      reviewer adjudication of the attempt-vs-commit counter semantics)
      (F6 RESOLVED by reviewer adjudication: exited_during_start is `game` by
      the evidence-based default — a post-spawn exit is attributed to the
      workload because GABS observes only the first process it created and
      cannot distinguish a game binary from a user-owned wrapper/container
      launcher; launch mode, target shape, and status-hook results are not
      cause evidence. design/05:220 and its dependents were amended to that
      implementable contract, the launch-mode heuristic and classification-only
      config flag were rejected (recorded in the round-12 Deviations), the
      ClassifyContext.WrapperExit seam and its unit-only branch were removed,
      and production tests prove DirectPath/CustomCommand/SteamManaged and the
      status-hook render path all classify game. spawn_failed (process-creation
      failure) stays environment; wrapper stderr surfaces in outputTail with
      guidance to read it)
      (internal/process/history.go + classifier.go: a per-game 0600
      atomic history.json beside runtime.json, every read-modify-write
      under the per-game transition lock so a server delivery callback
      and a CLI stop lose no increments (proven by a 40×2 concurrent
      -race test). The context hash is INPUT-FREE by construction —
      composed from the resolver's own effective base context (a shared
      launch.ResolveBaseContext reusing the real env merge — target, mode,
      base argv, the config-declared env layer with platform folding,
      effective absences, cwd, resolved lifecycle — NOT a second
      config-direct env implementation; round 10 P2-10), never from the
      post-input Resolved
      (inputs bind both args AND env via LaunchInputConfig, so hashing
      Resolved would split arena/tutorial into two contexts instead of
      one context + two buckets). Granularity is the reset mechanism:
      editing profile B leaves A's hash intact, adding profile C resets
      nothing, editing a shared game-level arg changes every profile's
      hash — all tested. Two-level reset: a context change resets the
      whole entry; an input-DECLARATION edit (ResetInputBuckets) drops
      only that declaration's buckets while base counters and the
      bare-set proof survive. Successes bucket by sorted input names +
      declaration hash + a per-game-keyed value digest (values never
      persist in the clear), LRU-capped at 16 per input set. Four split
      counters increment at exactly their points — Stage 4 verified →
      workloadStarts++ + consecutiveFailures reset + lastGood refresh,
      Stage 5 connected → bridgeConnects++, fully-verified welcome →
      deliveriesVerified++, verified stop → cleanStops++; a terminal
      failure of an accepted attempt with a resolved context →
      lastFailure + consecutiveFailures++. call-class and config_invalid
      NEVER mutate history (a caller typo distorts no proof — tested).
      The classifier is a pure function built from design/05's bad-case
      Class column + design/08's five definitions: static codes map by
      the code alone, and only launch_spec_unresolvable and unobserved
      are proof-adjusted (never-proven → config, proven → environment);
      an unproven input combination adjusts CONFIDENCE (a secondary
      candidate-input note), never the class. Rendering: every failure
      result carries causeClass + a one-line track record (the split
      "workload proven, bridge never connected → game-side" hint
      included), class-keyed nextActions where no non-config class
      proposes a config edit (template-level assertion), and the
      once-per-edit visibility notice (noticeShownForHash) that fires
      only for proven + last-failure-non-config + hash-changed, not for
      additive edits. Privacy is structural: the lastGood entrySnapshot
      may hold env values but lives only in the 0600 file — rendering
      consumes counters + the computed line, never the raw record, so
      it cannot leak (tested). Corrupt/missing history degrades to "no
      track record" without failing any lifecycle op. Round 10 landed the
      full behavioural surface here — not deferred: the record/render split
      (a terminal failure is WRITTEN while the claim is alive and fenced to
      the launch, inside startGame, then RENDERED after release), every
      history event fenced to the launch identity, the pinned
      HistoryContextHash (delivery/stop/recovery credit the launch, never a
      hot-config recompute), bridgeConnects++ at every credential-bound
      attachment, evidence-based exited_during_start, the reload-driven
      declaration-edit invalidation wired into every start, and per-profile
      proof + counters in games_show (an edited context reads never-proven).
      Only the doctor --show-last-good surface and the CLI track-record
      DISPLAY remain M3.2's doctor work. Round 11 completed the coverage the
      round-10 tests missed: EVERY structured failure now carries causeClass +
      (with a resolved context) a track-record line via one mandatory read-only
      attribution path — including the proof-adjusted launch_spec_unresolvable
      (a target that vanished after proven starts reads environment, computed
      from input-free coordinates with NO history mutation), the pre-resolution
      call-class errors (no context, the NEUTRAL "no successful starts" track
      line per design/08:39, no mutation), config_invalid
      / launch_mode_incompatible, and the internal stop/kill execution error;
      Stage 4 verified now credits workloadStarts++ at EVERY promotion path —
      synchronous, passive status observation, bridge attachment, and restart
      recovery — inside the same fence that flips starting→active, from an
      identity (hash + snapshot + bucket) PINNED in the claim at publication so
      an unobserved-then-connected launch is no longer "bridge connected 1× but
      no successful starts"; an unobserved accepted attempt is now recorded as a
      proof-adjusted failure (P2-3) that a later promotion resets; removing an
      input declaration invalidates its buckets (not only editing one); a clean
      terminated stop carries no failure cause; buildHistoryContext is split
      into a pure compute and the accepted-start mutation)
- [~] M2.11 bridge.json diagnostic fields; env-only live contract
      preserved — spec: 03; tests: T-DELIV
      (round-12 F10: the spawn-boundary diagnostics stamp is fenced to the
      launch's endpoint — StampBridgeDiagnostics requires the expected
      port+token and returns ErrBridgeEndpointRotated for a successor's token; a
      stamp WRITE failure surfaces as a structured start warning. Round-13 F1:
      that generation check was a non-atomic read→compare→write that an
      interleaving rotation could defeat (A reads token A, B publishes token B,
      A rewrites restoring token A). Fixed by a dedicated per-(configDir,gameID)
      in-process write lock held across the WHOLE read-compare-write in BOTH
      StampBridgeDiagnostics and PrepareBridgeEndpointForStart, so the two can
      no longer interleave; cross-process same-game starts are already
      serialized by GateStart's transition lock. The earlier "A-spawns/
      B-rotates" cell was SEQUENTIAL and did not exercise the interleaving —
      replaced by a 400-iteration concurrency invariant (the final token is
      always the successor's, never restored to the stale one) and a
      deterministic after-read-barrier test proving a rotation cannot land while
      the stamp holds the lock. Reopened to [~] pending reviewer re-verification.)
      (config.BridgeJSON gained three diagnostic-ONLY fields — profile,
      configRevision, startedAt (the binding key name, design/20:235; RFC3339)
      — stamped AT SPAWN (design/20 "written at spawn"; round 11 P2-7/P2-8) by
      config.StampBridgeDiagnostics from the spawn observer, ONLY on a
      successful spawn. Endpoint preparation writes only port/token; a
      pre-spawn failure (spec_too_large, fencing, process-creation) therefore
      publishes NO diagnostics for a workload that was never created (tested).
      A reused endpoint's preparation clears the previous launch's stale
      diagnostics until the spawn boundary restamps this launch's values. The
      fields are omitempty and the non-start writers (Ensure/WriteBridgeJSON,
      all test-only) stamp nothing, so no production path writes blank
      diagnostics. The live contract stays env-only: the diagnostics
      never enter the endpoint-reuse decision (validBridgeEndpoint checks
      port/token/gameId alone — tested) and never reach a live path — the
      env-only regression lock seeds a claim with one profile/revision, hand-
      writes a bridge.json with BOGUS diagnostic fields, and asserts
      games_status reports the CLAIM's activeProfile/activeConfigRevision
      while the bogus markers appear nowhere (it passes because nothing reads
      them — the lock fails the moment a future change wires a diagnostic
      field into attribution). doctor DISPLAY of these fields is M3.2)
- [~] M2.16 (added) Test-server isolation + background-task join — spec: 30
      §race gate; tests: T-TRACK/T-DELIV race lanes
      (round 12 F4 discovered work. NewServerForTesting defaulted configDir to
      "" → the real ~/.gabs, so a test that skipped SetConfigDir read/wrote the
      user's live GABS state; it now creates an isolated os.MkdirTemp dir, and
      the config-package write tests (concurrency/env-isolation) and the
      bridge-cleanup test were moved off ~/.gabs. Server.Shutdown() cancels
      (shutdownCh) and JOINS every detached task before TempDir teardown.
      Round 13 corrections: (F3) EVERY background bgWG.Add — async mirroring,
      the nested attention spawn, the lease refresher, and the background GABP
      connect — now routes through admitBackgroundTask, which does the shutdown
      check AND the Add together under s.mu (the same lock Shutdown holds to
      close admission), so no positive Add can race bgWG.Wait; (F6) the
      constructor-created isolated dir is tracked as ownedTempDir and removed by
      Shutdown AFTER the joins, and the caller's SetConfigDir dir is never
      touched. Race tests cover concurrent admission-vs-shutdown, concurrent
      attachment-vs-shutdown, and the construct→SetConfigDir→Shutdown cleanup.
      Reopened to [~] pending reviewer re-verification of the join safety)
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

- 2026-07-22, M2.10, round 12 F6, design/05:220 (RESOLVED by reviewer
  adjudication — design amended): the bad-case map's fast-exiting-wrapper →
  environment row was not implementable — no producer fact distinguishes a
  container/wrapper exit from a game crash at the first process GABS creates,
  and Resolved/LaunchSpec expose no chain/container marker. Adjudicated
  contract: a post-spawn exited_during_start is `game` by the evidence-based
  default; launch mode, target shape, and status-hook "stopped" results are NOT
  cause evidence; the wrapper's stderr is surfaced in outputTail and the caller
  is told to read it; an OS process-CREATION failure stays spawn_failed →
  environment. design/05 (bad-case rows + a new rationale section), design/30
  (T-TRACK cells) were amended; the ClassifyContext.WrapperExit seam and its
  unit-only branch were removed and replaced by production tests over
  DirectPath/CustomCommand/SteamManaged + the status-hook render path. Two
  alternatives were REJECTED and are recorded so they are not re-proposed: (1) a
  launch-mode heuristic (CustomCommand/SteamManaged → environment) would
  misclassify the common real game crash under those modes as an environment
  problem — false attribution is worse than the honest default; (2) a
  classification-only config flag (launchKind: container) would push a cause
  GABS cannot observe onto the user to declare and would drift the moment the
  command is reused for a non-container target. M2.10 → [x].
- 2026-07-23, M2.10, round 13 F9/F5 (history-before-save double-count — FIXED
  by commit ordering; awaiting reviewer confirmation): round 12 acknowledged
  the flagged double-count only for workloadStart (launchID dedup) and
  UNILATERALLY declared bridgeConnect/delivery/cleanStop "per-attempt" — which
  the round-13 review correctly rejected: recording a disagreement does not
  authorize [x], and it was not part of any adjudication. Round 13 instead
  FIXES the root cause for ALL four counters. The transition primitive gained
  an afterCommit hook (TransitionRuntimeStateThen / FencedTransitionThen) that
  runs under the same lock but only AFTER SaveRuntimeState succeeds; every
  history increment — workloadStarts (all 4 promotion paths), bridgeConnects,
  deliveriesVerified — moved from the mutate callback into afterCommit, and
  cleanStops now records AFTER RemoveRuntimeState commits. So a runtime save/
  removal failure never advances history ahead of the claim, and the flagged
  retry-double-count is gone. Proven by failure-injection tests (inject a save
  failure → the counter is not recorded; the retry records it exactly once).
  workloadStart additionally keeps its launchID idempotency as belt-and-braces.
  RESIDUAL: a crash in the tiny window between the successful runtime save and
  the afterCommit history write would UNDERCOUNT by one (history behind, never
  ahead) — strictly better than the flagged double-count and not a corrupted
  proof signal. M2.10 stays [!] pending REVIEWER CONFIRMATION that the
  commit-ordering fix resolves F9 (and whether the residual undercount window is
  acceptable or a logical-event-ID reconciliation is also required); per
  protocol §18 a deviation stays [!] until the adjudicator signs off, not when
  the implementer believes it resolved.
- 2026-07-22, M2.10, round 12 F3, design/10:37 (authorized codes): two codes
  invented in round 11 were removed. Malformed profile/launchInputs CONTAINER
  arguments (wrong JSON type) now return a plain protocol-level invalid-params
  error with NO stable code — they are not lifecycle outcomes and the
  exhaustive list has no code for them. The internal stop/kill execution error
  (lock/persistence/system failure) maps to the authorized state code
  blocked_unknown_state, not a new action_execution_failed. A classifier
  exhaustiveness test now maps every code in design/10's list to exactly one
  class (failure) or none (success/pending), rejecting future invented codes.
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
- 2026-07-21, M2.1 + M2.2 + M2.3, protocol §rules + design/06: the
  M2.1/M2.2 checkpoint notes claimed coverage that did not exist — a
  "hardlink-fallback test" (no test forced that branch, and the branch
  itself was non-atomic and unlocked) and "0600/0700" enforcement (game
  dirs were still created 0755, chmod failures silently discarded).
  Additionally, the Windows transition lock was implemented as
  share-none CreateFile instead of design/06's LockFileEx without
  recording a deviation, and M2.3 was marked [x] with no Windows
  behavioral test executing any of its Windows-only code. All caught in
  review round 3. Resolution: locked atomic fallback + reader lock-retry
  with real forced-branch tests, 0700 enforcement with surfaced errors,
  LockFileEx byte-range locking per design (share-none rejected because
  ordinary readers like antivirus would cause false
  operation_in_progress), M2.3 reopened to [~] until the new
  windows-latest CI lane runs the new Windows tests.
- 2026-07-22, M2.6, design/04 + design/06 (interpretation, adjudicated):
  the stop-verification matrix treats bridge evidence asymmetrically —
  a live in-process GABP connection versus an explicit stopped status
  hook stays RUNNING with the design/04 contradiction warning, while
  the two narrow cells stay `termination_unverified`: the stop-only
  wrapper (no independent source) and a fresh foreign attachment lease
  (T-FENCE: a CLI can never clear a claim under another process's live
  bridge, but a lease alone cannot upgrade to positive running).
  Adjudicated by review rounds 7-8; recorded here because design/04
  row 1 alone could be read to make any bridge evidence positive.
- 2026-07-22, M2.6, design/06 §lastActionResult: RuntimeActionResult
  gained an additive `detail` field for built-in and verification facts
  that have no exit code or stderr to speak for them (a builtin scan
  failure, a verification summary, the interrupted reason). The
  specified fields are unchanged; review round 7 judged the addition
  reasonable without a contract amendment.
- 2026-07-22, M2.10, design/08 §failure attribution (adjudicated —
  codes design/08 does not tabulate): the classifier assigns cause
  classes to the stop/kill codes the bad-case map omits.
  stop_unsupported / kill_unsupported → `config` (a configuration gap:
  no stop/kill mechanism is configured — the fix is adding a hook or
  stopProcessName, which is the one legitimate config-edit case).
  action_failed / action_timed_out → `environment` (the host/process
  did not cooperate with a configured action; not a config defect and
  not GABS state). termination_unverified and operation_in_progress →
  `state` (per design/08's state definition). Recorded here because
  design/08's five definitions cover these by category but the specific
  code→class mapping is a judgment made in this milestone.
- 2026-07-22, M2.10, review round 10 P1-2 (single terminal finalizer):
  the finding asked for ONE mandatory finalizer that both classifies-and-
  records every start failure. It is implemented as a RECORD/RENDER SPLIT,
  not one call: `recordTerminalStartFailure` writes the fenced history
  mutation from inside `startGame` while the claim is still alive, and
  `finalizeStartFailure` renders causeClass + track record + class-keyed
  actions + edit notice in the handler after the claim has been released.
  The split is forced by the claim lifetime — the deferred claim release
  (and, on the exited-during-GABP path, an explicit RemoveRuntimeStateIf-
  Current) runs BEFORE the handler, so a fenced RecordFailure in the
  renderer would find no claim and silently drop the failure (this was the
  observed bug: TestExitedDuringStartCarriesGameCauseAndActions recorded
  `<nil>`). Both halves are mandatory and cover the identical code set
  (exited_during_start, spawn_failed, endpoint_unavailable, spec_too_large);
  the record side is gated to the exact ProcessError types the renderer
  labels spawn_failed so write-coverage never exceeds render-coverage.
- 2026-07-22, M2.10, review round 11 P2-5 (reversal of round 10 finding 8) —
  SUPERSEDED by the round-12 F6 adjudication below: round 10 directed
  exited_during_start to classify environment when a status hook surfaced the
  stop and game otherwise; round 11 corrected the status-hook inference. Round
  11 still retained a hypothetical ClassifyContext.WrapperExit seam. That seam
  was REMOVED entirely by the F6 adjudication (see the round-12 F6 Deviations
  entry): there is no producer fact for a wrapper/container exit at the first
  process GABS creates, so exited_during_start is ALWAYS game and the design row
  was amended. This note is retained only for history; the WrapperExit branch no
  longer exists.
  Test coverage of the record sites: the inline exitedFailure recorder
  (T-TRACK exited-during-start) and the deferred pendingFailCode recorder
  (T-TRACK spec_too_large) are both exercised through the real handler with
  a history-write assertion. The one ordering NOT deterministically covered
  is the promote-then-die-during-GABP branch (exitedFailure moved above
  RemoveRuntimeStateIfCurrent so the fenced write still sees our launchID):
  a brief-lived helper reaches it only nondeterministically (most runs exit
  before promotion), so it is verified by reasoning + the shared recorder,
  not a dedicated test — flagged here rather than implied as covered.
- 2026-07-22, M2.9, design/07 §"full field contract" (adjudicated
  clarification): the RuntimeContextDigests schema gained two fields
  beyond design/07's enumerated list — `absentEnvNames` (the
  GABS_ABSENT_ENV names, never values, needed to verify isolation after
  a restart) and the cwd tri-state (`cwdUnverifiable` plus the
  empty-digest = canonicalization-failure convention). Review round 9
  judged both substantively necessary; recorded here per protocol as an
  adjudicated design clarification, not a parenthetical. Channel
  membership is also persisted as separate managedEnvSha256/
  contextEnvSha256 maps rather than one envSha256 map, because prefix
  inference cannot classify the non-prefixed managed names — a schema
  refinement within the same additive clarification.
- 2026-07-22, M2.8, design/07 §legacy claims: "first lifecycle touch"
  is implemented as the first lifecycle touch THAT PROCEEDS — a
  games_connect refused by the runtime-ownership gate neither
  normalizes the claim nor burns the one-shot bridge.json migration
  candidate. Design/07 does not address the ownership-refused case;
  normalizing on a refused touch would spend the migration window
  without an attach attempt. Flagged for formal clarification in the
  design docs (M3.3's docs pass).
- 2026-07-21, M1.10 + M1.11 + M2.1 + test.yml, protocol §rules +
  design/21 ordering: review round 4 caught four more
  completeness/ordering violations — M1.10's launch_mode_incompatible
  was unreachable (no code path emitted it) despite the item being [x];
  M1.11 stayed open while M2.1–M2.4 advanced, violating strict
  milestone order after its M2.1 dependency had landed; M2.1's "full
  field contract" lacked appliedLaunchInputsState; and round 3's
  windows-latest CI lane ran the full unix-fixtured suite and could
  never pass. Resolutions: launch_mode_incompatible emitted via
  ConfigIssue.Code classification with a mixed-failure guard; M1.11
  completed (activeConfigRevision in show/status from the claim) and
  closed; appliedLaunchInputsState added with round-trip coverage; the
  Windows lane scoped to vet + build + ./internal/process with GOOS
  gates on POSIX-only tests, wider fixture porting recorded as future
  work rather than claimed.
