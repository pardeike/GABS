package mcp

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestGamesCallToolBridgePayloadNotAttributed is the round-14 F2 end-to-end
// reproduction: a game's successful GABP result whose key collides with a GABS
// stable code ({"code":"spawn_failed",...}) is forwarded VERBATIM — GABS failure
// attribution must never inject causeClass/trackRecord/nextActions into a game
// payload just because a key looks like a lifecycle code.
func TestGamesCallToolBridgePayloadNotAttributed(t *testing.T) {
	server := newProvenanceCallToolServer(t, map[string]interface{}{
		"code":  "spawn_failed", // collides with a GABS stable code
		"value": float64(7),
	})

	connect := callProvTool(t, server, "games.connect", map[string]interface{}{"gameId": "adventure"})
	if strings.Contains(connect, `"isError":true`) {
		t.Fatalf("connect failed: %s", connect)
	}

	raw := callProvTool(t, server, "games.call_tool", map[string]interface{}{
		"tool": "adventure.adventure.collide",
	})
	var resp struct {
		Result struct {
			StructuredContent map[string]interface{} `json:"structuredContent"`
			IsError           bool                   `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, raw)
	}
	sc := resp.Result.StructuredContent
	// The game payload passes through unchanged...
	if sc["code"] != "spawn_failed" || sc["value"] != float64(7) {
		t.Fatalf("the game payload must pass through verbatim: %#v", sc)
	}
	// ...and GABS attribution NEVER touched it.
	for _, k := range []string{"causeClass", "trackRecord", "nextActions"} {
		if _, has := sc[k]; has {
			t.Fatalf("GABS attribution %q leaked into a bridge payload: %#v", k, sc)
		}
	}
}

// TestCompleteFailureAttributionRespectsProvenance unit-tests the central gate:
// a BridgePassthrough result (success OR error) with a colliding code is left
// untouched, while a GABS-owned coded result still gets attributed.
func TestCompleteFailureAttributionRespectsProvenance(t *testing.T) {
	s := NewServerForTesting(t, util.NewLogger("error"))

	for _, isErr := range []bool{false, true} {
		res := &ToolResult{
			StructuredContent: map[string]interface{}{"code": "spawn_failed", "value": 7},
			IsError:           isErr,
			BridgePassthrough: true,
		}
		s.completeFailureAttribution("games_call_tool", res)
		for _, k := range []string{"causeClass", "trackRecord", "nextActions"} {
			if _, has := res.StructuredContent[k]; has {
				t.Fatalf("isError=%v: a bridge passthrough must not be attributed (%q injected)", isErr, k)
			}
		}
	}

	// Control: a GABS-owned coded failure IS attributed (proves the gate keys on
	// provenance, not a blanket skip).
	owned := &ToolResult{
		StructuredContent: map[string]interface{}{"code": "endpoint_unavailable", "gameId": "adventure"},
		IsError:           true,
	}
	s.completeFailureAttribution("games_start", owned)
	if owned.StructuredContent["causeClass"] != process.CauseEnvironment {
		t.Fatalf("a GABS-owned coded failure must still be attributed: %#v", owned.StructuredContent)
	}
}

// TestGabsOwnedCallToolFailuresAttributed proves the three GABS-owned
// games_call_tool wrapper failures carry direct class attribution and mint NO
// new stable lifecycle code.
func TestGabsOwnedCallToolFailuresAttributed(t *testing.T) {
	s := NewServerForTesting(t, util.NewLogger("error"))

	cases := []struct {
		name  string
		res   *ToolResult
		class string
	}{
		{"missing-tool-arg", s.gabsCallToolFailure("adventure", "Missing required argument: tool", process.CauseCall), process.CauseCall},
		{"no-connection", s.gabsCallToolFailure("adventure", "not connected", process.CauseState), process.CauseState},
		{"transport", s.gabpCallErrorResult("adventure", errStub("boom")), process.CauseState},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := c.res.StructuredContent
			if sc == nil {
				t.Fatal("a GABS-owned failure must carry attribution")
			}
			if sc["causeClass"] != c.class {
				t.Fatalf("causeClass = %v, want %s", sc["causeClass"], c.class)
			}
			if _, has := sc["trackRecord"]; !has {
				t.Fatal("must carry a track-record line")
			}
			if _, has := sc["nextActions"]; !has {
				t.Fatal("must carry class-keyed next actions")
			}
			// No NEW stable lifecycle code is minted — attribution is by class.
			if code, has := sc["code"]; has {
				t.Fatalf("a wrapper failure must not mint a lifecycle code, got code=%v", code)
			}
			if !c.res.IsError {
				t.Fatal("a wrapper failure must be an error result")
			}
		})
	}

	// Also drive the missing-tool-arg branch end-to-end (empty tool string).
	server := newProfiledServer(t)
	raw, structured := callTool(t, server, "games.call_tool", map[string]interface{}{"tool": ""})
	if !strings.Contains(raw, `"isError":true`) {
		t.Fatalf("empty tool must be an error: %s", raw)
	}
	if structured["causeClass"] != process.CauseCall {
		t.Fatalf("a malformed tool argument is a caller error, got %#v", structured["causeClass"])
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// callProvTool dispatches a core tool through the real message handler and
// returns the raw JSON response.
func callProvTool(t *testing.T, s *Server, name string, args map[string]interface{}) string {
	t.Helper()
	return marshalMessage(t, s.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"prov-1"`),
		Params:  map[string]interface{}{"name": name, "arguments": args},
	}))
}

// newProvenanceCallToolServer wires a game to a fake GABP bridge whose single
// tool returns the given payload on every tools/call.
func newProvenanceCallToolServer(t *testing.T, toolResult map[string]interface{}) *Server {
	t.Helper()
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	const token = "prov-token"
	go serveProvenanceGabpSession(listener, token, toolResult)

	port := listener.Addr().(*net.TCPAddr).Port
	cp, err := config.NewConfigPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.EnsureGameDir("adventure"); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.MarshalIndent(config.BridgeJSON{Port: port, Token: token, GameId: "adventure"}, "", "  ")
	if err := os.WriteFile(filepath.Join(cp.GetGameDir("adventure"), "bridge.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	seedClaimEndpointForTest(t, dir, "adventure", port, token)

	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(dir)
	server.RegisterGameManagementTools(&config.GamesConfig{
		Games: map[string]config.GameConfig{
			"adventure": {ID: "adventure", Name: "Adventure", LaunchMode: "DirectPath", Target: "/opt/game"},
		},
	}, 100*time.Millisecond, 1*time.Second)
	return server
}

func serveProvenanceGabpSession(listener net.Listener, token string, toolResult map[string]interface{}) {
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
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
			continue
		}
		switch request.Method {
		case "session/hello":
			_ = writer.WriteJSON(util.NewGABPResponse(request.ID, gabp.SessionWelcomeResult{
				AgentID:       "adventure",
				App:           gabp.AppInfo{Name: "ProvBridge", Version: "0.1.0"},
				Capabilities:  gabp.Capabilities{Methods: []string{"tools/list", "tools/call"}},
				SchemaVersion: "1.0",
			}))
		case "tools/list":
			_ = writer.WriteJSON(util.NewGABPResponse(request.ID, map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "adventure/collide", "description": "returns a colliding payload",
						"inputSchema": map[string]interface{}{"type": "object"}},
				},
			}))
		case "tools/call":
			_ = writer.WriteJSON(util.NewGABPResponse(request.ID, toolResult))
		default:
			_ = writer.WriteJSON(util.NewGABPResponse(request.ID, map[string]interface{}{}))
		}
	}
}
