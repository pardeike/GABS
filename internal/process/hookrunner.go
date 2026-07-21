package process

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// Status hook verdicts (design/04): unknown never cleans state and never
// authorizes a start.
const (
	StatusRunning = "running"
	StatusStopped = "stopped"
	StatusUnknown = "unknown"
)

// hookOutputCap bounds captured output per stream; the tail is kept because
// the end of the output is the evidence (design/01).
const hookOutputCap = 16 * 1024

// hookPipeGrace bounds how long Wait may block on I/O pipes held open by a
// process that outlived the direct child (a detached grandchild).
const hookPipeGrace = 2 * time.Second

// HookResult carries the observable facts of one hook execution. Stderr
// tails and exit codes are the debugging signal and are shown to callers.
type HookResult struct {
	ExitCode        int // -1 when the process did not exit normally
	TimedOut        bool
	ExecError       error
	StdoutTail      string
	StderrTail      string
	StdoutTruncated bool
	StderrTruncated bool
	TreeKillWarning bool
	Duration        time.Duration
}

// RunStatusHook executes a resolved status hook and classifies its exit per
// the contract: configured running/stopped sets; anything else — timeout,
// exec failure, unclassified exit — is unknown, never stopped.
func RunStatusHook(h *launch.ResolvedHook, gameID, profile string) (string, HookResult) {
	res := runHook(h, gameID, profile)
	if res.ExecError != nil || res.TimedOut || res.ExitCode < 0 {
		return StatusUnknown, res
	}
	for _, c := range h.RunningExitCodes {
		if res.ExitCode == c {
			return StatusRunning, res
		}
	}
	for _, c := range h.StoppedExitCodes {
		if res.ExitCode == c {
			return StatusStopped, res
		}
	}
	return StatusUnknown, res
}

// RunActionHook executes a resolved stop/kill hook. Success means the
// command accepted the action (exit 0) — it is provisional evidence only;
// termination is verified separately (design/06).
func RunActionHook(h *launch.ResolvedHook, gameID, profile string) (bool, HookResult) {
	res := runHook(h, gameID, profile)
	return res.ExecError == nil && !res.TimedOut && res.ExitCode == 0, res
}

func runHook(h *launch.ResolvedHook, gameID, profile string) HookResult {
	var res HookResult
	timeout := time.Duration(h.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	cmd := exec.Command(h.Command, h.Args...)
	if h.WorkingDir != "" {
		cmd.Dir = h.WorkingDir
	}
	cmd.Env = hookEnvironment(h, gameID, profile)

	stdout := newTailBuffer(hookOutputCap)
	stderr := newTailBuffer(hookOutputCap)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// A detached grandchild can inherit the pipes and outlive the tree kill
	// (the documented residual-straggler case); without this, Wait would
	// block on it indefinitely.
	cmd.WaitDelay = hookPipeGrace

	configureHookProcessGroup(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.ExecError = err
		res.ExitCode = -1
		res.Duration = time.Since(start)
		return res
	}
	tree, treeErr := newHookTree(cmd)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(timeout):
		// Kill the hook's whole process tree, not just the direct child — a
		// grandchild that survives could act late against a new workload.
		// A grandchild that left the group/Job can still escape; the warning
		// is recorded and the pre-start probes are the backstop (design/01).
		res.TimedOut = true
		res.TreeKillWarning = true
		if treeErr != nil || tree.kill() != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-done // direct child must be reaped before we report
	}
	if tree != nil {
		tree.close()
	}

	res.Duration = time.Since(start)
	res.StdoutTail, res.StdoutTruncated = stdout.tail()
	res.StderrTail, res.StderrTruncated = stderr.tail()

	if res.TimedOut {
		res.ExitCode = -1
		return res
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
			return res
		}
		res.ExecError = waitErr
		res.ExitCode = -1
		return res
	}
	res.ExitCode = 0
	return res
}

// hookEnvironment builds the hook env per design/01: sanitized inherited
// environment (inherited GABS_*/GABP_* removed — hooks never receive GABP
// secrets) → hook unsetEnv → hook env → GABS_GAME_ID + GABS_PROFILE.
func hookEnvironment(h *launch.ResolvedHook, gameID, profile string) []string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "GABS_") || strings.HasPrefix(upper, "GABP_") {
			continue
		}
		env[k] = kv[i+1:]
	}
	for _, k := range h.UnsetEnv {
		delete(env, k)
	}
	for k, v := range h.Env {
		env[k] = v
	}
	env["GABS_GAME_ID"] = gameID
	if profile != "" {
		env["GABS_PROFILE"] = profile
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// tailBuffer keeps the last cap bytes written.
type tailBuffer struct {
	cap       int
	buf       []byte
	truncated bool
}

func newTailBuffer(cap int) *tailBuffer { return &tailBuffer{cap: cap} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.cap {
		b.buf = b.buf[len(b.buf)-b.cap:]
		b.truncated = true
	}
	return len(p), nil
}

func (b *tailBuffer) tail() (string, bool) { return string(b.buf), b.truncated }
