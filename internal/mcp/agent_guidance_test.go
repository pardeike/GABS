package mcp

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/lifecycle"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestExternalSnapshotStatusSuppressesConnect pins the next actions for an
// externally observed instance: its snapshot has no bridge endpoint, so the
// generic games_connect recommendation would always answer
// attachment-unavailable — status must offer the operations that work.
func TestExternalSnapshotStatusSuppressesConnect(t *testing.T) {
	dir := t.TempDir()
	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{
		"g": {ID: "g", Name: "G", LaunchMode: "DirectPath", Target: "/bin/true"},
	}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(dir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.Source = process.SourceExternal
	st.ObservedProfile = process.ObservedProfileUnknown
	// Our own live PID keeps the snapshot provably running (so status never
	// cleans it) and gives stop/kill the built-in mechanism the
	// capability-aware guidance must offer.
	st.GamePID = os.Getpid()
	st.PIDRole = process.PIDRoleWorkload
	if fp, err := process.ProcessStartTime(st.GamePID); err == nil {
		st.PIDStartTime = fp
	}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("s", "games.status", "g")))
	if strings.Contains(statusText, `"tool":"games_connect"`) {
		t.Fatalf("games_connect cannot attach to an external snapshot and must not be recommended: %s", statusText)
	}
	if !strings.Contains(statusText, `"tool":"games_stop"`) {
		t.Fatalf("the supported stop operation must be offered instead: %s", statusText)
	}
	if !strings.Contains(statusText, `"source":"external"`) {
		t.Fatalf("the snapshot's source must be exposed: %s", statusText)
	}

	// The same runtime state must give the same guidance from EVERY view:
	// games_show and aggregate status must not advertise connect either.
	showText := marshalMessage(t, server.HandleMessage(toolCallMessage("sh", "games.show", "g")))
	if strings.Contains(showText, `"tool":"games_connect"`) {
		t.Fatalf("games_show must apply the same source-aware guidance: %s", showText)
	}
	aggText := marshalMessage(t, server.HandleMessage(noArgStatusMessage("agg")))
	if strings.Contains(aggText, `"tool":"games_connect"`) {
		t.Fatalf("aggregate status must apply the same source-aware guidance: %s", aggText)
	}
}

// TestExternalGuidanceOffersOnlyExecutableActions pins capability-aware next
// actions: an external snapshot with a stop hook but no kill hook (and no
// PID or stop name for the built-in fallback) must offer games_stop and not
// games_kill — selecting the missing one would just return kill_unsupported.
func TestExternalGuidanceOffersOnlyExecutableActions(t *testing.T) {
	dir := t.TempDir()
	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{
		"g": {ID: "g", Name: "G", LaunchMode: "DirectPath", Target: "/bin/true"},
	}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(dir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.Source = process.SourceExternal
	st.ObservedProfile = process.ObservedProfileUnknown
	st.StopProcessName = ""
	st.GamePID = 0
	st.Lifecycle = &launch.ResolvedLifecycle{Stop: &launch.ResolvedHook{Command: "/bin/true", TimeoutSeconds: 5}}
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	statusText := marshalMessage(t, server.HandleMessage(toolCallMessage("s", "games.status", "g")))
	if !strings.Contains(statusText, `"tool":"games_stop"`) {
		t.Fatalf("the hook-backed stop must be offered: %s", statusText)
	}
	if strings.Contains(statusText, `"tool":"games_kill"`) {
		t.Fatalf("kill has no mechanism on this claim and must not be offered: %s", statusText)
	}
}

// TestShowHonorsHookBasedTermination pins games_show's termination section: a
// URL-mode game whose hooks satisfy the validator must not be told to add
// stopProcessName.
func TestShowHonorsHookBasedTermination(t *testing.T) {
	dir := t.TempDir()
	gamesConfig := &config.GamesConfig{Games: map[string]config.GameConfig{
		"g": {ID: "g", Name: "G", LaunchMode: "SteamAppId", Target: "12345",
			Lifecycle: &config.LifecycleConfig{
				Status: &config.HookConfig{Command: "/bin/true"},
				Stop:   &config.HookConfig{Command: "/bin/true"},
			}},
	}}
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(dir)
	server.RegisterGameManagementTools(gamesConfig, 10*time.Millisecond, 20*time.Millisecond)

	showText := marshalMessage(t, server.HandleMessage(toolCallMessage("sh", "games.show", "g")))
	if strings.Contains(showText, "Missing stopProcessName") {
		t.Fatalf("a hook-controlled configuration is valid and must not be flagged: %s", showText)
	}
	if !strings.Contains(showText, "lifecycle hooks") {
		t.Fatalf("the hook-based termination path must be acknowledged: %s", showText)
	}
}

// TestAmbiguousCandidatesRefusalAvoidsStatusLoop pins the multi-candidate
// external refusal: no claim is persisted, so games_status reruns no probes
// and reports stopped — recommending it loops. The refusal must surface the
// candidates and a manual resolution instead.
func TestAmbiguousCandidatesRefusalAvoidsStatusLoop(t *testing.T) {
	server := NewServerForTesting(t, util.NewLogger("error"))
	server.SetConfigDir(t.TempDir())

	res := server.startRefusalResult(config.GameConfig{ID: "g", Name: "G"}, &lifecycle.StartRefusalError{
		Refusal: &process.StartRefusal{
			Code:       process.RefusalExternalInstance,
			Message:    "multiple profiles of g report running (a, b); resolve manually before starting",
			Candidates: []string{"a", "b"},
		},
	}, historyContext{}, nil)

	if _, ok := res.StructuredContent["nextActions"]; ok {
		t.Fatalf("no tool action resolves ambiguous candidates; recommending one loops: %v", res.StructuredContent)
	}
	if _, ok := res.StructuredContent["candidates"]; !ok {
		t.Fatalf("the candidate evidence must be surfaced: %v", res.StructuredContent)
	}
	if !strings.Contains(res.Content[0].Text, "Resolve manually") {
		t.Fatalf("the text must carry the manual resolution step: %s", res.Content[0].Text)
	}
}

// TestURLModeWarningsRecognizeHookAlternative pins the validator/warning
// agreement: a URL-mode game whose lifecycle hooks satisfy the control
// requirement must not be told stopProcessName is missing.
func TestURLModeWarningsRecognizeHookAlternative(t *testing.T) {
	hooked := config.GameConfig{
		ID: "g", Name: "G", LaunchMode: "SteamAppId", Target: "12345",
		Lifecycle: &config.LifecycleConfig{
			Status: &config.HookConfig{Command: "/bin/true"},
			Stop:   &config.HookConfig{Command: "/bin/true"},
		},
	}
	if warns := gameValidationWarnings(hooked); len(warns) != 0 {
		t.Fatalf("hook-based lifecycle control satisfies the requirement; no warning expected, got %v", warns)
	}

	bare := config.GameConfig{ID: "g", Name: "G", LaunchMode: "SteamAppId", Target: "12345"}
	warns := gameValidationWarnings(bare)
	if len(warns) == 0 || !strings.Contains(warns[0], "status hook") {
		t.Fatalf("the warning must present the hook alternative alongside stopProcessName, got %v", warns)
	}
}
