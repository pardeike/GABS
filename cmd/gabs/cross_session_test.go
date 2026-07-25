package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestGABPHelperProcess is a portable, GABP-speaking "game": when GABS spawns it
// (GABS_GAME_ID set), it listens on the injected GABP_SERVER_PORT and answers
// the session/hello handshake so a later server session can attach. Under a
// normal `go test` run the guard makes it a no-op.
func TestGABPHelperProcess(t *testing.T) {
	if os.Getenv("GABS_GAME_ID") == "" {
		return
	}
	port := os.Getenv("GABP_SERVER_PORT")
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		// No port or bind failure: still stay alive so Stage-4 liveness passes.
		time.Sleep(90 * time.Second)
		return
	}
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveGABPConn(conn)
	}
}

func serveGABPConn(conn net.Conn) {
	defer conn.Close()
	reader := util.NewLSPFrameReader(conn)
	writer := util.NewLSPFrameWriter(conn)
	for {
		data, err := reader.ReadMessage()
		if err != nil {
			return
		}
		var req util.GABPMessage
		if json.Unmarshal(data, &req) != nil {
			return
		}
		if req.ID == "" {
			continue // notification
		}
		var result interface{} = map[string]interface{}{}
		if req.Method == "session/hello" {
			result = map[string]interface{}{
				"agentId":       "helper",
				"capabilities":  map[string]interface{}{"methods": []string{"tools/list"}},
				"schemaVersion": "1.0",
			}
		}
		if writer.WriteJSON(util.NewGABPResponse(req.ID, result)) != nil {
			return
		}
	}
}

// TestCLICrossSessionConnectAndHotReload is the binding cross-session T-CLI +
// hot-reload composition (design/30, design/11): a real CLI subprocess starts a
// GABP-speaking game and exits; a still-running server session attaches to the
// CLI-created claim/endpoint via games_connect; then, without restarting that
// server, a profile is renamed on disk and the renamed profile is launched
// through the same server session. Portable (the game target is the test binary).
func TestCLICrossSessionConnectAndHotReload(t *testing.T) {
	gabs := buildGabs(t)
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// Initial config: one profiled game whose target is the GABP helper.
	writeCrossSessionConfig(t, dir, testBin, "p1", "p1")

	// 1. CLI start (a real subprocess) — leaves a GABP server running + an active
	// claim, then exits.
	out, code := runGabsGames(t, gabs, dir, "start", "g", "--profile", "p1")
	if code != 0 || !strings.Contains(out, "started_attachment_deferred") {
		t.Fatalf("CLI start: code=%d out=%s", code, out)
	}
	claim, _ := process.LoadRuntimeState("g", dir)
	if claim == nil {
		t.Fatal("no claim after CLI start")
	}
	killPIDOnCleanup(t, claim.GamePID, claim.PIDStartTime)
	if claim.Endpoint == nil || claim.Endpoint.Port == 0 {
		t.Fatalf("CLI claim must carry an endpoint for attach: %+v", claim.Endpoint)
	}

	// 2. A still-running SERVER session attaches from the CLI-created claim.
	server, store := newCrossSessionServer(t, dir)
	connectOut := serverCall(t, server, "games.connect", map[string]interface{}{"gameId": "g"})
	if strings.Contains(connectOut, `"isError":true`) || !strings.Contains(connectOut, "Successfully connected") {
		t.Fatalf("server games_connect to the CLI-created claim must succeed: %s", connectOut)
	}
	_ = store

	// 3. Stop the game through the same server, so a fresh launch can run.
	stopOut := serverCall(t, server, "games.stop", map[string]interface{}{"gameId": "g"})
	if strings.Contains(stopOut, `"isError":true`) {
		t.Fatalf("server games_stop failed: %s", stopOut)
	}

	// 4. Rename the profile on disk (p1 -> p2) WITHOUT restarting the server.
	writeCrossSessionConfig(t, dir, testBin, "p2", "p2")

	// 5. Start the renamed profile through the SAME server session: the server's
	// config store re-reads the file, so the renamed profile resolves.
	startOut := serverCall(t, server, "games.start", map[string]interface{}{"gameId": "g", "profile": "p2"})
	if strings.Contains(startOut, "profile_not_found") || strings.Contains(startOut, "config_invalid") {
		t.Fatalf("the renamed profile must launch through the running server (no restart): %s", startOut)
	}
	if !strings.Contains(startOut, "started_") {
		t.Fatalf("expected a started_* outcome for the renamed profile, got: %s", startOut)
	}
	if c2, _ := process.LoadRuntimeState("g", dir); c2 != nil {
		killPIDOnCleanup(t, c2.GamePID, c2.PIDStartTime)
		if c2.Profile != "p2" {
			t.Fatalf("the renamed profile launch must record activeProfile p2, got %q", c2.Profile)
		}
	}
}

// --- helpers ---

func writeCrossSessionConfig(t *testing.T, dir, testBin, profile, defaultProfile string) {
	t.Helper()
	cfg := `{"version":"1.0","timeouts":{"startup":{"gabpConnectSeconds":3}},"games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":` + jsonString(testBin) + `,"args":["-test.run=TestGABPHelperProcess"],"defaultProfile":"` + defaultProfile + `","profiles":{"` + profile + `":{}}}}}`
	writeCLIConfig(t, dir, cfg)
}

func newCrossSessionServer(t *testing.T, dir string) (*mcp.Server, *config.Store) {
	t.Helper()
	cp, err := config.NewConfigPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, snap, cerr := config.NewSeededStore(cp.GetMainConfigPath())
	if cerr != nil {
		t.Fatalf("seed store: %v", cerr)
	}
	server := mcp.NewServer(util.NewLogger("error"))
	server.SetConfigDir(dir)
	server.RegisterGameManagementTools(snap.Config, 50*time.Millisecond, 1*time.Second)
	server.SetConfigStore(store)
	t.Cleanup(server.Shutdown)
	return server, store
}

func serverCall(t *testing.T, server *mcp.Server, tool string, args map[string]interface{}) string {
	t.Helper()
	resp := server.HandleMessage(&mcp.Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"x"`),
		Params:  map[string]interface{}{"name": tool, "arguments": args},
	})
	b, _ := json.Marshal(resp)
	return string(b)
}

func runGabsGames(t *testing.T, gabs, dir string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"games", "--configDir", dir}, args...)
	cmd := exec.Command(gabs, full...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running gabs %v: %v", args, err)
	}
	return string(out), code
}

func killPIDOnCleanup(t *testing.T, pid int, start int64) {
	t.Helper()
	t.Cleanup(func() {
		if pid <= 0 {
			return
		}
		if v, _ := process.VerifyPIDFingerprint(pid, start); v == process.StatusRunning {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})
}
