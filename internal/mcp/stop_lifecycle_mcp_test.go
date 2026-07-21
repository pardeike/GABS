package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

func TestGamesStopTerminatesManagedGame(t *testing.T) {
	t.Setenv("GABSTEST_HELPER_PROCESS", "1")
	s := newProfiledServer(t)
	raw, _ := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "timeout": 1,
	})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("start failed: %s", raw)
	}

	raw, structured := callTool(t, s, "games.stop", map[string]interface{}{"gameId": "adventure"})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("stop of a managed game must succeed: %s", raw)
	}
	if structured["code"] != "terminated" {
		t.Fatalf("expected terminated, got %s", raw)
	}
	if claim, _ := process.LoadRuntimeState("adventure", s.configDir); claim != nil {
		t.Fatalf("terminated must clear the claim: %+v", claim)
	}
}

func TestGamesStopUnsupportedForKillOnlyClaim(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := NewServerForTesting(util.NewLogger("error"))
	s.SetConfigDir(dir)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"urlgame": {ID: "urlgame", Name: "U", LaunchMode: "SteamAppId", Target: "123456"},
		},
	}, 0, 0)

	// A kill-only URL claim: helper-role PID, no stopProcessName, kill hook
	// pinned but no stop hook (design/06).
	spec := process.LaunchSpec{GameId: "urlgame", Mode: "SteamAppId", PathOrId: "123456"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.Lifecycle = &launch.ResolvedLifecycle{
		Kill: &launch.ResolvedHook{Command: exe, TimeoutSeconds: 5, VerifyTimeoutSeconds: 1},
	}
	if err := process.ClaimRuntimeState("urlgame", dir, st); err != nil {
		t.Fatal(err)
	}

	raw, structured := callTool(t, s, "games.stop", map[string]interface{}{"gameId": "urlgame"})
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("stop_unsupported must be an error result: %s", raw)
	}
	if structured["code"] != "stop_unsupported" {
		t.Fatalf("expected stop_unsupported, got %s", raw)
	}
	if !strings.Contains(raw, "games_kill") {
		t.Fatalf("the refusal must point at games_kill: %s", raw)
	}
	if claim, _ := process.LoadRuntimeState("urlgame", dir); claim == nil || claim.Operation != nil {
		t.Fatalf("a refusal must not write anything: %+v", claim)
	}
}

func TestGamesStatusReportsStoppingPhaseWithoutBlockingOrCleaning(t *testing.T) {
	s := newProfiledServer(t)

	ownStart, err := process.ProcessStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	spec := process.LaunchSpec{GameId: "adventure", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseStopping
	st.SpawnState = process.SpawnStateSpawned
	st.Operation = &process.RuntimeOperation{
		OperationID: process.NewFencingID(), Action: "stop",
		ExecutorPID: os.Getpid(), ExecutorPIDStartTime: ownStart,
		AttemptStartedAt: time.Now().UTC(),
		Deadline:         time.Now().UTC().Add(time.Minute),
	}
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}

	startAt := time.Now()
	raw, structured := callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
	if elapsed := time.Since(startAt); elapsed > 5*time.Second {
		t.Fatalf("status must never block on an in-flight operation, took %v", elapsed)
	}
	if structured["phase"] != "stopping" {
		t.Fatalf("status must report the persisted phase, got %s", raw)
	}
	op, ok := structured["operation"].(map[string]interface{})
	if !ok || op["deadline"] == nil || op["action"] != "stop" {
		t.Fatalf("status must render the attempt's timing: %s", raw)
	}
	// The evidence probe found nothing running, but an in-flight operation's
	// claim must never be cleaned from the status path.
	if claim, _ := process.LoadRuntimeState("adventure", s.configDir); claim == nil {
		t.Fatal("status must not remove a claim that carries an in-flight operation")
	}
}

func TestGamesStatusSurfacesLastActionResult(t *testing.T) {
	s := newProfiledServer(t)

	exitCode := 3
	spec := process.LaunchSpec{GameId: "adventure", Mode: "DirectPath", PathOrId: "/opt/game", StopProcessName: "adventure-workload"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.LastActionResult = &process.RuntimeActionResult{
		Action: "stop", Outcome: "action_failed",
		ExitCode: &exitCode, StderrTail: "save in progress",
		Timestamp: time.Now().UTC(),
	}
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}
	restore := process.SetFindProcessesByNameForTesting(func(name string) ([]int, error) {
		return []int{os.Getpid()}, nil // workload still running
	})
	defer restore()

	raw, structured := callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
	lar, ok := structured["lastActionResult"].(map[string]interface{})
	if !ok {
		t.Fatalf("status must include lastActionResult when present: %s", raw)
	}
	if lar["outcome"] != "action_failed" || lar["action"] != "stop" {
		t.Fatalf("lastActionResult content wrong: %s", raw)
	}
	if lar["stderrTail"] != "save in progress" {
		t.Fatalf("the stderr tail is the debugging signal and must render: %s", raw)
	}
}

func TestBridgeAttachmentRecordPromotesAndFences(t *testing.T) {
	s := newProfiledServer(t)

	// A completed-unobserved claim: phase starting, spawn done, operation
	// cleared — the passive-promotion case (design/05 Stage 4).
	spec := process.LaunchSpec{GameId: "adventure", Mode: "SteamAppId", PathOrId: "123"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusStarting)
	st.SpawnState = process.SpawnStateSpawned
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}

	s.recordBridgeAttachment("adventure", func() bool { return true })

	claim, err := process.LoadRuntimeState("adventure", s.configDir)
	if err != nil || claim == nil || claim.Attachment == nil {
		t.Fatalf("attach must persist the attachment record: %+v %v", claim, err)
	}
	first := *claim.Attachment
	if first.ConnectionID == "" || first.OwnerPID != os.Getpid() || first.OwnerPIDStartTime == 0 {
		t.Fatalf("the record must carry the owner fingerprint: %+v", first)
	}
	if first.LeaseDeadline.IsZero() || !first.LeaseDeadline.After(time.Now()) {
		t.Fatalf("the lease must be fresh: %+v", first)
	}
	if claim.Phase != process.PhaseActive || claim.Status != process.RuntimeStateStatusRunning {
		t.Fatalf("a bridge attach proves running and promotes the starting claim: %+v", claim)
	}

	// A second attachment (reconnect) rotates the connection identity.
	s.recordBridgeAttachment("adventure", func() bool { return true })
	claim, _ = process.LoadRuntimeState("adventure", s.configDir)
	if claim.Attachment.ConnectionID == first.ConnectionID {
		t.Fatal("each attachment lifetime gets its own connectionID")
	}
	second := *claim.Attachment

	// A stale disconnect (the first connection's identity) must not clear
	// the newer connection (design/06).
	s.clearBridgeAttachment("adventure", claim.LaunchID, first.ConnectionID)
	claim, _ = process.LoadRuntimeState("adventure", s.configDir)
	if claim.Attachment == nil || claim.Attachment.ConnectionID != second.ConnectionID {
		t.Fatalf("an old disconnect must never clear a newer connection: %+v", claim.Attachment)
	}

	// The matching disconnect clears it.
	s.clearBridgeAttachment("adventure", claim.LaunchID, second.ConnectionID)
	claim, _ = process.LoadRuntimeState("adventure", s.configDir)
	if claim.Attachment != nil {
		t.Fatalf("the matching disconnect must clear the record: %+v", claim.Attachment)
	}
}

func TestGamesStatusPromotesUnobservedClaimOnRunningEvidence(t *testing.T) {
	s := newProfiledServer(t)

	// A completed-unobserved claim (design/05 Stage 4): phase starting,
	// spawn done, operation cleared — the store launcher was slower than
	// the budget, but the workload appeared later.
	spec := process.LaunchSpec{GameId: "adventure", Mode: "SteamAppId", PathOrId: "123", StopProcessName: "adventure-workload"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusStarting)
	st.SpawnState = process.SpawnStateSpawned
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}
	restore := process.SetFindProcessesByNameForTesting(func(name string) ([]int, error) {
		if name == "adventure-workload" {
			return []int{os.Getpid()}, nil
		}
		return nil, nil
	})
	defer restore()

	_, structured := callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
	if structured["status"] != "running" && structured["phase"] != "active" {
		claim, _ := process.LoadRuntimeState("adventure", s.configDir)
		t.Fatalf("running evidence must promote the unobserved claim: structured=%v claim=%+v", structured, claim)
	}
	claim, err := process.LoadRuntimeState("adventure", s.configDir)
	if err != nil || claim == nil {
		t.Fatalf("claim must survive promotion: %v", err)
	}
	if claim.Phase != process.PhaseActive || claim.Status != process.RuntimeStateStatusRunning {
		t.Fatalf("promotion must persist: %+v", claim)
	}
}

// fakeGABPServer accepts one GABP connection and answers the handshake and
// any follow-up requests generically until the connection closes.
func fakeGABPServer(t *testing.T, agentID string) (addr string, closeConn func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		connCh <- conn
		reader := util.NewLSPFrameReader(conn)
		writer := util.NewLSPFrameWriter(conn)
		for {
			data, err := reader.ReadMessage()
			if err != nil {
				return
			}
			var request util.GABPMessage
			if err := json.Unmarshal(data, &request); err != nil {
				return
			}
			if request.ID == "" {
				continue // notification
			}
			var result interface{} = map[string]interface{}{}
			if request.Method == "session/hello" {
				result = gabp.SessionWelcomeResult{
					AgentID:       agentID,
					Capabilities:  gabp.Capabilities{Methods: []string{"tools/list"}},
					SchemaVersion: "1.0",
				}
			}
			if err := writer.WriteJSON(util.NewGABPResponse(request.ID, result)); err != nil {
				return
			}
		}
	}()

	return listener.Addr().String(), func() {
		listener.Close()
		select {
		case conn := <-connCh:
			conn.Close()
		case <-time.After(2 * time.Second):
		}
	}
}

func TestGamesConnectRecordsAttachmentAndDisconnectClearsIt(t *testing.T) {
	s := newProfiledServer(t)

	addr, closeConn := fakeGABPServer(t, "adventure")
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)

	spec := process.LaunchSpec{GameId: "adventure", Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusStarting)
	st.SpawnState = process.SpawnStateSpawned
	st.Endpoint = &process.RuntimeEndpoint{Port: port, Token: "claim-token"}
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}

	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "adventure", "timeout": 5})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("connect against the fake bridge failed: %s", raw)
	}
	t.Cleanup(func() { s.CleanupGABPConnection("adventure") })

	claim, err := process.LoadRuntimeState("adventure", s.configDir)
	if err != nil || claim == nil || claim.Attachment == nil {
		t.Fatalf("a successful connect must persist the attachment record: %+v %v", claim, err)
	}
	if claim.Phase != process.PhaseActive {
		t.Fatalf("attach success promotes the starting claim to active: %+v", claim)
	}

	// Server-side connection death must clear the record via the disconnect
	// callback, fenced by launchID + connectionID.
	closeConn()
	deadline := time.Now().Add(5 * time.Second)
	for {
		claim, _ = process.LoadRuntimeState("adventure", s.configDir)
		if claim == nil || claim.Attachment == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the disconnect must clear the attachment record: %+v", claim.Attachment)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
