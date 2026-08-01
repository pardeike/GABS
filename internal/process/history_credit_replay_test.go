package process

import (
	"errors"
	"testing"
	"time"
)

// Round-14 F5: every history counter is credited by a RECORD-FIRST step under
// the transition lock — the credit commits BEFORE the runtime write, idempotent
// by event ID — so no event is ever lost or double-counted across either write
// direction:
//
//   - history-write failure  → the transition aborts (the runtime trigger is
//     NOT consumed); a retry re-credits EXACTLY once. This is the loss the
//     earlier round-13 afterCommit ordering could not prevent: it credited
//     AFTER the runtime commit, so a history-write failure once the trigger was
//     consumed dropped the event forever.
//   - runtime-write failure  → the credit is already on disk (record-first ==
//     a crash between the two writes); a retry must NOT double-count.
//
// The suite proves both directions for all four counters: workloadStarts,
// bridgeConnects, deliveriesVerified, cleanStops.

// assertHistoryWriteFailureNoLoss runs the history-write direction: the credit
// aborts the transition and persists nothing, and a retry credits exactly once.
func assertHistoryWriteFailureNoLoss(t *testing.T, doCredit func() error, read func() uint64) {
	t.Helper()
	restore := SetSaveHistoryFailHookForTesting(func() error { return errors.New("history disk full") })
	if err := doCredit(); err == nil {
		t.Fatal("a history-write failure must abort the transition (record-first gates the runtime commit on the credit)")
	}
	restore()
	if got := read(); got != 0 {
		t.Fatalf("a history-write failure must persist no credit, got %d", got)
	}
	if err := doCredit(); err != nil {
		t.Fatalf("retry after a history-write failure must succeed: %v", err)
	}
	if got := read(); got != 1 {
		t.Fatalf("the retry must credit exactly once (no loss), got %d", got)
	}
}

// assertRuntimeWriteFailureNoDouble runs the runtime-write direction on a FRESH
// event: the record-first credit lands on disk, then the runtime write fails
// (modeling a crash between the two writes); a retry must not double-count.
// installFail installs the appropriate runtime-write failure hook (SaveRuntime
// for the transition counters, RemoveRuntime for the clean stop).
func assertRuntimeWriteFailureNoDouble(t *testing.T, installFail func(func() error) func(), doCredit func() error, read func() uint64) {
	t.Helper()
	restore := installFail(func() error { return errors.New("runtime disk full") })
	err := doCredit()
	restore()
	if err == nil {
		t.Fatal("a runtime-write failure must surface")
	}
	if got := read(); got != 1 {
		t.Fatalf("record-first credits BEFORE the runtime write, so the count is already 1, got %d", got)
	}
	if err := doCredit(); err != nil {
		t.Fatalf("retry after a runtime-write failure must succeed: %v", err)
	}
	if got := read(); got != 1 {
		t.Fatalf("the idempotent credit must NOT double-count across a runtime-write failure, got %d", got)
	}
}

const replayCtxHash = "sha256:replay-ctx"

func readCounter(dir, gameID string, pick func(*HistoryEntry) uint64) func() uint64 {
	return func() uint64 {
		h, _ := LoadHistory(gameID, dir)
		e := h.Profiles[""]
		if e == nil {
			return 0
		}
		return pick(e)
	}
}

func seedStartingPinnedClaim(t *testing.T, dir, gameID string) string {
	t.Helper()
	spec := LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game"}
	st := NewRuntimeState(spec, RuntimeStateStatusStarting)
	st.Phase = PhaseStarting
	st.SpawnState = SpawnStateSpawned
	st.HistoryContextHash = replayCtxHash
	st.HistorySuccess = &HistorySuccessIdentity{Snapshot: ContextSnapshot{Target: "/opt/game"}}
	if err := ClaimRuntimeState(gameID, dir, st); err != nil {
		t.Fatal(err)
	}
	return st.LaunchID
}

func TestRecordFirstWorkloadStart(t *testing.T) {
	now := time.Now().UTC()
	mk := func(gameID string) (func() error, func() uint64) {
		dir := t.TempDir()
		launchID := seedStartingPinnedClaim(t, dir, gameID)
		doCredit := func() error {
			_, err := FencedTransitionWithCredit(gameID, dir, launchID, "",
				func(s *RuntimeState) error { s.Phase = PhaseActive; return nil },
				func(s *RuntimeState) error { return ApplyPinnedWorkloadStartLocked(gameID, dir, s, now) })
			return err
		}
		return doCredit, readCounter(dir, gameID, func(e *HistoryEntry) uint64 { return e.WorkloadStarts })
	}
	d1, r1 := mk("wsA")
	assertHistoryWriteFailureNoLoss(t, d1, r1)
	d2, r2 := mk("wsB")
	assertRuntimeWriteFailureNoDouble(t, SetSaveRuntimeStateFailHookForTesting, d2, r2)
}

func TestRecordFirstBridgeConnect(t *testing.T) {
	const connID = "conn-fixed"
	mk := func(gameID string) (func() error, func() uint64) {
		dir := t.TempDir()
		spec := LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game"}
		st := NewRuntimeState(spec, RuntimeStateStatusRunning)
		st.Phase = PhaseActive
		st.SpawnState = SpawnStateSpawned
		st.HistoryContextHash = replayCtxHash
		if err := ClaimRuntimeState(gameID, dir, st); err != nil {
			t.Fatal(err)
		}
		doCredit := func() error {
			_, err := TransitionRuntimeStateWithCredit(gameID, dir, time.Second,
				func(s *RuntimeState) error { return nil },
				func(s *RuntimeState) error {
					return ApplyBridgeConnectLocked(gameID, dir, EffectiveClaimProfile(s), s.HistoryContextHash, connID)
				})
			return err
		}
		return doCredit, readCounter(dir, gameID, func(e *HistoryEntry) uint64 { return e.BridgeConnects })
	}
	d1, r1 := mk("bcA")
	assertHistoryWriteFailureNoLoss(t, d1, r1)
	d2, r2 := mk("bcB")
	assertRuntimeWriteFailureNoDouble(t, SetSaveRuntimeStateFailHookForTesting, d2, r2)
}

// Delivery is no longer credited by a record-first FencedTransitionWithCredit
// (round 16 F5): a verified welcome is a self-contained pending event
// reconciled by its own connectionID. Its write-direction coverage lives in the
// delivery_reconcile tests (identity-bound, attachment-independent).

func TestRecordFirstCleanStop(t *testing.T) {
	const opID = "op-stop"
	mk := func(gameID string) (func() error, func() uint64) {
		dir := t.TempDir()
		spec := LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game"}
		st := NewRuntimeState(spec, RuntimeStateStatusRunning)
		st.Phase = PhaseStopping
		st.SpawnState = SpawnStateSpawned
		st.HistoryContextHash = replayCtxHash
		st.Operation = &RuntimeOperation{OperationID: opID, Action: OperationActionStop, ExecutorPID: 1, ExecutorPIDStartTime: 1}
		if err := ClaimRuntimeState(gameID, dir, st); err != nil {
			t.Fatal(err)
		}
		launchID := st.LaunchID
		req := StopRequest{GameID: gameID, ConfigDir: dir, InstanceID: "inst", HistoryProfile: "", HistoryContextHash: replayCtxHash}
		doCredit := func() error { return removeRuntimeStateForStopCompletion(req, launchID, opID) }
		return doCredit, readCounter(dir, gameID, func(e *HistoryEntry) uint64 { return e.CleanStops })
	}
	d1, r1 := mk("csA")
	assertHistoryWriteFailureNoLoss(t, d1, r1)
	// The clean-stop runtime write is the CLAIM REMOVAL, not a SaveRuntimeState.
	d2, r2 := mk("csB")
	assertRuntimeWriteFailureNoDouble(t, SetRemoveRuntimeStateFailHookForTesting, d2, r2)
}
