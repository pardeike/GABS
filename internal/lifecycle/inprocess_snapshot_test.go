package lifecycle

import (
	"errors"
	"os"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestInProcessSnapshotUsesControllerFactsOnly pins the lost-claim repair's
// facts: the republished snapshot must carry the CONTROLLER's pinned stop
// name — never the current request's (possibly hot-edited) config value — and
// only a PID actually observed for the workload, which is 0 when liveness
// came from a name lookup after the direct child exited (the stored PID would
// be the exited wrapper's). A wrong name or a dead PID would make the claim
// judged stopped and removed while the old process still runs, permitting a
// duplicate launch.
func TestInProcessSnapshotUsesControllerFactsOnly(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(util.NewLogger("error"), dir, "inst-1", &config.GamesConfig{Version: "1.0"}, 0,
		process.NewSerializedStarterForTesting(),
		func() process.ControllerInterface { return &spyMaterializeController{} })

	// The NEW request carries a hot-edited stop name and a fresh config
	// revision, neither of which describes the old workload; the claim must
	// not inherit them.
	game := config.GameConfig{ID: "g", Name: "G", LaunchMode: "DirectPath", Target: "/bin/true", StopProcessName: "edited-name"}
	_, err := m.Start(StartRequest{
		Game:           game,
		LaunchSpec:     process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true", StopProcessName: "edited-name", ConfigRevision: "rev-b"},
		HistoryContext: HistoryContext{},
		CheckInProcessActive: func() (InProcessFacts, bool) {
			// The tracked controller's facts: liveness via name lookup after
			// the direct child exited (no observed workload PID), original
			// stop name pinned in its launch spec.
			return InProcessFacts{Status: "running", StopProcessName: "orig-name"}, true
		},
	})
	var active *GameAlreadyActiveError
	if !errors.As(err, &active) {
		t.Fatalf("the fast path must report already active, got %v", err)
	}

	claim, lerr := process.LoadRuntimeState("g", dir)
	if lerr != nil || claim == nil {
		t.Fatalf("the snapshot must be published, got claim=%v err=%v", claim, lerr)
	}
	if claim.StopProcessName != "orig-name" {
		t.Fatalf("the snapshot must pin the controller's stop name, not the request's: got %q", claim.StopProcessName)
	}
	if claim.GamePID != 0 {
		t.Fatalf("name-observed liveness must not publish the exited wrapper's PID: %+v", claim)
	}
	if claim.ObservedProfile != process.ObservedProfileUnknown || claim.Profile != "" || claim.Lifecycle != nil {
		t.Fatalf("the snapshot stays unattributed: %+v", claim)
	}
	if claim.ConfigRevision != "" {
		t.Fatalf("the duplicate request's config revision must not be reported as active: %+v", claim)
	}
	if claim.HistoryContextHash != "" || claim.HistorySuccess != nil {
		t.Fatalf("the refused request's history identity must not follow the snapshot: %+v", claim)
	}
}

// TestInProcessSnapshotPublishesOnlyPinnedFingerprint pins the PID rule: the
// snapshot publishes the spawn-pinned (pid, startTime) verbatim — never a
// fresh lookup, which could fingerprint an unrelated reuse of the number —
// and suppresses the PID entirely when no pinned fingerprint exists.
func TestInProcessSnapshotPublishesOnlyPinnedFingerprint(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(util.NewLogger("error"), dir, "inst-1", &config.GamesConfig{Version: "1.0"}, 0,
		process.NewSerializedStarterForTesting(),
		func() process.ControllerInterface { return &spyMaterializeController{} })

	game := config.GameConfig{ID: "g", Name: "G", LaunchMode: "DirectPath", Target: "/bin/true"}
	_, err := m.Start(StartRequest{
		Game:           game,
		LaunchSpec:     process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"},
		HistoryContext: HistoryContext{},
		CheckInProcessActive: func() (InProcessFacts, bool) {
			return InProcessFacts{Status: "running", PID: os.Getpid(), PIDStartTime: 424242}, true
		},
	})
	var active *GameAlreadyActiveError
	if !errors.As(err, &active) {
		t.Fatalf("the fast path must report already active, got %v", err)
	}
	claim, lerr := process.LoadRuntimeState("g", dir)
	if lerr != nil || claim == nil {
		t.Fatalf("the snapshot must be published, got claim=%v err=%v", claim, lerr)
	}
	if claim.GamePID != os.Getpid() || claim.PIDStartTime != 424242 {
		t.Fatalf("the pinned fingerprint must be published verbatim, got pid=%d start=%d", claim.GamePID, claim.PIDStartTime)
	}
}
