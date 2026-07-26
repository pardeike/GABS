package process

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestBridgePathHonorsRuntimeDir covers a broken context-delivery channel:
// getBridgePath hardcoded $HOME/.gabs/<gameId>/bridge.json and never consulted
// the spec's RuntimeDir, while every other path in the codebase resolves through
// config.NewConfigPaths. Under --configDir, GABS therefore wrote bridge.json
// into the configured directory but told the game to read the one under
// $HOME/.gabs — a stale file carrying a different port and token.
func TestBridgePathHonorsRuntimeDir(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "sandbox", "adventure")

	c := &Controller{spec: LaunchSpec{
		GameId:     "adventure",
		Mode:       "DirectPath",
		PathOrId:   "/bin/echo",
		RuntimeDir: runtimeDir,
	}}

	want := filepath.Join(runtimeDir, "bridge.json")
	if got := c.getBridgePath(); got != want {
		t.Errorf("getBridgePath() = %q, want %q", got, want)
	}
	if got := c.runtimeDir(); got != runtimeDir {
		t.Errorf("runtimeDir() = %q, want %q", got, runtimeDir)
	}
	if got := c.launchLogPath(); got != filepath.Join(runtimeDir, "launch.log") {
		t.Errorf("launchLogPath() = %q, want it beside bridge.json", got)
	}
}

// TestBridgePathEnvHonorsRuntimeDir pins the delivered value, since the
// environment variable is what a game-side bridge actually reads.
func TestBridgePathEnvHonorsRuntimeDir(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "sandbox", "adventure")
	want := "GABS_BRIDGE_PATH=" + filepath.Join(runtimeDir, "bridge.json")

	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "resolved context", env: map[string]string{"DATA_ROOT": "/tmp/x"}},
		{name: "legacy context", env: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Controller{
				spec: LaunchSpec{
					GameId:     "adventure",
					Mode:       "DirectPath",
					PathOrId:   "/bin/echo",
					RuntimeDir: runtimeDir,
					Env:        tc.env,
				},
				bridgeInfo: &BridgeInfo{Port: 12345, Token: "t"},
			}
			var found string
			for _, kv := range c.buildEnvironment() {
				if strings.HasPrefix(kv, "GABS_BRIDGE_PATH=") {
					found = kv
				}
			}
			if found != want {
				t.Errorf("delivered %q, want %q", found, want)
			}
		})
	}
}

// TestBridgePathFallbackUnchanged keeps the legacy default intact for callers
// that never stamp a runtime dir.
func TestBridgePathFallbackUnchanged(t *testing.T) {
	c := &Controller{spec: LaunchSpec{GameId: "adventure", Mode: "DirectPath"}}

	got := c.getBridgePath()
	wantSuffix := filepath.Join(".gabs", "adventure", "bridge.json")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("getBridgePath() = %q, want it to end in %q", got, wantSuffix)
	}
	if got := c.runtimeDir(); !strings.HasSuffix(got, filepath.Join(".gabs", "adventure")) {
		t.Errorf("runtimeDir() = %q, want the legacy per-game default", got)
	}
}
