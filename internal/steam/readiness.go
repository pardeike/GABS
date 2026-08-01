package steam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ReadinessReason is the stable explanation returned when functional Steam
// readiness could not be proven before a SteamManaged spawn.
type ReadinessReason string

const (
	ReadinessReasonTimeout          ReadinessReason = "readiness_timeout"
	ReadinessReasonProbeUnavailable ReadinessReason = "probe_unavailable"
)

// ReadinessStage identifies the furthest Steam client interface stage observed
// by the app-specific probe.
type ReadinessStage string

const (
	ReadinessStageClientLibrary ReadinessStage = "client_library"
	ReadinessStageIPCPipe       ReadinessStage = "ipc_pipe"
	ReadinessStageGlobalUser    ReadinessStage = "global_user"
	ReadinessStageSteamAPI      ReadinessStage = "steam_api"
	ReadinessStageAppInterface  ReadinessStage = "app_interface"
	ReadinessStageAppState      ReadinessStage = "app_state"
)

// ReadinessResult is the complete parent-side readiness verdict. Durations are
// retained as time.Duration internally and rendered as milliseconds by the
// lifecycle frontends.
type ReadinessResult struct {
	Ready     bool
	Reason    ReadinessReason
	Stage     ReadinessStage
	Detail    string
	Retryable bool
	Waited    time.Duration
	Timeout   time.Duration
}

type probeState string

const (
	probeStateReady       probeState = "ready"
	probeStateNotReady    probeState = "not_ready"
	probeStateUnavailable probeState = "unavailable"
)

type probeObservation struct {
	State  probeState     `json:"state"`
	Stage  ReadinessStage `json:"stage"`
	Detail string         `json:"detail,omitempty"`
}

const (
	readinessProbeArgument = "__gabs_internal_steam_readiness_probe"
	maxProbeOutputBytes    = 8 * 1024
	maxProbeDetailBytes    = 2 * 1024
	readinessProbeMaxRun   = 2 * time.Second
	readinessPollInterval  = 250 * time.Millisecond
)

var (
	functionalReadinessSupported = func() bool { return runtime.GOOS == "darwin" }
	functionalReadinessProbe     = defaultEnsureFunctionalReadinessWithin
	nativeReadinessProbe         = defaultNativeReadinessProbe
	newReadinessProbeCommand     = defaultReadinessProbeCommand
)

// FunctionalReadinessSupported reports whether this platform has the strict
// pre-spawn SteamManaged readiness gate. It is intentionally false outside
// macOS so existing Windows/Linux launch behavior is unchanged.
func FunctionalReadinessSupported() bool { return functionalReadinessSupported() }

// EnsureFunctionalReadinessWithin proves the installed Steam client's
// IPC/global-user interface, the configured app's subscription/install state,
// and a balanced process-local Steamworks API initialization within timeout.
// On production macOS each probe is a hidden child invocation of the current
// GABS executable.
func EnsureFunctionalReadinessWithin(appID string, timeout time.Duration) ReadinessResult {
	return functionalReadinessProbe(appID, timeout)
}

// SetFunctionalReadinessForTesting overrides the platform gate and readiness
// implementation. Tests using this process-global seam must not run in
// parallel, matching the older Steam client-control seam in steam.go.
func SetFunctionalReadinessForTesting(supported bool, probe func(string, time.Duration) ReadinessResult) func() {
	previousSupported := functionalReadinessSupported
	previousProbe := functionalReadinessProbe
	functionalReadinessSupported = func() bool { return supported }
	if probe != nil {
		functionalReadinessProbe = probe
	}
	return func() {
		functionalReadinessSupported = previousSupported
		functionalReadinessProbe = previousProbe
	}
}

// RunReadinessProbeChild handles the private child invocation before ordinary
// CLI parsing. The child emits exactly one small JSON object and exits; it uses
// only the explicit configured App ID and balances its process-local Steamworks
// API initialization with shutdown before returning.
func RunReadinessProbeChild(args []string, out io.Writer) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != readinessProbeArgument {
		return false, 0
	}
	if len(args) != 2 {
		return true, 2
	}
	appID, err := parseReadinessAppID(args[1])
	if err != nil {
		return true, 2
	}
	observation := nativeReadinessProbe(appID)
	observation.Detail = truncateProbeDetail(observation.Detail)
	data, err := json.Marshal(observation)
	if err != nil {
		return true, 1
	}
	data = append(data, '\n')
	if len(data) > maxProbeOutputBytes {
		return true, 1
	}
	if _, err := out.Write(data); err != nil {
		return true, 1
	}
	return true, 0
}

type readinessDependencies struct {
	probe        func(string, time.Duration) probeObservation
	openClient   func() error
	pollInterval time.Duration
}

func defaultEnsureFunctionalReadinessWithin(appID string, timeout time.Duration) ReadinessResult {
	return ensureFunctionalReadinessWithin(appID, timeout, readinessDependencies{
		probe:        runReadinessProbeProcess,
		openClient:   openSteamClient,
		pollInterval: readinessPollInterval,
	})
}

func ensureFunctionalReadinessWithin(appID string, timeout time.Duration, deps readinessDependencies) ReadinessResult {
	startedAt := time.Now()
	result := ReadinessResult{
		Reason:  ReadinessReasonProbeUnavailable,
		Stage:   ReadinessStageClientLibrary,
		Timeout: timeout,
	}
	if _, err := parseReadinessAppID(appID); err != nil {
		result.Detail = err.Error()
		return result
	}
	if timeout <= 0 || deps.probe == nil {
		result.Detail = "no time budget or probe implementation is available"
		return result
	}
	if deps.pollInterval <= 0 {
		deps.pollInterval = readinessPollInterval
	}
	deadline := startedAt.Add(timeout)
	validNotReady := false
	opened := false

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		probeBudget := readinessProbeMaxRun
		if remaining < probeBudget {
			probeBudget = remaining
		}
		observation := deps.probe(appID, probeBudget)
		probeFinishedBeforeDeadline := time.Now().Before(deadline)
		if stageRank(observation.Stage) >= stageRank(result.Stage) {
			result.Stage = observation.Stage
			if observation.Detail != "" {
				result.Detail = truncateProbeDetail(observation.Detail)
			}
		}
		switch observation.State {
		case probeStateReady:
			// Only the terminal app-specific stage is sufficient. A lower-level
			// interface may report ready while Steam is still loading app state.
			if observation.Stage == ReadinessStageAppState && probeFinishedBeforeDeadline {
				result.Ready = true
				result.Reason = ""
				result.Detail = ""
				result.Retryable = false
				result.Stage = ReadinessStageAppState
				result.Waited = time.Since(startedAt)
				return result
			}
			// The interface became usable only after the caller's hard bound.
			// Preserve that as valid client evidence, but do not spawn late.
			validNotReady = true
		case probeStateNotReady:
			validNotReady = true
		}

		if !opened {
			opened = true
			if deps.openClient != nil {
				if err := deps.openClient(); err != nil && result.Detail == "" {
					result.Detail = truncateProbeDetail(fmt.Sprintf("Steam could not be opened: %v", err))
				}
			}
		}

		remaining = time.Until(deadline)
		if remaining <= 0 {
			break
		}
		delay := deps.pollInterval
		if delay > remaining {
			delay = remaining
		}
		time.Sleep(delay)
	}

	result.Waited = time.Since(startedAt)
	if validNotReady {
		result.Reason = ReadinessReasonTimeout
		result.Retryable = true
	} else {
		result.Reason = ReadinessReasonProbeUnavailable
		result.Retryable = false
	}
	return result
}

func stageRank(stage ReadinessStage) int {
	switch stage {
	case ReadinessStageAppState:
		return 6
	case ReadinessStageAppInterface:
		return 5
	case ReadinessStageSteamAPI:
		return 4
	case ReadinessStageGlobalUser:
		return 3
	case ReadinessStageIPCPipe:
		return 2
	default:
		return 1
	}
}

func openSteamClient() error {
	cmdName, args, err := startClientCommand()
	if err != nil {
		return err
	}
	cmd := exec.Command(cmdName, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start Steam client: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func runReadinessProbeProcess(appID string, timeout time.Duration) probeObservation {
	if timeout <= 0 {
		return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: "probe deadline elapsed"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd, err := newReadinessProbeCommand(ctx, appID)
	if err != nil {
		return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: truncateProbeDetail(err.Error())}
	}
	cmd.Env = scrubReadinessProbeEnvironment(os.Environ())
	stdout := cappedProbeBuffer{limit: maxProbeOutputBytes}
	stderr := cappedProbeBuffer{limit: maxProbeOutputBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: "readiness probe helper timed out"}
	}
	if err != nil {
		detail := strings.TrimSpace(string(stderr.Bytes()))
		if detail == "" {
			detail = err.Error()
		}
		return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: truncateProbeDetail("readiness probe helper failed: " + detail)}
	}
	observation, err := decodeProbeObservation(stdout.Bytes())
	if err != nil {
		return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: truncateProbeDetail(fmt.Sprintf("invalid readiness probe output: %v", err))}
	}
	return observation
}

func defaultReadinessProbeCommand(ctx context.Context, appID string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate GABS executable: %w", err)
	}
	return exec.CommandContext(ctx, executable, readinessProbeArgument, appID), nil
}

func parseReadinessAppID(raw string) (uint32, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return 0, fmt.Errorf("configured Steam app ID is not a non-zero decimal uint32")
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("configured Steam app ID is not a non-zero decimal uint32")
	}
	return uint32(value), nil
}

func decodeProbeObservation(data []byte) (probeObservation, error) {
	if len(data) == 0 || len(data) > maxProbeOutputBytes {
		return probeObservation{}, fmt.Errorf("output size %d is outside the accepted range", len(data))
	}
	var observation probeObservation
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&observation); err != nil {
		return probeObservation{}, err
	}
	var trailing interface{}
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return probeObservation{}, fmt.Errorf("more than one JSON value")
		}
		return probeObservation{}, fmt.Errorf("trailing probe output: %w", err)
	}
	if observation.State != probeStateReady && observation.State != probeStateNotReady && observation.State != probeStateUnavailable {
		return probeObservation{}, fmt.Errorf("unknown probe state %q", observation.State)
	}
	if stageRank(observation.Stage) == 1 && observation.Stage != ReadinessStageClientLibrary {
		return probeObservation{}, fmt.Errorf("unknown readiness stage %q", observation.Stage)
	}
	observation.Detail = truncateProbeDetail(observation.Detail)
	return observation, nil
}

func scrubReadinessProbeEnvironment(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		switch strings.ToLower(name) {
		case "steamappid", "steamgameid", "steamoverlaygameid":
			continue
		default:
			out = append(out, entry)
		}
	}
	return out
}

func truncateProbeDetail(detail string) string {
	if len(detail) <= maxProbeDetailBytes {
		return detail
	}
	return detail[:maxProbeDetailBytes]
}

type cappedProbeBuffer struct {
	limit int
	data  []byte
}

func (b *cappedProbeBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - len(b.data)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		b.data = append(b.data, p[:remaining]...)
	}
	return written, nil
}

func (b *cappedProbeBuffer) Bytes() []byte { return b.data }
