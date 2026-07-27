package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

// TestGamesShowSurfacesConfigWarningsInText pins the text surface for per-game
// load warnings: an unknown key such as a typo'd "envv" is deliberately
// nonfatal, so its warning is the only signal that the next launch will not
// use the intended context — and some MCP clients never show structured
// content, so the text must carry it.
func TestGamesShowSurfacesConfigWarningsInText(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	body := `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","envv":{"DATA":"x"}}}}`
	if err := os.WriteFile(cfgPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadGamesConfigFromPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(dir)
	server.RegisterGameManagementTools(loaded, 10*time.Millisecond, 20*time.Millisecond)

	showText := marshalMessage(t, server.HandleMessage(toolCallMessage("show", "games.show", "g")))
	if !strings.Contains(showText, "Configuration Warnings") || !strings.Contains(showText, "envv") {
		t.Fatalf("games_show text must carry the unknown-key warning: %s", showText)
	}
}

// TestRuntimeOnlyStatusSurfacesConfigError pins config-health parity across
// status forms: after a valid edit removes a running game and a later hot
// edit breaks the file, the runtime-only branch must still attach the active
// config error — the answer must not depend on which status form was used.
func TestRuntimeOnlyStatusSurfacesConfigError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	valid := `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/bin/echo"}}}`
	if err := os.WriteFile(cfgPath, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}

	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(dir)
	initial, err := config.LoadGamesConfigFromPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	server.RegisterGameManagementTools(initial, 0, 0)
	server.SetConfigStore(config.NewStore(cfgPath))
	seedRuntimeOnlyClaim(t, dir, "ghost")

	// Break the config on disk: the last-good snapshot (without ghost) still
	// serves reads, and the runtime-only branch answers for ghost.
	if err := os.WriteFile(cfgPath, []byte(`{"version":"1.0","games":{`), 0600); err != nil {
		t.Fatal(err)
	}

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("s", "games.status", "ghost")))
	if !strings.Contains(statusText, "unconfigured") {
		t.Fatalf("ghost must resolve runtime-only: %s", statusText)
	}
	if !strings.Contains(statusText, "configError") {
		t.Fatalf("the runtime-only status must surface the active config error: %s", statusText)
	}
	// Text-only clients must see it too: the stale-config marker belongs in
	// the textual content, not only in structured fields.
	if !strings.Contains(statusText, "INVALID") {
		t.Fatalf("the invalid-config notice must appear in text content: %s", statusText)
	}

	listText := marshalMessage(t, server.HandleMessage(&Message{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      json.RawMessage(`"l"`),
		Params:  map[string]interface{}{"name": "games.list", "arguments": map[string]interface{}{}},
	}))
	if !strings.Contains(listText, "INVALID") {
		t.Fatalf("games_list text must carry the invalid-config notice while serving last-good values: %s", listText)
	}
}
