# Liveness

One liveness rule, in precedence order, used by start (duplicate check),
status, GABP waiting, stop/kill verification, and restart recovery:

1. A live GABP connection proves `running` (the bridge is served by the
   game). Because the live contract is env-only, a connection also proves
   the managed `GABP_*` environment reached the process; the handshake's
   optional delivery report is what verifies the remaining channels (argv,
   cwd, context env). Attachment is also a *persisted record* in the
   runtime claim, maintained under the transition lock: `connectionID`,
   the attachment owner's instance and process fingerprint, `observedAt`,
   and a renewable lease deadline refreshed while the socket stays
   connected (piggybacked on the existing heartbeat). A different process
   — the CLI stopping a server-owned launch — treats it as running
   evidence only while the lease is fresh **and** the owner fingerprint
   still matches a live process; a matching disconnect clears it; an
   expired lease downgrades it to history. A CLI can therefore never
   clear a claim while another live GABS process still owns the bridge.
2. Otherwise a configured status hook is authoritative: running / stopped /
   unknown per its exit-code contract.
3. Otherwise, built-in evidence: tracked PID verified by a PID +
   process-start-time fingerprint recorded at launch (PID reuse can never
   match), then the existing `stopProcessName` fallback. An *inspection
   failure* — a fingerprint read error, a process-table enumeration error,
   a permissions denial — is `unknown`, never stopped: only a successful
   lookup that finds no match is stopped-evidence. For URL modes the
   tracked child is the short-lived URL-opener helper, never the workload:
   its liveness is not workload evidence and its exit is expected — only
   GABP, a status hook, or `stopProcessName` can prove a URL-mode
   workload.

## Invariants

- `unknown` never cleans up state and never authorizes a start when a
  claim exists. The result says what was observed (hook exit code / stderr
  tail / timeout) and what to do next.
- Contradictions are reported, not resolved by deletion: hook says stopped
  while GABP is live → running, with a warning about the hook. GABS never
  drops a live bridge to make evidence agree.
- A launcher/wrapper exit while the status hook reports running is normal.
- Before hook-reported `stopped` clears a claim, GABS terminates and reaps
  any still-live launcher child it started.
- The claim file is authoritative between processes. If a GABS server's
  in-memory state disagrees with the claim file (e.g. a CLI stop removed
  it), the server reconciles against fresh liveness evidence instead of
  trusting either side blindly.
