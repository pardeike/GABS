package steam

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestFunctionalReadinessColdClientOpensOnceAndEventuallySucceeds(t *testing.T) {
	probes := []probeObservation{
		{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: "client library not present yet"},
		{State: probeStateNotReady, Stage: ReadinessStageIPCPipe, Detail: "pipe unavailable"},
		{State: probeStateNotReady, Stage: ReadinessStageSteamAPI, Detail: "Steamworks API initialization failed"},
		{State: probeStateNotReady, Stage: ReadinessStageAppState, Detail: "app state unavailable"},
		{State: probeStateReady, Stage: ReadinessStageAppState},
	}
	probeCalls := 0
	openCalls := 0
	result := ensureFunctionalReadinessWithin("123456", 250*time.Millisecond, readinessDependencies{
		probe: func(appID string, _ time.Duration) probeObservation {
			if appID != "123456" {
				t.Fatalf("probe app ID = %q", appID)
			}
			idx := probeCalls
			probeCalls++
			if idx >= len(probes) {
				return probes[len(probes)-1]
			}
			return probes[idx]
		},
		openClient: func() error {
			openCalls++
			return nil
		},
		pollInterval: time.Millisecond,
	})

	if !result.Ready || result.Reason != "" {
		t.Fatalf("readiness = %+v, want ready", result)
	}
	if result.Stage != ReadinessStageAppState {
		t.Fatalf("ready stage = %q, want %q", result.Stage, ReadinessStageAppState)
	}
	if probeCalls != 5 {
		t.Fatalf("probe calls = %d, want 5 fresh probes", probeCalls)
	}
	if openCalls != 1 {
		t.Fatalf("Steam open calls = %d, want exactly one", openCalls)
	}
}

func TestFunctionalReadinessDoesNotAcceptIntermediateReadyStage(t *testing.T) {
	probes := []probeObservation{
		{State: probeStateReady, Stage: ReadinessStageGlobalUser},
		{State: probeStateReady, Stage: ReadinessStageAppState},
	}
	probeCalls := 0
	result := ensureFunctionalReadinessWithin("123456", 100*time.Millisecond, readinessDependencies{
		probe: func(string, time.Duration) probeObservation {
			observation := probes[probeCalls]
			probeCalls++
			return observation
		},
		openClient:   func() error { return nil },
		pollInterval: time.Millisecond,
	})
	if !result.Ready || result.Stage != ReadinessStageAppState {
		t.Fatalf("readiness = %+v, want terminal app-state proof", result)
	}
	if probeCalls != 2 {
		t.Fatalf("probe calls = %d, want intermediate ready stage rejected", probeCalls)
	}
}

func TestFunctionalReadinessTimeoutPrefersValidNotReadyEvidence(t *testing.T) {
	result := ensureFunctionalReadinessWithin("123456", 20*time.Millisecond, readinessDependencies{
		probe: func(string, time.Duration) probeObservation {
			return probeObservation{State: probeStateNotReady, Stage: ReadinessStageSteamAPI, Detail: "Steamworks API initialization failed"}
		},
		openClient:   func() error { return nil },
		pollInterval: time.Millisecond,
	})

	if result.Ready || result.Reason != ReadinessReasonTimeout || !result.Retryable {
		t.Fatalf("readiness = %+v, want retryable timeout", result)
	}
	if result.Stage != ReadinessStageSteamAPI {
		t.Fatalf("stage = %q, want %q", result.Stage, ReadinessStageSteamAPI)
	}
	if result.Waited <= 0 || result.Timeout != 20*time.Millisecond {
		t.Fatalf("timing evidence missing: %+v", result)
	}
}

func TestFunctionalReadinessUnavailableWhenNoValidProbeRan(t *testing.T) {
	result := ensureFunctionalReadinessWithin("123456", 15*time.Millisecond, readinessDependencies{
		probe: func(string, time.Duration) probeObservation {
			return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: "helper crashed"}
		},
		openClient:   func() error { return nil },
		pollInterval: time.Millisecond,
	})

	if result.Ready || result.Reason != ReadinessReasonProbeUnavailable || result.Retryable {
		t.Fatalf("readiness = %+v, want non-retryable probe_unavailable", result)
	}
	if result.Stage != ReadinessStageClientLibrary {
		t.Fatalf("stage = %q, want client_library", result.Stage)
	}
}

func TestFunctionalReadinessNeverAcceptsProofAfterDeadline(t *testing.T) {
	result := ensureFunctionalReadinessWithin("123456", 10*time.Millisecond, readinessDependencies{
		probe: func(_ string, budget time.Duration) probeObservation {
			time.Sleep(budget + 5*time.Millisecond)
			return probeObservation{State: probeStateReady, Stage: ReadinessStageAppState}
		},
		openClient:   func() error { return nil },
		pollInterval: time.Millisecond,
	})
	if result.Ready || result.Reason != ReadinessReasonTimeout || !result.Retryable {
		t.Fatalf("late proof was accepted: %+v", result)
	}
}

func TestReadinessProbeChildEmitsBoundedJSONAndNoOtherCLIOutput(t *testing.T) {
	previous := nativeReadinessProbe
	var receivedAppID uint32
	nativeReadinessProbe = func(appID uint32) probeObservation {
		receivedAppID = appID
		return probeObservation{State: probeStateNotReady, Stage: ReadinessStageIPCPipe, Detail: "not yet"}
	}
	t.Cleanup(func() { nativeReadinessProbe = previous })

	var out bytes.Buffer
	handled, exitCode := RunReadinessProbeChild([]string{readinessProbeArgument, "123456"}, &out)
	if !handled || exitCode != 0 {
		t.Fatalf("handled=%v exit=%d", handled, exitCode)
	}
	if out.Len() > maxProbeOutputBytes {
		t.Fatalf("probe output exceeded bound: %d", out.Len())
	}
	got, err := decodeProbeObservation(out.Bytes())
	if err != nil {
		t.Fatalf("decode child output: %v (%q)", err, out.String())
	}
	if got.State != probeStateNotReady || got.Stage != ReadinessStageIPCPipe {
		t.Fatalf("decoded observation = %+v", got)
	}
	if receivedAppID != 123456 {
		t.Fatalf("native probe app ID = %d, want 123456", receivedAppID)
	}

	for _, invalid := range [][]string{
		{readinessProbeArgument},
		{readinessProbeArgument, "not-an-id"},
		{readinessProbeArgument, "0"},
		{readinessProbeArgument, "123456", "extra"},
	} {
		var invalidOut bytes.Buffer
		handled, exitCode = RunReadinessProbeChild(invalid, &invalidOut)
		if !handled || exitCode == 0 || invalidOut.Len() != 0 {
			t.Fatalf("invalid child args %q: handled=%v exit=%d output=%q", invalid, handled, exitCode, invalidOut.String())
		}
	}

	var ignored bytes.Buffer
	handled, _ = RunReadinessProbeChild([]string{"games", "status"}, &ignored)
	if handled || ignored.Len() != 0 {
		t.Fatalf("ordinary CLI invocation was intercepted: handled=%v output=%q", handled, ignored.String())
	}
}

func TestReadinessProbeEnvironmentScrubsSteamAppIdentity(t *testing.T) {
	env := scrubReadinessProbeEnvironment([]string{
		"PATH=/bin",
		"SteamAppId=123456",
		"SteamGameId=123456",
		"SteamOverlayGameId=123456",
		"OTHER=value",
	})
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"SteamAppId=", "SteamGameId=", "SteamOverlayGameId="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("probe environment leaked %s: %q", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "OTHER=value") {
		t.Fatalf("unrelated environment was not preserved: %q", joined)
	}
}

func TestReadinessAppIDMustBeNonZeroDecimalUint32(t *testing.T) {
	for _, valid := range []string{"1", "123456", "4294967295"} {
		if _, err := parseReadinessAppID(valid); err != nil {
			t.Fatalf("valid app ID %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "0", "-1", " 123456", "123456 ", "not-an-id", "4294967296"} {
		if _, err := parseReadinessAppID(invalid); err == nil {
			t.Fatalf("invalid app ID %q was accepted", invalid)
		}
	}
}

func TestDefaultReadinessProbeCommandCarriesExplicitAppID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := defaultReadinessProbeCommand(ctx, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != readinessProbeArgument || cmd.Args[2] != "123456" {
		t.Fatalf("helper argv = %q", cmd.Args)
	}
}

func TestCappedProbeBufferNeverGrowsPastLimit(t *testing.T) {
	var dst cappedProbeBuffer
	dst.limit = 8
	n, err := dst.Write([]byte("0123456789abcdef"))
	if err != nil || n != 16 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := len(dst.Bytes()); got != 8 {
		t.Fatalf("retained bytes = %d, want 8", got)
	}
}

func TestReadinessProbeProcessContainsHelperFailuresAndOutput(t *testing.T) {
	previous := newReadinessProbeCommand
	t.Cleanup(func() { newReadinessProbeCommand = previous })

	for _, tc := range []struct {
		name, scenario, detail string
		timeout                time.Duration
	}{
		{name: "crash", scenario: "crash", detail: "helper failed", timeout: 3 * time.Second},
		{name: "hang", scenario: "hang", detail: "timed out", timeout: 100 * time.Millisecond},
		{name: "malformed", scenario: "malformed", detail: "invalid readiness probe output", timeout: 3 * time.Second},
		{name: "oversized", scenario: "oversized", detail: "invalid readiness probe output", timeout: 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newReadinessProbeCommand = func(ctx context.Context, appID string) (*exec.Cmd, error) {
				if appID != "123456" {
					t.Fatalf("helper app ID = %q", appID)
				}
				return exec.CommandContext(ctx, os.Args[0], "-test.run=TestReadinessProbeProcessHelper", "--", tc.scenario), nil
			}
			observation := runReadinessProbeProcess("123456", tc.timeout)
			if observation.State != probeStateUnavailable || observation.Stage != ReadinessStageClientLibrary {
				t.Fatalf("observation = %+v", observation)
			}
			if !strings.Contains(observation.Detail, tc.detail) {
				t.Fatalf("detail %q does not contain %q", observation.Detail, tc.detail)
			}
		})
	}
}

func TestReadinessProbeProcessHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--" || i+1 >= len(os.Args) {
			continue
		}
		switch os.Args[i+1] {
		case "crash":
			os.Exit(7)
		case "hang":
			time.Sleep(time.Second)
			os.Exit(0)
		case "malformed":
			fmt.Fprint(os.Stdout, "not-json")
			os.Exit(0)
		case "oversized":
			fmt.Fprint(os.Stdout, strings.Repeat("x", maxProbeOutputBytes*2))
			os.Exit(0)
		}
	}
}
