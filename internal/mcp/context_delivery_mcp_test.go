package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/process"
)

func TestStartPinsContextDigestsWithoutRawValues(t *testing.T) {
	t.Setenv("GABSTEST_HELPER_PROCESS", "1")
	s := newProfiledServer(t)
	raw, _ := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "profile": "combat", "timeout": 1,
	})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("start failed: %s", raw)
	}
	defer callTool(t, s, "games.kill", map[string]interface{}{"gameId": "adventure"})

	claim, err := process.LoadRuntimeState("adventure", s.configDir)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	d := claim.ContextDigests
	if d == nil || d.Salt == "" || d.ArgvSHA256 == "" {
		t.Fatalf("expected-context digests must be pinned at spawn: %+v", d)
	}
	if _, ok := d.ManagedEnvSHA256["GABP_TOKEN"]; !ok {
		t.Fatalf("the forwarded managed values must be digested: %+v", d.ManagedEnvSHA256)
	}
	if _, ok := d.ManagedEnvSHA256["GABP_SERVER_PORT"]; !ok {
		t.Fatalf("the forwarded managed values must be digested: %+v", d.ManagedEnvSHA256)
	}

	// Privacy: the raw per-launch token must never appear in the claim
	// file outside the endpoint field — digests are non-reversible.
	if claim.Endpoint == nil || claim.Endpoint.Token == "" {
		t.Fatalf("claim endpoint missing: %+v", claim)
	}
	for k, v := range d.ManagedEnvSHA256 {
		if strings.Contains(v, claim.Endpoint.Token) {
			t.Fatalf("digest for %s leaks the raw token", k)
		}
	}
}

// seedClaimWithDigests publishes an active claim carrying an endpoint plus
// digests over known values, so a fake bridge can report matching or
// mismatching observations.
func seedClaimWithDigests(t *testing.T, s *Server, gameID string, port int, token, cwd string, managed, context map[string]string) *process.RuntimeContextDigests {
	t.Helper()
	digests, err := process.ComputeContextDigests([]string{"-profile", "combat"}, cwd, false, managed, context, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := process.LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.GamePID = os.Getpid()
	if start, err := process.ProcessStartTime(os.Getpid()); err == nil {
		st.PIDStartTime = start
	}
	st.Endpoint = &process.RuntimeEndpoint{Port: port, Token: token}
	st.ContextDigests = digests
	if err := process.ClaimRuntimeState(gameID, s.configDir, st); err != nil {
		t.Fatal(err)
	}
	return digests
}

func TestConnectPersistsDeliveryVerdicts(t *testing.T) {
	cwd := t.TempDir()
	managed := map[string]string{"GABS_GAME_ID": "adventure"}
	context := map[string]string{"CONTENT_SET": "combat-pack"}

	cases := []struct {
		name        string
		observed    map[string]interface{}
		wantOverall string
	}{
		{
			name: "verified",
			observed: map[string]interface{}{
				"argv":      []string{"/opt/game/bin", "-profile", "combat"},
				"cwd":       cwd,
				"envValues": map[string]string{"GABS_GAME_ID": "adventure", "CONTENT_SET": "combat-pack"},
			},
			wantOverall: process.DeliveryVerified,
		},
		{
			name: "partial",
			observed: map[string]interface{}{
				"argv":      []string{"/opt/game/bin", "-profile", "combat"},
				"cwd":       cwd,
				"envValues": map[string]string{"GABS_GAME_ID": "adventure", "CONTENT_SET": "wrong-pack"},
			},
			wantOverall: process.DeliveryOverallPartial,
		},
		{
			name:        "old-bridge-unknown",
			observed:    nil,
			wantOverall: process.DeliveryUnknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newProfiledServer(t)
			addr, _ := fakeGABPServerWithObserved(t, "adventure", c.observed)
			parts := strings.Split(addr, ":")
			port := 0
			fmt.Sscanf(parts[len(parts)-1], "%d", &port)

			seedClaimWithDigests(t, s, "adventure", port, "launch-token", cwd, managed, context)

			raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "adventure", "timeout": 5})
			if strings.Contains(raw, `"isError":true`) {
				t.Fatalf("connect failed: %s", raw)
			}
			t.Cleanup(func() { s.CleanupGABPConnection("adventure") })

			claim, err := process.LoadRuntimeState("adventure", s.configDir)
			if err != nil || claim == nil || claim.ContextDelivery == nil {
				t.Fatalf("the delivery verdict must persist: %+v %v", claim, err)
			}
			if claim.ContextDelivery.Overall != c.wantOverall {
				t.Fatalf("overall=%s, want %s: %+v", claim.ContextDelivery.Overall, c.wantOverall, claim.ContextDelivery)
			}

			// The persisted verdict renders in games_status without
			// re-deriving (design/07).
			_, structured := callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
			cd, ok := structured["contextDelivery"].(map[string]interface{})
			if !ok || cd["overall"] != c.wantOverall {
				t.Fatalf("status must render the persisted verdict: %v", structured)
			}
		})
	}
}

func TestDeliveryVerdictSurvivesConfigEditAndRestart(t *testing.T) {
	cwd := t.TempDir()
	managed := map[string]string{"GABS_GAME_ID": "adventure"}
	context := map[string]string{"CONTENT_SET": "combat-pack"}

	s := newProfiledServer(t)
	addr, _ := fakeGABPServerWithObserved(t, "adventure", map[string]interface{}{
		"argv":      []string{"/opt/game/bin", "-profile", "combat"},
		"cwd":       cwd,
		"envValues": map[string]string{"GABS_GAME_ID": "adventure", "CONTENT_SET": "combat-pack"},
	})
	parts := strings.Split(addr, ":")
	port := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	seedClaimWithDigests(t, s, "adventure", port, "launch-token", cwd, managed, context)

	raw, _ := callTool(t, s, "games.connect", map[string]interface{}{"gameId": "adventure", "timeout": 5})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("connect failed: %s", raw)
	}
	s.CleanupGABPConnection("adventure")

	// A fresh server over the same config dir (restart): the verdict comes
	// from the claim, not from any in-memory state or current config.
	s2 := NewServerForTesting(t, s.log)
	s2.SetConfigDir(s.configDir)
	s2.RegisterGameManagementTools(profiledTestConfig(t), 0, 0)
	restore := process.SetFindProcessesByNameForTesting(func(string) ([]int, error) { return nil, nil })
	defer restore()

	_, structured := callTool(t, s2, "games.status", map[string]interface{}{"gameId": "adventure"})
	cd, ok := structured["contextDelivery"].(map[string]interface{})
	if !ok || cd["overall"] != process.DeliveryVerified {
		t.Fatalf("the verdict must survive a restart without downgrading: %v", structured)
	}
}

func TestSpawnDigestsCoverForwardEnvNames(t *testing.T) {
	t.Setenv("GABSTEST_HELPER_PROCESS", "1")
	s := newProfiledServer(t)
	raw, _ := callTool(t, s, "games.start", map[string]interface{}{
		"gameId": "adventure", "timeout": 1,
	})
	if strings.Contains(raw, `"isError":true`) {
		t.Fatalf("start failed: %s", raw)
	}
	defer callTool(t, s, "games.kill", map[string]interface{}{"gameId": "adventure"})

	claim, _ := process.LoadRuntimeState("adventure", s.configDir)
	if claim == nil || claim.ContextDigests == nil {
		t.Fatal("digests missing")
	}
	// The digest key set is exactly what the wrapper contract forwards:
	// every name must be a managed or config-declared variable, and the
	// cwd must have been canonicalizable (the resolved workingDir).
	for k := range claim.ContextDigests.ManagedEnvSHA256 {
		if !strings.HasPrefix(k, "GABS_") && !strings.HasPrefix(k, "GABP_") {
			t.Fatalf("unexpected non-managed digest key for an unprofiled context: %s", k)
		}
	}
	if len(claim.ContextDigests.ContextEnvSHA256) != 0 {
		t.Fatalf("an unprofiled launch declares no context keys: %+v", claim.ContextDigests.ContextEnvSHA256)
	}
	if claim.ContextDigests.CwdUnverifiable {
		t.Fatalf("a resolved absolute workingDir must be comparable: %+v", claim.ContextDigests)
	}
	_ = filepath.Separator
}
