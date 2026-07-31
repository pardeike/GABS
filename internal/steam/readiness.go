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

// ReadinessStage identifies the furthest app-neutral Steam client interface
// stage observed by the probe.
type ReadinessStage string

const (
	ReadinessStageClientLibrary ReadinessStage = "client_library"
	ReadinessStageIPCPipe       ReadinessStage = "ipc_pipe"
	ReadinessStageGlobalUser    ReadinessStage = "global_user"
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
// app-neutral IPC/global-user interface within timeout. On production macOS
// each probe is a hidden child invocation of the current GABS executable.
func EnsureFunctionalReadinessWithin(timeout time.Duration) ReadinessResult {
	return functionalReadinessProbe(timeout)
}

// SetFunctionalReadinessForTesting overrides the platform gate and readiness
// implementation. Tests using this process-global seam must not run in
// parallel, matching the older Steam client-control seam in steam.go.
func SetFunctionalReadinessForTesting(supported bool, probe func(time.Duration) ReadinessResult) func() {
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
// CLI parsing. The child emits exactly one small JSON object and exits; it does
// not initialize any game API or derive an app identity.
func RunReadinessProbeChild(args []string, out io.Writer) (handled bool, exitCode int) {
	if len(args) != 1 || args[0] != readinessProbeArgument {
		return false, 0
	}
	observation := nativeReadinessProbe()
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
	probe        func(time.Duration) probeObservation
	openClient   func() error
	pollInterval time.Duration
}

func defaultEnsureFunctionalReadinessWithin(timeout time.Duration) ReadinessResult {
	return ensureFunctionalReadinessWithin(timeout, readinessDependencies{
		probe:        runReadinessProbeProcess,
		openClient:   openSteamClient,
		pollInterval: readinessPollInterval,
	})
}

func ensureFunctionalReadinessWithin(timeout time.Duration, deps readinessDependencies) ReadinessResult {
	startedAt := time.Now()
	result := ReadinessResult{
		Reason:  ReadinessReasonProbeUnavailable,
		Stage:   ReadinessStageClientLibrary,
		Timeout: timeout,
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
		observation := deps.probe(probeBudget)
		probeFinishedBeforeDeadline := time.Now().Before(deadline)
		if stageRank(observation.Stage) >= stageRank(result.Stage) {
			result.Stage = observation.Stage
			if observation.Detail != "" {
				result.Detail = truncateProbeDetail(observation.Detail)
			}
		}
		switch observation.State {
		case probeStateReady:
			if probeFinishedBeforeDeadline {
				result.Ready = true
				result.Reason = ""
				result.Retryable = false
				result.Stage = ReadinessStageGlobalUser
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

func runReadinessProbeProcess(timeout time.Duration) probeObservation {
	if timeout <= 0 {
		return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: "probe deadline elapsed"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd, err := newReadinessProbeCommand(ctx)
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

func defaultReadinessProbeCommand(ctx context.Context) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate GABS executable: %w", err)
	}
	return exec.CommandContext(ctx, executable, readinessProbeArgument), nil
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
