package mcp

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/process"
)

// connectAdventureFailingDelivery drives a real games_connect against a fake
// bridge whose welcome report is a VERIFIED delivery, failing ONLY the delivery
// history write (after bridgeConnect lands). It returns the server with the
// verdict durably persisted and the delivery credit still pending (round 16 F5).
func connectAdventureFailingDelivery(t *testing.T) *Server {
	t.Helper()
	cwd := t.TempDir()
	managed := map[string]string{"GABS_GAME_ID": "adventure"}
	context := map[string]string{"CONTENT_SET": "combat-pack"}
	const hash = "sha256:delivery-ctx"

	s := newProfiledServer(t)
	addr, _ := fakeGABPServerWithObserved(t, "adventure", map[string]interface{}{
		"argv":      []string{"/opt/game/bin", "-profile", "combat"},
		"cwd":       cwd,
		"envValues": map[string]string{"GABS_GAME_ID": "adventure", "CONTENT_SET": "combat-pack"},
	})
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)

	digests, err := process.ComputeContextDigests([]string{"-profile", "combat"}, cwd, false, managed, context, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := process.LaunchSpec{GameId: "adventure", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.GamePID = os.Getpid()
	if start, err := process.ProcessStartTime(os.Getpid()); err == nil {
		st.PIDStartTime = start
	}
	st.Endpoint = &process.RuntimeEndpoint{Port: port, Token: "launch-token"}
	st.ContextDigests = digests
	st.HistoryContextHash = hash
	st.HistorySuccess = &process.HistorySuccessIdentity{Snapshot: process.ContextSnapshot{Target: "/opt/game"}}
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}

	// Fail only the delivery history write: allow the bridgeConnect credit first,
	// then fail the next history write once (the delivery credit).
	failed := false
	restore := process.SetSaveHistoryFailHookForTesting(func() error {
		if failed {
			return nil
		}
		h, _ := process.LoadHistory("adventure", s.configDir)
		for _, e := range h.Profiles {
			if e.BridgeConnects >= 1 {
				failed = true
				return errors.New("delivery history write failed")
			}
		}
		return nil
	})
	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "adventure", "timeout": 5})
	restore()
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("connect must succeed despite the delivery credit failure: %s", raw)
	}

	claim, _ := process.LoadRuntimeState("adventure", s.configDir)
	if claim == nil || claim.ContextDelivery == nil || claim.ContextDelivery.Overall != process.DeliveryVerified {
		t.Fatalf("the delivery verdict must persist even when its credit fails: %+v", claim)
	}
	if len(claim.PendingDeliveries) != 1 {
		t.Fatalf("the verified delivery must persist as one pending event: %+v", claim)
	}
	if h, _ := process.LoadHistory("adventure", s.configDir); h.Profiles[""] == nil || h.Profiles[""].DeliveriesVerified != 0 {
		t.Fatalf("the delivery credit must not have landed yet: %+v", h.Profiles[""])
	}
	return s
}

// TestGamesConnectDeliveryCreditReconciledByStatus: a delivery credit that fails
// at connect time is reconciled to exactly one by a later games_status.
func TestGamesConnectDeliveryCreditReconciledByStatus(t *testing.T) {
	s := connectAdventureFailingDelivery(t)
	t.Cleanup(func() { s.CleanupGABPConnection("adventure") })

	callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
	if h, _ := process.LoadHistory("adventure", s.configDir); h.Profiles[""] == nil || h.Profiles[""].DeliveriesVerified != 1 {
		t.Fatalf("games_status must reconcile deliveriesVerified to exactly 1: %+v", h.Profiles[""])
	}
	callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
	if h, _ := process.LoadHistory("adventure", s.configDir); h.Profiles[""].DeliveriesVerified != 1 {
		t.Fatalf("a second status must not double-credit: %+v", h.Profiles[""])
	}
}

// TestDeliveryCreditedAfterDisconnectThenStatus is the round-16 F5 finding-2
// reviewer reproduction through the real MCP path: the bridge DISCONNECTS
// (clearing the attachment) before any status. games_status must still credit
// the pending delivery from its self-contained event, independent of the
// now-absent attachment — exactly once.
func TestDeliveryCreditedAfterDisconnectThenStatus(t *testing.T) {
	s := connectAdventureFailingDelivery(t)

	// The bridge disconnects: ClearAttachmentIfCurrent removes the attachment.
	s.CleanupGABPConnection("adventure")
	if claim, _ := process.LoadRuntimeState("adventure", s.configDir); claim == nil || claim.Attachment != nil {
		t.Fatalf("disconnect must clear the attachment: %+v", claim)
	}

	// The workload PID is still alive, so status sees running and reconciles the
	// pending delivery WITHOUT a live attachment.
	callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
	if h, _ := process.LoadHistory("adventure", s.configDir); h.Profiles[""] == nil || h.Profiles[""].DeliveriesVerified != 1 {
		t.Fatalf("status must credit the pending delivery after a disconnect, got %+v", h.Profiles[""])
	}
}
