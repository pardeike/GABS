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
		{State: probeStateReady, Stage: ReadinessStageGlobalUser},
	}
	probeCalls := 0
	openCalls := 0
	result := ensureFunctionalReadinessWithin(250*time.Millisecond, readinessDependencies{
		probe: func(time.Duration) probeObservation {
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
	if probeCalls != 3 {
		t.Fatalf("probe calls = %d, want 3 fresh probes", probeCalls)
	}
	if openCalls != 1 {
		t.Fatalf("Steam open calls = %d, want exactly one", openCalls)
	}
}

func TestFunctionalReadinessTimeoutPrefersValidNotReadyEvidence(t *testing.T) {
	result := ensureFunctionalReadinessWithin(20*time.Millisecond, readinessDependencies{
		probe: func(time.Duration) probeObservation {
			return probeObservation{State: probeStateNotReady, Stage: ReadinessStageGlobalUser, Detail: "global user unavailable"}
		},
		openClient:   func() error { return nil },
		pollInterval: time.Millisecond,
	})

	if result.Ready || result.Reason != ReadinessReasonTimeout || !result.Retryable {
		t.Fatalf("readiness = %+v, want retryable timeout", result)
	}
	if result.Stage != ReadinessStageGlobalUser {
		t.Fatalf("stage = %q, want %q", result.Stage, ReadinessStageGlobalUser)
	}
	if result.Waited <= 0 || result.Timeout != 20*time.Millisecond {
		t.Fatalf("timing evidence missing: %+v", result)
	}
}

func TestFunctionalReadinessUnavailableWhenNoValidProbeRan(t *testing.T) {
	result := ensureFunctionalReadinessWithin(15*time.Millisecond, readinessDependencies{
		probe: func(time.Duration) probeObservation {
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
	result := ensureFunctionalReadinessWithin(10*time.Millisecond, readinessDependencies{
		probe: func(budget time.Duration) probeObservation {
			time.Sleep(budget + 5*time.Millisecond)
			return probeObservation{State: probeStateReady, Stage: ReadinessStageGlobalUser}
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
	nativeReadinessProbe = func() probeObservation {
		return probeObservation{State: probeStateNotReady, Stage: ReadinessStageIPCPipe, Detail: "not yet"}
	}
	t.Cleanup(func() { nativeReadinessProbe = previous })

	var out bytes.Buffer
	handled, exitCode := RunReadinessProbeChild([]string{readinessProbeArgument}, &out)
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
			newReadinessProbeCommand = func(ctx context.Context) (*exec.Cmd, error) {
				return exec.CommandContext(ctx, os.Args[0], "-test.run=TestReadinessProbeProcessHelper", "--", tc.scenario), nil
			}
			observation := runReadinessProbeProcess(tc.timeout)
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
