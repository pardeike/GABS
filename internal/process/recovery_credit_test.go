package process

import (
	"os"
	"testing"
	"time"
)

// TestRecoveryCreditsStartPromotionNotStop verifies P1-2 Site D: a restart
// recovery that promotes an interrupted START to active credits the Stage 4
// workloadStart from the pinned identity, while a recovered STOP/KILL — whose
// workload started earlier — must not.
func TestRecoveryCreditsStartPromotionNotStop(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	const hash = "sha256:ctx"

	mkClaim := func(gameID, phase, action string) {
		spec := LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game"}
		st := NewRuntimeState(spec, RuntimeStateStatusStarting)
		st.Phase = phase
		st.SpawnState = SpawnStateSpawned
		st.GamePID = os.Getpid() // alive → liveness sees the workload running
		if s, err := ProcessStartTime(os.Getpid()); err == nil {
			st.PIDStartTime = s
		}
		st.HistoryContextHash = hash
		st.HistorySuccess = &HistorySuccessIdentity{Snapshot: ContextSnapshot{Target: "/opt/game"}}
		st.Operation = &RuntimeOperation{
			OperationID:        NewFencingID(),
			Action:             action,
			ExecutorInstanceID: "dead-instance",
			ExecutorPID:        999999999, // nonexistent
			AttemptStartedAt:   now.Add(-time.Hour),
			Deadline:           now.Add(-time.Minute), // expired → recoverable
		}
		if err := ClaimRuntimeState(gameID, dir, st); err != nil {
			t.Fatal(err)
		}
	}

	// A recovered START promotion credits workloadStarts++.
	mkClaim("startgame", PhaseStarting, OperationActionStart)
	claim, _ := LoadRuntimeState("startgame", dir)
	if _, err := RecoverInterruptedClaim("startgame", dir, "me", claim, false, func(string) bool { return false }, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if h, _ := LoadHistory("startgame", dir); h.Profiles[""] == nil || h.Profiles[""].WorkloadStarts != 1 {
		t.Fatalf("a recovered start must credit workloadStarts++: %+v", h.Profiles[""])
	}

	// A recovered STOP promotion must not.
	mkClaim("stopgame", PhaseStopping, OperationActionStop)
	claim2, _ := LoadRuntimeState("stopgame", dir)
	if _, err := RecoverInterruptedClaim("stopgame", dir, "me", claim2, false, func(string) bool { return false }, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if h, _ := LoadHistory("stopgame", dir); h.Profiles[""] != nil && h.Profiles[""].WorkloadStarts != 0 {
		t.Fatalf("a recovered stop must NOT credit a start: %+v", h.Profiles[""])
	}
}
