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
- [x] M2.3 Hook runner (tree-kill, output capture, Windows Job Objects,
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
      porting work. CLOSED: the windows-latest lane is GREEN (PR #67, run
      30120986500) — the Windows behavioral cells executed and passed. Round-20
      also fixed the newly-exercised Windows claim-read races surfaced by the
      first real run: LoadRuntimeState now retries transient sharing/lock/
      delete-pending (access-denied) violations and the legacy-permission
      tighten is unix-only (Windows protects the token via NTFS ACLs on the
      private ~/.gabs dir, not unix bits). Round-21 cleared the PR's CodeQL
      aggregate check (was red): the three json.Number cursor/limit parsers now
      ParseInt with bitSize 0 so out-of-range values are rejected instead of
      silently truncated on 32-bit int, three hand-quoted tool-result messages
      use %q, and test.yml declares permissions: contents: read.)
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
- [x] M2.10 History store + classifier + input-combination buckets +
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
      stop/kill, connect failure). ROUND 14 (F2 reopened): the central step also
      ran over games_call_tool, which forwards the GAME's GABP payload as
      StructuredContent — so a game result or error whose key collided with a
      GABS stable code (e.g. {"code":"spawn_failed"}) was wrongly injected with
      causeClass/trackRecord/nextActions. Fixed by explicit PROVENANCE: a
      ToolResult.BridgePassthrough flag (json:"-") marks every bridge-forwarded
      payload (success + error, main handler + callDirectGABPTool), and
      completeFailureAttribution skips it regardless of keys/error flag; the
      GABS-OWNED call_tool wrapper failures (malformed tool arg, no connection,
      transport error, game-not-found, ambiguous) are attributed BY CLASS
      DIRECTLY (call/state) with NO minted stable code. Tests: an end-to-end
      colliding-code bridge result stays un-attributed, the central gate is
      unit-tested for success+error passthrough vs a GABS-owned coded control,
      and each wrapper failure carries its class without a code. F4: a real
      games_start with a status hook
      reporting stopped exercises the production hook-stopped exitedFailure
      branch and asserts game class + preserved hookEvidence + recorded game
      failure. F2 (attribution provenance) accepted round 15. F9/F5
      (history-credit loss/double-count) fixed round 14 by record-first credits;
      rounds 15-17 additionally bind each deferred credit to its own immutable
      event identity — self-contained pending clean-stop/delivery events
      reconciled by every deleter and by games_status, independent of the
      claim's current Operation/Attachment, with a lifetime-coupled dedup that
      cannot outlive its pending record, a non-dropping append at saturation, and
      marker GC gated behind (and scoped to) the durable runtime transition so an
      unrelated reconcile can never drop a not-yet-durable event's marker — see
      the F5 Deviations entry. F5 signed off by the adjudicator round 17
      conditional on the green build/vet/full/race gates (protocol §18))
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
- [x] M2.11 bridge.json diagnostic fields; env-only live contract
      preserved — spec: 03; tests: T-DELIV
      (round-12 F10: the spawn-boundary diagnostics stamp is fenced to the
      launch's endpoint — StampBridgeDiagnostics requires the expected
      port+token and returns ErrBridgeEndpointRotated for a successor's token; a
      stamp WRITE failure surfaces as a structured start warning. That
      generation check was a non-atomic read→compare→write that an interleaving
      rotation could defeat (A reads token A, B publishes token B, A rewrites
      restoring token A). Round-13 fixed it with a per-(configDir,gameID)
      IN-PROCESS mutex and justified it by claiming GateStart's transition lock
      serialized cross-process starts — which round-14 F1 found FALSE: GateStart
      acquires and releases the transition lock internally and does NOT retain it
      across PrepareBridgeEndpointForStart or the async StampBridgeDiagnostics,
      so an in-process mutex cannot fence a superseded GABS process against a
      successor GABS process (design/06: phase/generation transitions are
      cross-process). Round-14 F1 replaces the mutex with a DEDICATED
      CROSS-PROCESS advisory lock (bridge.lock, flock/LockFileEx via
      withBridgeLock) held across the WHOLE read-compare-write in BOTH
      StampBridgeDiagnostics and PrepareBridgeEndpointForStart — dedicated so it
      never nests with a transition lock, cross-process so it serializes separate
      GABS processes. Coverage: the 400-iteration in-process concurrency
      invariant (final token is always the successor's) and after-read barrier
      are kept, PLUS a new SUBPROCESS barrier test (a re-exec'd process holds the
      lock paused inside the stamp while this process's rotation must block until
      it releases) — verified to FAIL without the cross-process lock. Round 15:
      the reviewer accepted the dedicated cross-process bridge.lock (complete
      read/compare/write in both prepare and stamp, Unix + Windows primitives,
      subprocess barrier test) → [x].)
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
- [x] M2.16 (added) Test-server isolation + background-task join — spec: 30
      §race gate; tests: T-TRACK/T-DELIV race lanes
      (round 12 F4 discovered work. NewServerForTesting defaulted configDir to
      "" → the real ~/.gabs, so a test that skipped SetConfigDir read/wrote the
      user's live GABS state; the constructor now REQUIRES a testing.TB and sets
      configDir to a caller-owned tb.TempDir(), so it can never resolve to
      ~/.gabs. The config-package write tests (concurrency/env-isolation) and
      the bridge-cleanup test were moved off ~/.gabs. Server.Shutdown() cancels
      (shutdownCh) and JOINS every detached task; the constructor registers it
      via tb.Cleanup, so EVERY test server's background tasks join before
      teardown. Round 13 corrections: (F3) EVERY background bgWG.Add — async
      mirroring, the nested attention spawn, the lease refresher, and the
      background GABP connect — now routes through admitBackgroundTask, which
      does the shutdown check AND the Add together under s.mu (the same lock
      Shutdown holds to close admission), so no positive Add can race
      bgWG.Wait; (F6) the earlier constructor-owned isolated dir + Shutdown-time
      RemoveAll leaked at the ~90 call sites that never call Shutdown, so it is
      REPLACED by the tb.TempDir() the framework removes universally — Shutdown
      owns only the join, never a directory, and no gabs-test-isolated dir is
      ever created. A subtest observes the framework actually removing the
      constructor's dir after the NewServerForTesting→SetConfigDir→Shutdown
      sequence. Race tests cover concurrent admission-vs-shutdown and concurrent
      attachment-vs-shutdown. Round 14: the reviewer verified the testing.TB
      constructor, framework-owned temp dirs, tb.Cleanup shutdown registration,
      and synchronized task admission close both the leak and the
      WaitGroup.Add/Wait race → [x])
- [x] M2.12 Remaining conformance cells (env-dropping, filtering,
      absent-env reintroduction, detached) — spec: 03; tests: T-DELIV
      (REOPENED round-18: the first pass proved delivery observation +
      verdict per wrapper shape but (1) bypassed the Stage-4 lifecycle so
      the T-DELIV adoption-on-launcher-exit and detached status-hook
      liveness cases were absent, (2) declared the Windows cells
      observation-only over a real production argv gap (the cmd.exe /c
      prefix is digested), and (3) was flipped to [x] on compiled-not-run
      Windows cells + no executable macOS .app fixture. Round-18 correction
      landed all three: (1) production-path lifecycle cells — an env-scrubbing
      launcher that exits with its workload observable by name yields an ADOPTED
      result through the real games.start manager (teeth-checked: no survivor →
      exited_during_start, not adopted), and a detached double-fork chain whose
      pinned status hook keeps EvaluateLiveness running after the direct child is
      reaped; (2) ArgvPayloadForDigest so the documented cmd.exe /c wrapper argv
      VERIFIES (the Windows cells now compute the production verdict, not
      observation-only); (3) an executable Probe.app whose inner binary runs via
      the production resolver, proving argv/env arrival. Unix + macOS cells run
      locally; the Windows cells' green is the windows-latest lane's to confirm.
      STAYS [~] until that lane actually runs, per the adjudicator.)
      (conformance_delivery_test.go: each cell spawns the probe through a
      real sh wrapper and evaluates what actually arrived against the
      spawn-pinned digests with the production EvaluateContextDelivery, so
      the verdict can claim no more than the chain delivered — filtering
      boundary forwarding all GABS_FORWARD_ENV names → verified;
      managed-only filtering (drops the context key) → partial with the
      context env channel not verified; env-dropping `env -i` scrub →
      partial (argv+cwd still verify, env unknown); a boundary reintroducing
      the GABS_ABSENT_ENV name HOST_OVERRIDE → partial (env channel
      mismatch); detaching double-fork wrapper → verified. Digests use the
      real ComputeContextDigests over test-controlled values (the
      managed/context split cannot change any overall verdict); the probe
      also records HOST_OVERRIDE, and the scrub/filter wrappers carry
      PROBE_OUTPUT_FILE across as the test's own reporting channel,
      independent of the GABS channels under test. Windows cmd.exe variants
      (conformance_windows_test.go: for-loop unset of GABS_FORWARD_ENV,
      `set CONTENT_SET=`, `set HOST_OVERRIDE=`, `start /b`) NOW COMPUTE the
      production verdict (round-18 ArgvPayloadForDigest makes the cmd.exe /c
      wrapper argv verify) and are written-but-unexecuted until the M2.3
      windows-latest lane runs them — cmd.exe has no `env -i`, so each shape
      is reproduced with targeted `set`; Windows filtering-full is the
      existing forwarding-wrapper cell, and the verdict logic itself is
      unit-tested cross-platform in context_delivery_test.go).
      CLOSED: the windows-latest lane is GREEN (PR #67, run 30120986500) — the
      cmd.exe conformance cells executed and their production verdicts passed.)
- [x] M2.13 repair --forget-runtime + no-arg games_status union of
      runtime-only claims — spec: 07, 10; tests: T-RT
      (process.ListRuntimeClaimIDs enumerates persisted claims; no-arg
      games_status unions configured entries with runtime-only claims
      (configured:false + persisted phase, stop/kill next actions), and a
      removed-but-claimed game stays addressable by ID for single-ID status AND
      for stop/kill — the claim carries the pinned lifecycle, so a synthetic
      GameConfig drives the same claim-based design/06 pipeline; status resolves
      from the claim + liveness, never config. CLI `gabs games repair <id>
      --forget-runtime` prints the claim's evidence and removes it after
      confirmation (or --yes), operating on the CLAIM not config so a game
      already edited out — and a corrupt/unreadable claim — is still forgettable.
      CLI-only — no MCP tool forgets state (design/07:100).
      REOPENED round-19, three bounded fixes: (P1 security) the runtime-only /
      forget / status / stop / kill paths passed a RAW identifier through
      filepath.Join, so `../victim` and a symlinked game dir escaped the config
      base (reproduced: forget deleted a sibling). Centralized in
      config.ValidateGameID (one-component grammar) + ConfigPaths.SafeGameDir
      (symlink-contain if the dir exists), wired into LoadRuntimeState /
      RemoveRuntimeState / RuntimeClaimExists / AcquireTransitionLock and the
      create path; CLI + MCP traversal + symlink regressions. (P1 forget) the CLI
      removed whatever runtime.json existed AFTER an unbounded prompt and skipped
      F5 reconciliation — a successor B could be deleted unseen, and a healthy
      claim's pending credits were silently discarded. Now a process-layer
      ForceForgetRuntimeClaim binds a raw-bytes digest captured with the shown
      evidence (ErrForgetClaimChanged on mismatch), reconciles pending credits
      (creditPendingThenRemoveLocked) for a readable claim, and only discards on
      an explicit second confirmation when reconciliation is impossible (corrupt
      / history-write failure). (P2) runtime-only rows now probe through the SAME
      concurrent status pool (3 slow-hook claims ~2s, not ~6s), and a corrupt
      claim renders unknown + the repair command, not silent-unknown + stop/kill.
      ROUND-19 ADJUDICATION: the one-component grammar wrongly rejected
      design-legal slash IDs (e.g. "factory/old" loaded + listed but games_start
      failed). FIX: ValidateGameID no longer redefines the public ID grammar as a
      filesystem grammar — it rejects only empty/NUL/absolute; SafeGameDir
      confines the runtime dir structurally (LEXICAL filepath.Join containment
      rejects `..`, plus a SYMLINK check on the deepest existing ancestor so a
      symlinked intermediate cannot redirect even a not-yet-created leaf — the
      create path via EnsureGameDir is not exempt). A nested `/` ID maps to a
      nested runtime dir (no migration; existing dirs stay natural, unlike an
      encoding that would orphan slash-ID history); ListRuntimeClaimIDs walks
      recursively and decodes the storage key back to the exact ID. Slash-ID
      lifecycle regression (claim/exists/load/enumerate/remove, MCP
      discovery/status/stop, CLI forget) plus retained ../victim / absolute /
      symlink rejections. M2.14/M2.15 proceed.)
- [x] M2.14 Remove M1 lifecycle feature gate — spec: 21; tests: T-VAL
      update
      (the M2 lifecycle runtime executes, so the gate that rejected `lifecycle`
      config fields as "not yet supported" is gone: removed
      ValidationOptions.AllowLifecycle and the rejection in validateLifecycleSlot,
      so lifecycle validates + executes on the default load path. The URL-mode
      observation/control check (stopProcessName OR a status + stop/kill hook,
      T-VAL) was gated on AllowLifecycle; it is now unconditional — the hook
      alternative is always available. games.go's stopProcessName message dropped
      its stale "once lifecycle hooks are supported" clause. T-VAL updated:
      TestLifecycleGateRemoved + TestLoadAcceptsLifecycle assert lifecycle now
      validates/loads (reproduce-first: both failed on the pre-removal gate); the
      AllowLifecycle test literals became plain ValidationOptions{}. Full config
      and mcp suites green — no existing config regressed from the stricter
      load-path URL-mode check.)
- [x] M2.15 EnsureClientRunning demoted to bounded best-effort warning —
      spec: 05 Stage 2, 20; tests: T-START (Steam advisory)
      (Controller.Start no longer calls EnsureClientRunning — it could turn
      assistance failure into spawn_failed or run twice. The store-launcher
      advisory is now Stage-2 work in the start manager (after GateStart): a
      Steam mode scans once via steam.ClientRunning() and, if the client is not
      observable, records exactly one advisory warning; SteamManaged additionally
      runs steam.EnsureClientRunningWithin(budget) best-effort while SteamAppId
      does not. CRITICAL timing (reviewer): the assistance is charged against the
      accepted operation's PERSISTED deadline (time.Until(Operation.Deadline)
      minus a 2s spawn headroom, skipped if <= 0), never a fresh 30s+20s wait, so
      it cannot let the operation expire before cmd.Start — reopening the
      supersession/fencing hole. The warning records the observed preflight
      condition and stays even if assistance then succeeds; failure/timeout is
      advisory, never a start failure. Tests: EnsureClientRunningWithin returns
      within budget on timeout; Controller.Start invokes no assistance;
      SteamManaged-absent → advisory once + assistance attempted;
      SteamManaged-present → neither; SteamAppId-absent → advisory but no managed
      ensure.
      ROUND-20 correction: the fixed 2s headroom did not make the persisted
      deadline authoritative across the REST of the start — endpoint prep can
      block up to bridgeLockTimeout (5s) on bridge.lock, and Stage 4 received the
      ORIGINAL full startBudget rather than the remaining time. Now the absolute
      Operation.Deadline governs Stage 2 + Stage 4 with no overlapping budgets:
      (1) assistance reserves config.BridgeLockTimeout()+headroom out of the
      deadline (skipped when nothing is left), so it cannot eat the claim before
      pre-spawn work; (2) Stage 4 is charged time.Until(Operation.Deadline), and
      below a minStageFourBudget floor the start does not spawn at all (a
      supersedable operation must not create an OS process a concurrent start
      could be replacing); (3) the pre-spawn FencedTransition checks the deadline
      and marks spawning ATOMICALLY under the transition lock — once past the
      deadline it returns ErrFencingViolation, which maps to the stable
      supersession outcome (operation_in_progress/blocked), never spawn_failed or
      a game fault; assistance failure remains only a warning. Production-path
      regression: contended endpoint prep (held bridge.lock) under a short
      deadline proves the first start never spawns after becoming supersedable
      (spawn-marker absent) while a concurrent second is refused
      operation_in_progress — it cannot replace the first while its executor
      legitimately proceeds. Full mcp suite green (the general pre-spawn deadline
      check introduced no fallout).)

## Milestone 3 — CLI + docs + skill

- [x] M3.1 CLI start/status/stop/kill on the shared lifecycle manager +
      started_attachment_deferred — spec: 11; tests: T-CLI
      (architecture B. A new internal/lifecycle package holds the typed Manager
      that owns the Stage 1–4 start pipeline + stop/kill/status over the
      persisted claim; both frontends drive it. The MCP server is a thin
      adapter (Server.startGame -> s.lifecycle().Start then its own Stage 5
      attach; lifecycleActionResult -> s.lifecycle().Stop); the CLI adds
      `gabs games start/status/stop/kill` in cmd/gabs/games_lifecycle.go as
      thin adapters that render text (never JSON-RPC, never ToolResult). The
      server's live-bridge evidence and in-process registry enter the pipeline
      only through nil-safe BridgeBound/CheckInProcessActive policy callbacks;
      a one-shot CLI passes nil, so the persisted attachment lease + owner
      fingerprint is the authoritative cross-process liveness (design/04) —
      exactly the correct verdict. CLI start runs Stages 1–4 then exits with
      started_attachment_deferred (claim phase active, endpoint persisted, no
      attachment); status/stop/kill work from the snapshot after that process
      exits. Repeated `--input NAME=VALUE` parse per the declared type (bool/
      integer/string), and repeating a name is an error; `--profile` selects
      the profile. T-CLI: cmd/gabs/games_lifecycle_test.go covers flag parsing,
      typed-input coercion + duplicate-name error, and the full cross-process
      start->status->stop/kill cycle asserting claim state (active + endpoint +
      no attachment + workloadStarts credited) and no process leak; repair
      --forget-runtime is covered by forget_runtime_test.go. The "later server
      games_connect attaches from the CLI-created claim" cell is covered
      transitively: the CLI claim is asserted attachable (endpoint present,
      phase active, attachment nil), and the server's attach-from-persisted-
      claim path is exercised by the mcp reconnect/session-roaming suite, which
      now runs through the same lifecycle.Start (the nil-callback delta does
      not change the persisted endpoint). Gate: build, vet, go test ./..., and
      -race on config/process/mcp all green; mcp suite byte-identical (oracle)
      across the extraction.
      ROUND-2 CORRECTION (reviewer): reopened to add the direct tests the first
      pass leaned on the oracle for. internal/lifecycle/lifecycle_test.go now
      unit-tests the Manager directly (budget/lease, spec builders, error types,
      Status over a live-PID claim + no-claim, LoadStopClaim, SupersededStart
      Refusal by phase, ComputeHistoryContext + ContextProven with seeded
      history, NewInstanceID). cmd/gabs/subprocess_cli_test.go replaces the
      in-process T-CLI cell with a REAL cross-OS-process test: it builds the
      gabs binary and runs `games start` / `status` / `stop` as three separate
      processes, so a claim written by one process is read and cleared by
      independent processes — the portable test-binary-as-game helper lets it
      run on Windows too.
      ROUND-5 CORRECTION (reviewer, two P2 runtime-state I/O defects in the
      shared status path): (a) status.go discarded the POST-observation reload
      error — a claim/runtime-dir that became unreadable (I/O/permission)
      returned (nil claim, nil error), which a caller renders as a successful
      stop; it now surfaces the error (LoadRuntimeState returns (nil,nil) only on
      ErrNotExist, so "removed" and "unreadable" are now distinguished). (b)
      status_machine.go returned the EMPTY supersession sentinel when a fenced
      removal failed for a NON-fencing reason (write/lock/permission) while the
      claim was retained — the CLI rendered an empty status and the MCP path got
      an invalid one; it now returns "unknown" (real uncertainty, claim kept).
      Direct white-box tests cover both (a reload seam injects the post-
      observation read fault; a read-only claim dir forces the non-fencing
      removal failure), each verified to FAIL against the pre-fix code. MCP
      consumer confirmed to route "unknown" through the same default branch that
      took "" (strictly better); mcp oracle byte-identical.)
- [x] M3.2 Profile-aware doctor + --show-last-good + track-record
      display + conflation lint — spec: 11, 08; tests: T-CLI
      (cmd/gabs/games_doctor.go. `gabs games doctor <id>` is now profile-aware
      and, per the advisor's no-early-return structure, reports every diagnostic
      it can before exiting once: loading the snapshot validates profile/input/
      hook references (a ValidationError names the JSON path); it resolves every
      launchable context (default + each named profile), prints resolved hook
      commands + working dirs, runs the docker/podman stopped-vs-cannot-
      determine conflation lint (advisory, basename-matched, design/01/20),
      warns on broadly readable config/runtime files (unix-only — NTFS ACLs
      govern on Windows), and prints the full per-profile track record
      unconditionally (readable from history.json regardless of config state).
      `doctor --show-last-good` prints the last-known-good context per profile
      and flags when the current context was edited so a human can compare or
      restore by hand (design/08). It stays CLI-local presentation over the
      already-shared LoadHistory/launch.Resolve — no shared "diagnostics
      manager". Tests: cmd/gabs/games_doctor_test.go (conflation unit + profile-
      aware output + invalid-config-still-prints-track-record + broadly-readable
      warning + track-record/last-good after a verified start). Gate: build,
      vet, go test ./..., -race cmd/gabs green; M3.1's mcp oracle unaffected
      (doctor touches no mcp/lifecycle/process code).
      ROUND-4 CORRECTION (reviewer, P2): extended attributes are path-specific,
      so doctorMacOSTarget's quarantine check on the resolved inner .app binary
      alone missed a bundle carrying com.apple.quarantine on its ROOT while
      Contents/MacOS/<exe> had none (reproduced). Now checks BOTH the configured
      target and the resolved inner executable (design/20:300). Regression
      TestDoctorMacOSQuarantineOnBundleRootOnly creates Probe.app/Contents/MacOS/
      Probe, quarantines only Probe.app, guards that the inner binary is clean,
      and requires the warning — verified to FAIL against the old single-path
      check and PASS with the fix.)
- [x] M3.3 User docs (README, CONFIGURATION, INTEGRATION,
      TROUBLESHOOTING, example-config.json) — spec: 31; gates: genericity
      scan
      (README: the user-level model + one discovery->start example (games_list
      -> games_start -> games_connect -> games_tool_names/detail/call_tool),
      both frontends, hot-reload. docs/CONFIGURATION.md: full launch-profile
      schema (env/unsetEnv/defaultProfile/profiles/typed launchInputs/lifecycle
      hooks), the exact resolver order (args append, env override, workingDir
      replace), the exit-code contract with the canonical status-hook WRAPPER
      pattern (reachability -> exit 2 unknown; running/absent -> 0/1) and raw
      `docker inspect` as the misconfiguration, idempotent hooks,
      verifyTimeoutSeconds save-on-exit guidance, the Windows cmd.exe /c
      script-hook rule, the Steam re-exec caveat + workarounds, the
      legacy->profile recipe, the ID-consolidation checklist, and the
      old-binary warning. docs/INTEGRATION.md: the launcher/wrapper contract
      (forward argv, preserve/map env, GABS_FORWARD_ENV container loop, never
      reintroduce GABS_ABSENT_ENV names), the env-only live-bridge rule
      (bridge.json diagnostic-only, never a discovery fallback), and the
      optional session-welcome `observed` field spec. docs/TROUBLESHOOTING.md:
      the design/05 bad-case map table verbatim + the not-a-failure outcomes +
      the conflation mistake. example-config.json: one neutral profiled game
      with a typed input and the wrapper-based status hook (schema-validated).
      Genericity gate: scripts/genericity-scan.sh (a genericity CI job in
      test.yml + a make target) rejects real game/studio trademarks on the
      public surface — it caught+fixed a stray `terraria` in DEPLOYMENT.md and
      now runs clean. All docs use only neutral/fictional names.
      ROUND-2 CORRECTION (reviewer): the INTEGRATION.md container-wrapper example
      declared #!/bin/sh but used the bash-only ${GABS_FORWARD_ENV//,/ }, which
      fails under POSIX sh (dash) with "Bad substitution" (exit 2) — exactly the
      shell a minimal container base provides. Rewritten to POSIX word-splitting
      (IFS=,; for v in $GABS_FORWARD_ENV; ...; unset IFS), verified in dash.)
- [x] M3.4 skills/gabs-mcp update incl. the agent edit contract — spec:
      31; gates: skill validation
      (skills/gabs-mcp/SKILL.md gains three concise sections: "The Edit
      Contract" (verbatim — edit config only when causeClass is config, when
      setting up a never-proven game, or when the user asked; environment/game/
      state failures on a proven context are never config problems; treat
      "started N×" as authoritative), "Profiles and Launch Inputs" (check
      games_show first; prefer a profile over a duplicate ID only when target+
      launchMode match; supply an input only when the user asked and never as a
      substitute for a GABP tool; hot-reload verified via games_show; stop/kill
      take no profile), and "Outcomes That Are Not Failures" (started_bridge_
      pending / unobserved / started_attachment_deferred / operation_in_progress
      / unknown — follow nextActions, never relaunch, switch profiles, or start
      a duplicate). Skill validation: frontmatter intact, genericity scan clean
      over skills/gabs-mcp, tool names verified against the code.)
- [x] M3.5 Acceptance scenario end-to-end — tests: T-ACC
      (cmd/gabs/acceptance_test.go drives the neutral end-to-end scenario
      through the CLI with real processes: (1) two profiles of ONE game launched
      sequentially isolate argv, env, AND cwd — asserted against what a recorder
      target actually received, not config echoing — and each launch reports its
      activeProfile in the claim; (2) an early-crash target (exit 3 + stderr)
      surfaces exited_during_start with the exit code and the captured output
      tail and leaves no claim; (3) renaming a profile on disk and launching the
      new name works without restarting GABS or the client (config is re-read
      from its source of truth), and the old name no longer resolves. The
      remaining T-ACC cells — wrapper-exit + status hook / adoption, stop-via-
      hook + verification, and slow-shutdown raised verifyTimeoutSeconds — are
      covered by the existing suites (internal/mcp/conformance_adoption_test.go,
      internal/process/stop_gate_test.go, internal/mcp/stop_lifecycle_mcp_test.go)
      and hot-reload by TestDiscoveryUsesPerCallConfig/TestActiveConfigRevision
      Surfaced. Gate: build, vet, -race cmd/gabs green. Skips on Windows — the
      cmd acceptance tests run on the unix CI lanes, matching the M2 pattern.)
- [x] M3.6 Final regression gate on all three OSes — tests: T-GATE
      (TRI-OS GREEN. macOS local gate: go build, go vet, go test ./..., go test
      -race ./..., make build, both genericity checks (in-suite
      TestPublicSurfacesStayGeneric + scripts/genericity-scan.sh, aligned to
      reject "mod"/"modification"; the TROUBLESHOOTING bad-case table's "mod
      failure" was genericized to "add-on failure"), skill validation, clean
      tree. Linux (Tests/test) + Windows (Tests/test-windows) GREEN on PR #67
      (origin 5c3186d), alongside CodeQL (both analyses + aggregate), the new
      genericity CI job, and GitGuardian. One CI-only fix: the doctor conflation
      test asserted the raw "status hook: docker" line, which the Linux runner
      (docker installed) expands to an absolute path — reasserted on the stable
      basename-matched conflation advisory. The T-GATE regression cells are
      covered by the existing suites (recovery verdicts running->active/stopped->
      removed/unknown->occupied, delivery unknown vs partial, interrupted-
      executor normalization, GABS_PROFILE-divergent status hooks, attached-
      bridge-vs-CLI-stop owner-fingerprint running-evidence, exhaustive Stage-1
      branch coverage) plus the M3.5 acceptance cells. This is the Milestone-3/
      final-design hand-off boundary. PR #67 remains a DRAFT — not merged.
      ROUND-2 CORRECTION (reviewer): (1) integer arithmetic extremes were
      untested — added internal/mcp/integer_boundary_test.go exercising the
      ParseInt paths (timeout/limit/cursor) at overflow + boundary through BOTH
      raw stdio (HandleMessage) and a real HTTP round-trip (httptest), asserting
      overflow is rejected as "must be an integer", never truncated or a panic.
      (2) The windows-latest lane now runs ./internal/lifecycle and ./cmd/gabs
      in addition to ./internal/process, so the new lifecycle/CLI code has real
      Windows test execution (incl. the cross-process subprocess T-CLI), not
      just build+vet. (3) The final marker commit is PUSHED so PR #67 reflects
      the completed state. Full re-gate: go build, go vet, go test ./...
      -count=1, go test -race ./... -count=1, make build, genericity, skill
      validation, clean tree — all green.
      ROUND-3 CORRECTION (reviewer, six groups): (F1/P1) checked pagination
      (cursor+limit overflow panicked at entries[cursor:end]) and clamped
      duration conversions (positive int -> negative timeout), with stdio+HTTP+
      framed+overflow tests. (F2/P1) moved the ENTIRE claim-status state machine
      into lifecycle.Manager (internal/lifecycle/status_machine.go) so both
      frontends share one implementation — recovery / passive-promotion+credit /
      reconciliation / fenced removal; CLI status now matches MCP; mcp oracle
      byte-identical. (F3/P1) added the real cross-session T-CLI + hot-reload
      composition test (CLI subprocess starts a GABP helper -> server-session
      games_connect -> rename profile on disk -> renamed-profile launch through
      the same live server). (F4/P2) CLI endpoint code -> endpoint_unavailable
      (not the invented bridge_endpoint_in_use) and one pinned snapshot supplies
      both stop mode AND revision for legacy normalization. (F5/P2) doctor
      derives target findings from the Stage-1 resolver (no false os.Stat fail
      on PATH/relative targets) + the macOS quarantine/translocation checks.
      (F6/P2) fixed CONFIGURATION recovery wording (interrupted stop/kill is NOT
      replayed) and unified the design/05 <-> TROUBLESHOOTING bad-case table with
      an equality test. Re-gate all green; mcp oracle byte-identical.
      ROUND-4 CORRECTION (reviewer, sole remaining P2): the macOS quarantine
      check now inspects the configured .app bundle root, not only its resolved
      inner executable (see M3.2) — the last design-contract gap. Same re-gate:
      build, vet, go test ./..., -race cmd/gabs, macOS bundle-root regression;
      tri-OS PR CI re-run green.
      ROUND-5 CORRECTION (reviewer): two P2 runtime-state I/O defects in the
      shared status path — discarded post-observation reload error + empty status
      on a non-fencing removal failure (see M3.1). Re-gate: build, vet, go test
      ./..., -race ./internal/lifecycle + ./cmd/gabs, mcp oracle byte-identical,
      genericity; tri-OS PR CI re-run green.)

## Deviations

- 2026-07-23, M2.12, round-18 P1b, design/20:262-264 + design/30:243 (argv
  payload for the documented cmd.exe /c shape): design/20 defines the argv digest
  as "elements after argv[0]" — literally one excluded element. That cannot
  satisfy T-DELIV's requirement (design/30) that the Windows forwarding wrapper,
  which the design mandates be configured EXPLICITLY as `cmd.exe /c script.cmd
  ...` (design/01:119-122, GABS never implicitly wraps), verify its argv channel
  fully: the workload the script re-launches via %* sees only the tokens after the
  script, so `/c` and the script path are three-token launch prefix, not one.
  REFINEMENT: argv[0]-exclusion becomes launch-prefix-exclusion for that one
  documented shape — a pure ArgvPayloadForDigest(pathOrId, args) recognizes
  basename cmd/cmd.exe + `/c` and digests args[2:]; every other launch
  (DirectPath, unix wrapper-as-target) returns args unchanged, so no existing
  digest changes. Unit-tested cross-GOOS (TestArgvPayloadForDigest); the actual
  %* forwarding is the only CI-gated part. cmd.exe re-quotes %*, so exotic values
  may mis-split — already a documented caveat (design/03:145-146), not handled.
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
  an afterCommit hook (later renamed to the beforeCommit/…WithCredit form in
  round 14, below) that ran under the same lock but only AFTER SaveRuntimeState
  succeeded; every
  history increment — workloadStarts (all 4 promotion paths), bridgeConnects,
  deliveriesVerified — moved from the mutate callback into afterCommit, and
  cleanStops now records AFTER RemoveRuntimeState commits. So a runtime save/
  removal failure never advances history ahead of the claim, and the flagged
  retry-double-count is gone. ROUND 14 (F5 reopened): the reviewer rejected
  adjudicating the residual undercount window as acceptable — the round-13
  afterCommit ordering ran the credit AFTER the runtime commit, so a history-
  write failure or crash once the trigger was consumed permanently LOST the
  event (a lost workloadStart can flip a later missing-target from environment
  to speculative config, exactly what the track record must prevent). Fixed by
  the required logical-event-ID reconciliation: the credit is now recorded FIRST
  under the transition lock, idempotent by event ID (start:launchID via
  CreditedLaunchIDs; connect/delivery:connectionID and stop:operationID via a
  new CreditedEvents ring), BEFORE the runtime write, which is gated on the
  credit committing (afterCommit→beforeCommit: TransitionRuntimeStateWithCredit
  / FencedTransitionWithCredit return the credit's error and abort; clean-stop
  credits before removal). A history-write failure aborts the transition so a
  retry re-credits exactly once (no loss); a runtime-write failure or crash
  between the two writes replays to exactly-one via idempotency (no double). A
  corrupt/unreadable history still degrades inside LoadHistory (repair-and-
  credit), so the degradation rule never blocks the lifecycle. Proven by
  history_credit_replay_test.go: BOTH write directions (history-write and
  runtime-write, the latter = crash-between-writes) for ALL FOUR counters.
  ROUND 15 (F5 reopened again): record-first makes a retry SAFE but two
  production paths never RETRY, because the retry trigger is destroyed —
  (1) interrupted recovery deleted a definitively-stopped claim via a nil
  beforeRemove, losing the clean-stop credit; (2) TakeObservedContext consumed
  the delivery report before the verdict+credit converged, so a failed delivery
  history write lost the event with no source to retry. Fixed by the same shape
  as the accepted counters — persist the durable DOMAIN FACT, reconcile the
  credit from it, idempotent by event ID — scoped to exactly these two paths
  (workloadStart/bridgeConnect record-first is untouched): (1) a verified stop
  whose credit/removal fails marks the claim CleanStopVerified, and EVERY claim
  deleter that can encounter it replays the clean-stop credit (stop:operationID,
  idempotent) before removal — interrupted recovery, the direct stop retry, and
  a superseding games_start's stale-claim clear (via a shared
  creditCleanStopIfMarked); other deleters are fenced out (they require a
  claim with no Operation, or an empty status, which a marked stopping claim
  never has). An interrupted stop is unmarked and NOT credited. (2) delivery
  FLIPS to
  verdict-first: the derived (non-env) verdict is persisted, then
  deliveriesVerified++ is reconciled from it (connect-time best-effort AND by
  any later games_status, idempotent by connectionID, read-only when already
  credited). Both fixed with PRODUCTION-PATH tests (real ExecuteStopAction ->
  RecoverInterruptedClaim; real games_connect -> failed delivery write ->
  games_status), asserting exactly-once. ROUND 16 (F5 reopened again): the
  round-15 direction was right but both durable facts still read their event
  IDENTITY from the CURRENT claim state, which ordinary lifecycle replaces — the
  reviewer disproved the self-audit through production paths. (1) CleanStopVerified
  was a bool; reconciliation took the operationID from cur.Operation, which stop
  admission replaces and non-terminated completion clears, so the original
  verified stop was lost; the marker was also published in a separate post-lock
  transition with a discarded error (a race gap). (2) the delivery verdict stored
  no connectionID, so a successor attachment could be credited from a predecessor's
  verdict, and a disconnect stranded the pending credit. FIX: each pending fact is
  a SELF-CONTAINED PendingCredit{id, profile, contextHash, at} — operationID for a
  clean stop, connectionID for a verified delivery — in bounded per-claim lists
  (PendingCleanStops/PendingDeliveries). Reconciliation is a pure function of the
  entry (never re-reading Operation/Attachment); EVERY deleter reconciles BOTH
  lists before removal and aborts on any credit failure (no event lost with the
  claim), and games_status additionally reconciles both lists on the live claim
  independent of Operation/Attachment (the disconnect-before-status case). The
  removal surface is BOUNDED and enumerated: all six RemoveRuntimeState callers
  are {process deleter (removeRuntimeStateGuarded, removeEvaluatedClaim), status
  funnel (RemoveRuntimeStateIfCurrent) — all three reconcile-before-remove |
  legacy-schema-only (restore-after-failed-connect, stale-legacy-status) —
  pending events are current-schema | legacy controller cleanup
  (cleanupRuntimeStateInternal) — read-only-guarded to never remove a
  pending-bearing claim}. The pending event is published under the SAME lock that
  observes the failure — no post-release gap, the save error is surfaced. The
  bounded cap is a LOUD error, never a silent evict. Raw env values never persist
  — only the derived verdict + identity.
  Production-path, teeth-checked tests cover the reviewer's clean-stop set
  (operation replacement, both-counted, concurrent deleter) and delivery set
  (detach, successor-never-credited-from-predecessor, partial-preserves-pending,
  restart, reconcile-at-removal). ROUND 17 (F5 reopened again): the round-16
  self-contained identity was right, but its dedup LIFETIME was wrong in two
  ways. (1) the pending credit was deduped through the shared record-first LRU
  (CreditedEvents, cap 32), while a durable pending record can outnumber it (the
  list cap was 256) — so an evicted marker let a still-replayable pending event
  double-count (reproduced: 33 pending -> 66 credited). FIX: pending events dedup
  through a SEPARATE, lifetime-coupled marker set (CreditedPendingEvents) that is
  NOT LRU-evicted; it is GC'd against the claim's CURRENT pending ids in the SAME
  history write that credits them (retainPendingCreditMarkers), so a marker stays
  durable exactly as long as the pending record it guards and is forgotten only
  once that record is durably gone — the dedup identity can never outlive, nor be
  outlived by, its pending record. (2) appending a verified event at the bounded
  cap DROPPED it — but the welcome report was already consumed (TakeObservedContext)
  and the clean stop already executed, so a drop is permanent loss, not a loud
  refusal. FIX: appendPendingCredit NEVER drops (dedup by id, no cap); unbounded
  growth stays unreachable in normal operation because the reconcile after every
  append drains the list; only a sustained history-SPECIFIC write outage could
  grow it, and non-dropping is deliberately correct there (a bounded replayable
  backlog beats losing an event that already happened). Reconcile now credits +
  GCs + prunes in one history write, then
  one runtime save; a save failure leaves both the records and their markers, so a
  replay re-credits nothing. Permanent regressions added: the 33/40-event
  runtime-save-failure replay (delivery AND clean-stop, asserting exactly-once
  beyond the old LRU cap), the full-list one-shot saturation tests (delivery
  and clean-stop, asserting the next verified event is preserved not dropped),
  and a DELETER-path replay (a stop completion whose credit commits but whose
  RemoveRuntimeState then fails must re-credit nothing on retry — the same
  lifetime through removeRuntimeStateForStopCompletion, not the live reconcile).
  All teeth-checked: a simulated 32-cap eviction reproduces the reviewer's 66/33
  double-credit and 82/41 on the deleter path. Dead ApplyDeliveryVerifiedLocked
  removed (reconcile inlines the credit). ROUND 17 (final F5 finding): the
  lifetime-coupled dedup was right, but the GC ran in the SAME history write as
  the credit — a global "retain-live" sweep against the durable claim's pending
  ids. A stop completion appends its clean-stop event only to the in-memory
  claim, credits it, then removes the claim; if the removal (or a crash) leaves
  the event durable in history but NOT in runtime.json, an unrelated reconcile of
  another pending event rebuilds live from the on-disk claim and drops the stop's
  marker — the retried, still-current completion then credits the same clean stop
  twice (reproduced: intervening reconcile -> got 2, want 1). FIX: GC is moved
  BEHIND the durable runtime transition and SCOPED to the exact records that
  transition drained — never a global retain-live sweep. creditPendingEventsLocked
  only credits; ReconcilePendingCredits prunes+saves runtime and THEN GCs only the
  pruned markers (gcPendingCreditMarkersLocked / dropPendingCreditMarkers); every
  deleter credits, removes the claim, and THEN GCs only that claim's markers
  (creditPendingThenRemoveLocked, replacing the three duplicated
  reconcile-before-remove tails). So a marker for an event that is durable in
  history but not yet pruned from its own runtime state always survives, and a
  marker is forgotten only once its record is durably de-referenced. A stale
  marker left by a crash or a GC-write failure is harmless (event ids are random,
  never colliding). Permanent regression added:
  TestInterveningReconcileDoesNotReplayStopCredit (the reviewer's exact 6-step
  sequence, asserting cleanStops == 1). The reviewer set this as the FINAL F5
  acceptance condition and pre-authorized closure: with this regression passing
  and the build/vet/full/race gates green, M2.10 -> [x] in the same commit as the
  fix (protocol §18 sign-off granted by the adjudicator, conditional on exactly
  these gates, which are green).
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
- 2026-07-23, M2.4, round 13 F6 side-discovery (controller race — FIXED in this
  commit), internal/process/controller.go:715: the M2.16 -race lane flagged an
  unsynchronized read of c.cmd.ProcessState in IsLauncherProcessRunning, which
  raced waitForExit's cmd.Wait() write. It was the one liveness path reading
  ProcessState ahead of the race-free waitDone signal; fixed to rely on
  waitDone + Signal(0) (ErrProcessDone on a reaped process), matching the
  file's existing pattern (lines 466/594/674 and getExitCode). Whether the F6
  constructor change SURFACED a pre-existing race or the prior -race lane was
  merely lucky is unprovable — the earlier lane's "green" was read from a
  mis-indexed shell variable and never actually asserted pass, so this is
  treated as a pre-existing latent race, not a regression. M2.4 kept [x] (the
  fix restores the invariant it claimed); flagged for REVIEWER to decide whether
  M2.4 warrants reopening.
- 2026-07-23, M2.16, round-16 -count=2 insurance side-discovery (test-seam race
  — FIXED in this commit), internal/process/diagnostics.go: a -count=2 mcp -race
  pass flagged a data race on the test-injectable findProcessesByNameFunc — the
  startup GABP-connect monitor's background liveness poll (Controller.IsRunning)
  reads it in the window between a test's deferred SetFindProcessesByNameForTesting
  restore and t.Cleanup(Shutdown) joining that goroutine. VERIFIED pre-existing
  (reproduces on the pre-F5 base at -count=120; unrelated to the round-16 F5
  changes) and test-only (production sets the seam once at init, never writes it;
  the monitor goroutine is joined, not leaked). Fixed by guarding the seam with
  an RWMutex getter/setter and routing every reader through it — race-free
  regardless of a test's cleanup ordering, and robust for any future test rather
  than a per-test defer→Cleanup patch. The background monitor reads only this
  seam; the other Set*ForTesting globals (launch factories, status hook,
  processStartTime) are foreground-only. Confirmed with the reliable -count=120
  repro (was failing, now green). M2.16 kept [x].
