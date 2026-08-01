package mcp

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// TestClaimHistorySuccessSnapshotNeverLeaks locks the new sensitive-data
// surface created by P1-2: HistorySuccess.Snapshot may hold ConfigEnv VALUES
// and is now serialized into the 0600 runtime.json. It must never reach an
// MCP result (mirroring round 10's lastGood-snapshot privacy guarantee).
func TestClaimHistorySuccessSnapshotNeverLeaks(t *testing.T) {
	s := newProfiledServer(t)
	spec := process.LaunchSpec{GameId: "adventure", Mode: "DirectPath", PathOrId: "/opt/game", Profile: "vanilla"}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusRunning)
	st.Phase = process.PhaseActive
	st.SpawnState = process.SpawnStateSpawned
	st.GamePID = os.Getpid()
	if start, err := process.ProcessStartTime(os.Getpid()); err == nil {
		st.PIDStartTime = start
	}
	st.HistoryContextHash = "sha256:ctx"
	st.HistorySuccess = &process.HistorySuccessIdentity{
		Snapshot: process.ContextSnapshot{Target: "SECRET-TARGET", ConfigEnv: map[string]string{"K": "SECRET-VALUE"}},
	}
	if err := process.ClaimRuntimeState("adventure", s.configDir, st); err != nil {
		t.Fatal(err)
	}

	for _, tool := range []string{"games.status", "games.show"} {
		raw, _ := callTool(t, s, tool, map[string]interface{}{"gameId": "adventure"})
		if strings.Contains(raw, "SECRET-VALUE") || strings.Contains(raw, "SECRET-TARGET") || strings.Contains(raw, "historySuccess") {
			t.Fatalf("%s leaked the pinned claim snapshot: %s", tool, raw)
		}
	}
}

// TestPromotionResetsRecordedUnobservedFailure closes the P2-3↔P1-2 loop: an
// unobserved attempt records consecutiveFailures, and a later passive Stage 4
// promotion resets it while crediting the start.
func TestPromotionResetsRecordedUnobservedFailure(t *testing.T) {
	s := newProfiledServer(t)
	const hash = "sha256:ctx"

	// A prior unobserved failure recorded for this context/profile.
	lid := seedTrackClaim(t, "adventure", s.configDir)
	if err := process.RecordFailure("adventure", s.configDir, lid, "vanilla", hash, "unobserved", process.CauseConfig, nil, timeNow()); err != nil {
		t.Fatal(err)
	}
	_ = process.RemoveRuntimeState("adventure", s.configDir)
	if e := mustHistory(t, s, "adventure").Profiles["vanilla"]; e == nil || e.ConsecutiveFailures != 1 {
		t.Fatalf("setup: expected one recorded failure, got %+v", e)
	}

	// A completed-unobserved claim for the same context, promoted by status.
	seedUnobservedClaim(t, s, "adventure", "vanilla", hash, nil)
	callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})

	e := mustHistory(t, s, "adventure").Profiles["vanilla"]
	if e == nil || e.WorkloadStarts != 1 || e.ConsecutiveFailures != 0 {
		t.Fatalf("promotion must credit the start AND reset the recorded failures: %+v", e)
	}
}

func mustHistory(t *testing.T, s *Server, gameID string) *process.GameHistory {
	t.Helper()
	h, err := process.LoadHistory(gameID, s.configDir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// seedUnobservedClaim publishes a completed-unobserved claim (phase starting,
// no operation, spawned, PID alive) carrying the pinned history identity — the
// shape a later Stage 4 promotion must credit (round 11 P1-2).
func seedUnobservedClaim(t *testing.T, s *Server, gameID, profile, hash string, endpoint *process.RuntimeEndpoint) {
	t.Helper()
	spec := process.LaunchSpec{GameId: gameID, Mode: "DirectPath", PathOrId: "/opt/game", Profile: profile}
	st := process.NewRuntimeState(spec, process.RuntimeStateStatusStarting)
	st.Phase = process.PhaseStarting
	st.SpawnState = process.SpawnStateSpawned
	st.Operation = nil
	st.GamePID = os.Getpid()
	if start, err := process.ProcessStartTime(os.Getpid()); err == nil {
		st.PIDStartTime = start
	}
	st.HistoryContextHash = hash
	st.HistorySuccess = &process.HistorySuccessIdentity{Snapshot: process.ContextSnapshot{Target: "/opt/game"}}
	st.Endpoint = endpoint
	if err := process.ClaimRuntimeState(gameID, s.configDir, st); err != nil {
		t.Fatal(err)
	}
}

func TestPassiveStatusPromotionCreditsWorkloadStart(t *testing.T) {
	s := newProfiledServer(t)
	seedUnobservedClaim(t, s, "adventure", "vanilla", "sha256:ctx", nil)

	// A status observation that finds the workload running promotes the
	// completed-unobserved claim to active — a Stage 4 verification that must
	// credit workloadStarts++ (previously only the synchronous path did).
	callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})

	h, _ := process.LoadHistory("adventure", s.configDir)
	e := h.Profiles["vanilla"]
	if e == nil || e.WorkloadStarts != 1 {
		t.Fatalf("passive status promotion must credit workloadStarts++: %+v", e)
	}

	// Exactly once: the claim is now active, so a second status observation
	// does not re-promote and does not re-credit.
	callTool(t, s, "games.status", map[string]interface{}{"gameId": "adventure"})
	h, _ = process.LoadHistory("adventure", s.configDir)
	if e := h.Profiles["vanilla"]; e == nil || e.WorkloadStarts != 1 {
		t.Fatalf("Stage 4 credit must fire exactly once: %+v", e)
	}
}

func TestAttachmentPromotionCreditsWorkloadStartAndBridgeConnect(t *testing.T) {
	s := newProfiledServer(t)
	seedUnobservedClaim(t, s, "adventure", "vanilla", "sha256:ctx",
		&process.RuntimeEndpoint{Port: 12345, Token: "tok"})

	// A bridge attachment on the completed-unobserved claim is BOTH the Stage
	// 5 connect (bridgeConnects++) and the Stage 4 verification that promotes
	// it to active (workloadStarts++). Before P1-2 only bridgeConnects moved,
	// producing "bridge connected 1× but no successful starts".
	if _, err := attachForTest(s, "adventure", 12345, "tok", func() bool { return true }); err != nil {
		t.Fatalf("attach: %v", err)
	}

	h, _ := process.LoadHistory("adventure", s.configDir)
	e := h.Profiles["vanilla"]
	if e == nil || e.WorkloadStarts != 1 {
		t.Fatalf("attachment promotion must credit workloadStarts++: %+v", e)
	}
	if e.BridgeConnects != 1 {
		t.Fatalf("attachment must also credit bridgeConnects++: %+v", e)
	}
}

// steamishUnobservedServer configures a SteamAppId game whose launcher is a
// harmless sleep and whose process finder returns unknown evidence, so a start
// reliably produces the unobserved outcome.
func steamishUnobservedServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	s := NewServerForTesting(t, util.NewLogger("error"))
	s.SetConfigDir(dir)
	s.RegisterGameManagementTools(&config.GamesConfig{
		Version: "1.0",
		Games: map[string]config.GameConfig{
			"steamish": {ID: "steamish", Name: "S", LaunchMode: "SteamAppId", Target: "123456", StopProcessName: "steamish-workload"},
		},
		Timeouts: &config.TimeoutsConfig{Startup: &config.StartupTimeoutsConfig{ProcessStartSeconds: 1}},
	}, 0, 0)
	t.Cleanup(process.SetLaunchCommandFactoriesForTesting(
		func(target string) (string, []string) { return "/bin/sleep", []string{"5"} }, nil))
	t.Cleanup(process.SetFindProcessesByNameForTesting(
		func(name string) ([]int, error) { return nil, &fakeScanError{} }))
	return s
}

func TestUnobservedRecordsNeverProvenAsConfigFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep binary as the launcher stand-in")
	}
	s := steamishUnobservedServer(t)

	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "steamish", "timeout": 1})
	if structured["code"] != "unobserved" {
		t.Fatalf("expected unobserved, got %v", structured["code"])
	}
	if structured["causeClass"] != process.CauseConfig {
		t.Fatalf("a never-proven unobserved is config, got %#v", structured["causeClass"])
	}
	// The accepted attempt is recorded as a failure (design/20:206; P2-3).
	h, _ := process.LoadHistory("steamish", s.configDir)
	e := h.Profiles[""]
	if e == nil || e.ConsecutiveFailures != 1 || e.LastFailure == nil || e.LastFailure.Outcome != "unobserved" {
		t.Fatalf("an unobserved accepted attempt must be recorded: %+v", e)
	}
	if e.LastFailure.Class != process.CauseConfig {
		t.Fatalf("a never-proven unobserved is recorded as config: %+v", e.LastFailure)
	}
}

func TestUnobservedHistoryWriteFailureDoesNotCommitCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep binary as the launcher stand-in")
	}
	s := steamishUnobservedServer(t)
	restore := process.SetSaveHistoryFailHookForTesting(func() error { return errors.New("history disk full") })
	t.Cleanup(restore)

	raw, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "steamish", "timeout": 1})
	if structured["code"] == "unobserved" {
		t.Fatalf("unobserved must not commit after its failure-history write was lost: %s", raw)
	}
	if !strings.Contains(raw, "history disk full") {
		t.Fatalf("the history failure must reach the caller: %s", raw)
	}
	claim, err := process.LoadRuntimeState("steamish", s.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil || claim.Phase != process.PhaseStarting || claim.Operation == nil {
		t.Fatalf("the unobserved transition must remain fenced and retryable: %+v", claim)
	}
}

func TestUnobservedRecordsProvenAsEnvironmentFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep binary as the launcher stand-in")
	}
	s := steamishUnobservedServer(t)

	// Establish proof for the context this start will resolve to.
	snap, _ := s.currentSnapshot()
	base, berr := launch.ResolveBaseContext(snap, "steamish", "", launch.Options{
		InheritedEnv: os.Environ(), CaseInsensitiveEnv: runtime.GOOS == "windows",
	})
	if berr != nil {
		t.Fatalf("resolve base: %v", berr)
	}
	hash := process.ContextHash(base)
	lid := seedTrackClaim(t, "steamish", s.configDir)
	if err := process.RecordWorkloadStart("steamish", s.configDir, lid, "", hash,
		process.ContextSnapshot{}, process.SuccessBucket{}, timeNow()); err != nil {
		t.Fatal(err)
	}
	_ = process.RemoveRuntimeState("steamish", s.configDir)

	_, structured := callTool(t, s, "games.start", map[string]interface{}{"gameId": "steamish", "timeout": 1})
	if structured["code"] != "unobserved" {
		t.Fatalf("expected unobserved, got %v", structured["code"])
	}
	if structured["causeClass"] != process.CauseEnvironment {
		t.Fatalf("a proven unobserved is environment, got %#v", structured["causeClass"])
	}
	h, _ := process.LoadHistory("steamish", s.configDir)
	e := h.Profiles[""]
	// Proof survives; the failure is recorded alongside it.
	if e == nil || e.WorkloadStarts != 1 || e.ConsecutiveFailures != 1 {
		t.Fatalf("a proven context keeps its proof and records the failure: %+v", e)
	}
	if e.LastFailure == nil || e.LastFailure.Class != process.CauseEnvironment {
		t.Fatalf("a proven unobserved is recorded as environment: %+v", e.LastFailure)
	}
}
