package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/steam"
)

var (
	steamLaunchCommandFactory = defaultSteamLaunchCommandFactory
	epicLaunchCommandFactory  = defaultEpicLaunchCommandFactory
	findProcessesByNameFunc   = findProcessesByName
)

type LaunchSpec struct {
	GameId          string
	Mode            string // DirectPath|SteamAppId|SteamManaged|EpicAppId|CustomCommand
	PathOrId        string
	Args            []string
	WorkingDir      string
	StopProcessName string // Optional process name for stopping the game

	// Resolved launch-profile context (design/02-launch-resolution.md).
	// Env nil = legacy behavior (inherit os.Environ). Non-nil = the
	// resolver's config-layer environment; the controller adds only the
	// managed layer on top.
	Profile        string
	Env            map[string]string
	ContextEnvKeys []string
	AbsentEnvNames []string

	// RuntimeDir is the per-game runtime directory (bridge.json,
	// launch.log). Empty falls back to the default ~/.gabs/<gameId>.
	RuntimeDir string

	// AppliedInputs are the applied launch-input names (never values) and
	// ConfigRevision the snapshot pinned at resolution — both persisted
	// into the runtime claim.
	AppliedInputs  []string
	ConfigRevision string

	// Lifecycle is the resolved hook snapshot, persisted into the runtime
	// claim so stop/status never consult mutable config (design/07).
	Lifecycle *launch.ResolvedLifecycle
}

type BridgeInfo struct {
	Port  int
	Token string
}

// Controller implements a stateless approach to process management
// It queries the actual system state rather than maintaining internal state
type Controller struct {
	spec       LaunchSpec
	cmd        *exec.Cmd
	bridgeInfo *BridgeInfo
	waitOnce   sync.Once // guards c.cmd.Wait() to prevent multiple calls
	waitDone   chan struct{}
}

// Configure sets up the controller with the given launch specification
func (c *Controller) Configure(spec LaunchSpec) error {
	if spec.GameId == "" {
		return &ProcessError{
			Type:    ProcessErrorTypeConfiguration,
			Context: "GameId is required",
			Err:     fmt.Errorf("GameId cannot be empty"),
		}
	}

	switch spec.Mode {
	case "DirectPath", "":
		if spec.PathOrId == "" {
			return &ProcessError{
				Type:    ProcessErrorTypeConfiguration,
				Context: fmt.Sprintf("PathOrId is required for mode %s", spec.Mode),
				Err:     fmt.Errorf("PathOrId cannot be empty for DirectPath mode"),
			}
		}
	case "SteamAppId", "SteamManaged", "EpicAppId", "CustomCommand":
		if spec.PathOrId == "" {
			return &ProcessError{
				Type:    ProcessErrorTypeConfiguration,
				Context: fmt.Sprintf("PathOrId is required for mode %s", spec.Mode),
				Err:     fmt.Errorf("PathOrId cannot be empty for %s mode", spec.Mode),
			}
		}
	default:
		return &ProcessError{
			Type:    ProcessErrorTypeConfiguration,
			Context: fmt.Sprintf("unsupported launch mode: %s", spec.Mode),
			Err:     fmt.Errorf("unsupported launch mode: %s", spec.Mode),
		}
	}

	c.spec = spec
	return nil
}

// SetBridgeInfo sets the bridge connection information
func (c *Controller) SetBridgeInfo(port int, token string) {
	c.bridgeInfo = &BridgeInfo{
		Port:  port,
		Token: token,
	}
}

// Start launches the process and waits for verification
func (c *Controller) Start() error {
	// Prepare command based on launch mode
	var cmdName string
	var cmdArgs []string

	switch c.spec.Mode {
	case "DirectPath", "":
		cmdName = c.spec.PathOrId
		cmdArgs = c.spec.Args
	case "SteamAppId":
		cmdName, cmdArgs = steamLaunchCommandFactory(c.spec.PathOrId)
	case "SteamManaged":
		app, err := steam.ResolveApp(c.spec.PathOrId)
		if err != nil {
			return &ProcessError{
				Type:    ProcessErrorTypeConfiguration,
				Context: fmt.Sprintf("failed to resolve Steam app %s", c.spec.PathOrId),
				Err:     err,
			}
		}
		if err := steam.EnsureClientRunning(); err != nil {
			return &ProcessError{
				Type:    ProcessErrorTypeStart,
				Context: fmt.Sprintf("failed to prepare Steam client for %s", c.spec.GameId),
				Err:     err,
			}
		}
		if err := steam.EnsureAppIDFile(app); err != nil {
			return &ProcessError{
				Type:    ProcessErrorTypeConfiguration,
				Context: fmt.Sprintf("failed to prepare Steam app id file for %s", c.spec.GameId),
				Err:     err,
			}
		}
		cmdName = app.Executable
		cmdArgs = c.spec.Args
		if c.spec.WorkingDir == "" {
			c.spec.WorkingDir = app.WorkingDir
		}
	case "EpicAppId":
		cmdName, cmdArgs = epicLaunchCommandFactory(c.spec.PathOrId)
	case "CustomCommand":
		cmdName = c.spec.PathOrId
		cmdArgs = c.spec.Args
	default:
		return &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: fmt.Sprintf("unsupported launch mode: %s", c.spec.Mode),
			Err:     fmt.Errorf("unsupported launch mode: %s", c.spec.Mode),
		}
	}

	// Create command
	c.cmd = exec.Command(cmdName, cmdArgs...)
	if c.spec.WorkingDir != "" {
		c.cmd.Dir = c.spec.WorkingDir
	}

	// Set up environment variables
	c.setupEnvironment()

	// Child stdout/stderr go to a per-launch log file whose descriptors
	// stay valid after any GABS process exits — never parent-owned pipes,
	// which would turn a CLI exit into EPIPE for a logging game
	// (design/05-start-pipeline.md, Stage 3). Truncated at each spawn; the
	// capped tail is the "why did it die" evidence in failure results.
	if err := os.MkdirAll(c.runtimeDir(), 0o700); err != nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: fmt.Sprintf("cannot create runtime directory for %s launch log", c.spec.GameId),
			Err:     err,
		}
	}
	logFile, err := os.OpenFile(c.launchLogPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		// The per-launch log is the mandated evidence channel (design/05
		// Stage 3): starting a child without it silently discards the
		// why-did-it-die evidence.
		return &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: fmt.Sprintf("cannot open per-launch log for %s", c.spec.GameId),
			Err:     err,
		}
	}
	c.cmd.Stdout = logFile
	c.cmd.Stderr = logFile
	defer logFile.Close() // the child keeps its own descriptor

	// Start the process
	if err := c.cmd.Start(); err != nil {
		ctxMsg := fmt.Sprintf("failed to start %s (mode: %s, target: %s)", c.spec.GameId, c.spec.Mode, c.spec.PathOrId)
		if hint := startErrorHintFor(err, runtime.GOOS); hint != "" {
			ctxMsg += "; " + hint
		}
		return &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: ctxMsg,
			Err:     err,
		}
	}

	c.waitOnce = sync.Once{}
	c.waitDone = make(chan struct{})
	go c.waitForExit()

	return nil
}

// setupEnvironment configures environment variables for the process
func (c *Controller) setupEnvironment() {
	c.cmd.Env = c.buildEnvironment()
}

// FinalEnvironment exposes the fully materialized child environment
// (config layers + managed layer) for pre-spawn checks.
func (c *Controller) FinalEnvironment() []string {
	return c.buildEnvironment()
}

// buildEnvironment produces the child environment. With a resolved spec
// (Env non-nil), the config layers come from the launch resolver and the
// controller adds only the managed layer — GABS identity, GABP endpoint,
// platform requirements, and the delivery-contract variables
// GABS_FORWARD_ENV / GABS_ABSENT_ENV (design/03-context-delivery.md).
// Managed variables always win; output ordering is deterministic.
func (c *Controller) buildEnvironment() []string {
	if c.spec.Env == nil {
		return c.buildLegacyEnvironment()
	}

	env := make(map[string]string, len(c.spec.Env)+10)
	for k, v := range c.spec.Env {
		env[k] = v
	}

	managed := map[string]string{
		"GABS_GAME_ID":     c.spec.GameId,
		"GABS_BRIDGE_PATH": c.getBridgePath(),
	}
	if c.spec.Mode == "SteamManaged" {
		managed["SteamAppId"] = c.spec.PathOrId
		managed["SteamGameId"] = c.spec.PathOrId
	}
	if c.bridgeInfo != nil {
		managed["GABP_SERVER_PORT"] = strconv.Itoa(c.bridgeInfo.Port)
		managed["GABP_TOKEN"] = c.bridgeInfo.Token
	}
	if c.spec.Profile != "" {
		managed["GABS_PROFILE"] = c.spec.Profile
	}
	// Windows platform variables are managed: pinned from the parent
	// value (C:\Windows only as fallback) so config layers can neither
	// remove nor override them, and they participate in GABS_FORWARD_ENV.
	// Resolved launches must not leak Windows variables into unix children.
	if runtime.GOOS == "windows" {
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = "C:\\Windows"
		}
		windir := os.Getenv("WINDIR")
		if windir == "" {
			windir = systemRoot
		}
		managed["SystemRoot"] = systemRoot
		managed["WINDIR"] = windir
	}
	// Final absence is computed against the managed layer: a config unset
	// of a managed name (SteamAppId, SystemRoot) is re-added above, and
	// exporting it as both present and must-be-absent would hand wrappers
	// and delivery verification contradictory metadata (design/03).
	absent := make([]string, 0, len(c.spec.AbsentEnvNames))
	for _, name := range c.spec.AbsentEnvNames {
		reAdded := false
		for k := range managed {
			if k == name || (runtime.GOOS == "windows" && strings.EqualFold(k, name)) {
				reAdded = true
				break
			}
		}
		if !reAdded {
			absent = append(absent, name)
		}
	}
	if len(absent) > 0 {
		managed["GABS_ABSENT_ENV"] = strings.Join(absent, ",")
	}

	// GABS_FORWARD_ENV: every name a wrapper must carry across a filtering
	// boundary — the managed names plus the config-context key names.
	forward := make([]string, 0, len(managed)+len(c.spec.ContextEnvKeys)+1)
	for k := range managed {
		forward = append(forward, k)
	}
	forward = append(forward, "GABS_FORWARD_ENV")
	forward = append(forward, c.spec.ContextEnvKeys...)
	sort.Strings(forward)
	forward = dedupeSorted(forward)
	managed["GABS_FORWARD_ENV"] = strings.Join(forward, ",")

	for k, v := range managed {
		if runtime.GOOS == "windows" {
			// Windows env keys are case-insensitive: remove case-variants
			// so the managed spelling is the only survivor.
			for existing := range env {
				if existing != k && strings.EqualFold(existing, k) {
					delete(env, existing)
				}
			}
		}
		env[k] = v
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

// buildLegacyEnvironment preserves the pre-profile behavior bit-for-bit for
// launches without resolved context.
func (c *Controller) buildLegacyEnvironment() []string {
	bridgePath := c.getBridgePath()
	bridgeEnvVars := []string{
		fmt.Sprintf("GABS_GAME_ID=%s", c.spec.GameId),
		fmt.Sprintf("GABS_BRIDGE_PATH=%s", bridgePath),
	}
	if c.spec.Mode == "SteamManaged" {
		bridgeEnvVars = append(bridgeEnvVars,
			fmt.Sprintf("SteamAppId=%s", c.spec.PathOrId),
			fmt.Sprintf("SteamGameId=%s", c.spec.PathOrId),
		)
	}

	if c.bridgeInfo != nil {
		bridgeEnvVars = append(bridgeEnvVars,
			fmt.Sprintf("GABP_SERVER_PORT=%d", c.bridgeInfo.Port),
			fmt.Sprintf("GABP_TOKEN=%s", c.bridgeInfo.Token),
		)
	}

	env := os.Environ()
	if os.Getenv("SystemRoot") == "" {
		env = append(env, "SystemRoot=C:\\Windows", "WINDIR=C:\\Windows")
	}
	return append(env, bridgeEnvVars...)
}

func dedupeSorted(in []string) []string {
	out := in[:0]
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// IsRunning queries the actual system state to determine if the process is running
// This is stateless - it directly checks the real process state
func (c *Controller) IsRunning() bool {
	// For Steam/Epic launchers, check for the actual game process by name if configured
	if c.spec.Mode == "SteamAppId" || c.spec.Mode == "EpicAppId" {
		if c.spec.StopProcessName != "" {
			pids, err := findProcessesByNameFunc(c.spec.StopProcessName)
			if err != nil {
				return false
			}
			return len(pids) > 0
		}
		// Without StopProcessName, we can't track launcher-based games
		return false
	}

	// For direct processes, check the managed process
	if c.cmd == nil || c.cmd.Process == nil {
		return c.isRunningByName()
	}

	// waitDone is the race-free "already reaped" signal; reading
	// cmd.ProcessState here would race the waitForExit goroutine's Wait().
	select {
	case <-c.waitDone:
		return c.isRunningByName()
	default:
	}

	// Check if the child process is still alive using a lightweight OS call
	// (Windows: OpenProcess+GetExitCodeProcess, Unix: Signal(0))
	if isProcessAlive(c.cmd.Process.Pid) {
		return true
	}

	// Child process is dead — reap it exactly once and fall back to name lookup
	// (the launched exe may have been a launcher that spawned the real game and exited)
	go c.waitForExit()
	return c.isRunningByName()
}

// isRunningByName checks if the game process is running by its StopProcessName.
// This is used as a fallback when the direct child process has exited, which happens
// when the launched executable is a launcher/loader that spawns the actual game process
// and then exits (e.g., BLSE for Bannerlord, bridge loaders, etc.).
func (c *Controller) isRunningByName() bool {
	if c.spec.StopProcessName == "" {
		return false
	}
	pids, err := findProcessesByNameFunc(c.spec.StopProcessName)
	if err != nil {
		return false
	}
	return len(pids) > 0
}

// WaitForProcessStart waits for the process to be detectable in the system
func (c *Controller) WaitForProcessStart(timeout time.Duration) error {
	if c.usesLauncherProcessNameTracking() {
		return c.waitForProcessNameStart(timeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return &ProcessError{
				Type:    ProcessErrorTypeStart,
				Context: fmt.Sprintf("timed out waiting for %s to start", c.spec.GameId),
				Err:     fmt.Errorf("process not found in system after %v", timeout),
			}
		case <-ticker.C:
			select {
			case <-c.waitDone:
				return nil
			default:
			}
			if c.IsRunning() {
				return nil
			}
		}
	}
}

func (c *Controller) usesLauncherProcessNameTracking() bool {
	return (c.spec.Mode == "SteamAppId" || c.spec.Mode == "EpicAppId") && c.spec.StopProcessName != ""
}

func (c *Controller) waitForProcessNameStart(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return &ProcessError{
				Type:    ProcessErrorTypeStart,
				Context: fmt.Sprintf("timed out waiting for %s to start", c.spec.GameId),
				Err:     fmt.Errorf("process %q not found in system after %v", c.spec.StopProcessName, timeout),
			}
		case <-ticker.C:
			if c.isRunningByName() {
				return nil
			}
		}
	}
}

// Stop gracefully stops the process
func (c *Controller) Stop(grace time.Duration) error {
	// Try to stop by process name first if configured
	if c.spec.StopProcessName != "" {
		if err := c.stopByProcessName(c.spec.StopProcessName, false, grace); err == nil {
			return nil
		}
	}

	if c.cmd == nil || c.cmd.Process == nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStop,
			Context: "no process to stop",
			Err:     fmt.Errorf("no process available"),
		}
	}

	// Try graceful termination first
	if err := c.cmd.Process.Signal(getTerminationSignal()); err != nil {
		// If graceful termination fails, try force kill
		killErr := c.cmd.Process.Kill()
		if killErr != nil {
			return &ProcessError{
				Type:    ProcessErrorTypeStop,
				Context: fmt.Sprintf("failed to stop %s", c.spec.GameId),
				Err:     killErr,
			}
		}
		return nil
	}

	// Wait for graceful shutdown with timeout
	select {
	case <-c.waitDone:
		return nil
	case <-time.After(grace):
		// Grace period expired, force kill
		if err := c.cmd.Process.Kill(); err != nil {
			return &ProcessError{
				Type:    ProcessErrorTypeStop,
				Context: fmt.Sprintf("failed to force kill %s after grace period", c.spec.GameId),
				Err:     err,
			}
		}
		return nil
	}
}

// Kill forcefully terminates the process
func (c *Controller) Kill() error {
	if c.spec.StopProcessName != "" {
		if err := c.stopByProcessName(c.spec.StopProcessName, true, 0); err == nil {
			return nil
		}
	}

	if c.cmd == nil || c.cmd.Process == nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStop,
			Context: "no process to kill",
			Err:     fmt.Errorf("no process available"),
		}
	}

	err := c.cmd.Process.Kill()
	if err != nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStop,
			Context: fmt.Sprintf("failed to kill %s", c.spec.GameId),
			Err:     err,
		}
	}
	return nil
}

// Restart stops and then starts the process
func (c *Controller) Restart() error {
	// Stop then Start, preserving spec
	if err := c.Stop(3 * time.Second); err != nil {
		// Log the stop error but continue with restart
		// The failure might be because the process was already dead
		// In that case, starting should still work
		fmt.Fprintf(os.Stderr, "Warning: Stop failed during restart: %v\n", err)
	}
	return c.Start()
}

// GetPID returns the process ID if available
func (c *Controller) GetPID() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// GetLaunchMode returns the launch mode
func (c *Controller) GetLaunchMode() string {
	return c.spec.Mode
}

// GetStopProcessName returns the stop process name
func (c *Controller) GetStopProcessName() string {
	return c.spec.StopProcessName
}

// IsLauncherProcessRunning checks if the launcher process itself is still running
func (c *Controller) IsLauncherProcessRunning() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}

	if c.cmd.ProcessState != nil {
		return false
	}

	select {
	case <-c.waitDone:
		return false
	default:
	}

	err := c.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

func (c *Controller) waitForExit() {
	if c.cmd == nil {
		return
	}

	c.waitOnce.Do(func() {
		_ = c.cmd.Wait()
		if c.waitDone != nil {
			close(c.waitDone)
		}
	})
}

// Helper methods
func defaultSteamLaunchCommandFactory(target string) (string, []string) {
	cmdName := getSteamLauncherCommand()
	if runtime.GOOS == "windows" {
		return cmdName, []string{"/c", "start", fmt.Sprintf("steam://rungameid/%s", target)}
	}
	return cmdName, []string{fmt.Sprintf("steam://rungameid/%s", target)}
}

func defaultEpicLaunchCommandFactory(target string) (string, []string) {
	return getSystemOpenCommand(), []string{fmt.Sprintf("com.epicgames.launcher://apps/%s?action=launch&silent=true", target)}
}

func getSteamLauncherCommand() string {
	switch runtime.GOOS {
	case "windows":
		return "cmd"
	case "darwin":
		return "open"
	default:
		return "xdg-open"
	}
}

func getSystemOpenCommand() string {
	switch runtime.GOOS {
	case "windows":
		return "cmd"
	case "darwin":
		return "open"
	default:
		return "xdg-open"
	}
}

// SetLaunchCommandFactoriesForTesting overrides launcher resolution for tests.
// It returns a restore function that resets the original factories.
func SetLaunchCommandFactoriesForTesting(
	steamFactory func(target string) (string, []string),
	epicFactory func(target string) (string, []string),
) func() {
	prevSteam := steamLaunchCommandFactory
	prevEpic := epicLaunchCommandFactory

	if steamFactory != nil {
		steamLaunchCommandFactory = steamFactory
	}
	if epicFactory != nil {
		epicLaunchCommandFactory = epicFactory
	}

	return func() {
		steamLaunchCommandFactory = prevSteam
		epicLaunchCommandFactory = prevEpic
	}
}

// launchLogPath is the per-launch child output file beside bridge.json.
func (c *Controller) launchLogPath() string {
	return filepath.Join(c.runtimeDir(), "launch.log")
}

// runtimeDir returns the per-game runtime directory.
func (c *Controller) runtimeDir() string {
	if c.spec.RuntimeDir != "" {
		return c.spec.RuntimeDir
	}
	return filepath.Dir(c.getBridgePath())
}

// LaunchLogTail returns up to maxBytes from the end of the child output
// log — evidence for spawn/exit failures.
func (c *Controller) LaunchLogTail(maxBytes int64) string {
	f, err := os.Open(c.launchLogPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := int64(0)
	if fi.Size() > maxBytes {
		offset = fi.Size() - maxBytes
	}
	buf := make([]byte, fi.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return ""
	}
	return string(buf)
}

// startErrorHintFor maps platform-specific spawn errors to precise hints.
// ERROR_ELEVATION_REQUIRED (740): the target requires elevation and GABS
// deliberately does not elevate (design/03, platform rules).
func startErrorHintFor(err error, goos string) string {
	if goos != "windows" {
		return ""
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && int(errno) == 740 {
		return "the target requires elevation; GABS does not elevate — remove the elevation requirement or launch it outside GABS"
	}
	return ""
}

func (c *Controller) getBridgePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".gabs", c.spec.GameId, "bridge.json")
	}
	return filepath.Join(homeDir, ".gabs", c.spec.GameId, "bridge.json")
}

func (c *Controller) stopByProcessName(processName string, force bool, grace time.Duration) error {
	pids, err := findProcessesByNameFunc(processName)
	if err != nil {
		return fmt.Errorf("failed to find processes named '%s': %w", processName, err)
	}

	if len(pids) == 0 {
		return fmt.Errorf("no processes found with name '%s'", processName)
	}

	var lastErr error
	stopped := 0
	for _, pid := range pids {
		if force {
			if err := killProcess(pid); err != nil {
				lastErr = err
			} else {
				stopped++
			}
		} else {
			if err := terminateProcess(pid, grace); err != nil {
				lastErr = err
			} else {
				stopped++
			}
		}
	}

	if stopped == 0 {
		if lastErr != nil {
			return fmt.Errorf("failed to stop any processes named '%s': %w", processName, lastErr)
		}
		return fmt.Errorf("failed to stop any processes named '%s'", processName)
	}

	return nil
}

// ProcessError represents different types of process-related errors
type ProcessError struct {
	Type    ProcessErrorType
	Context string
	Err     error
}

type ProcessErrorType int

const (
	ProcessErrorTypeConfiguration ProcessErrorType = iota
	ProcessErrorTypeStart
	ProcessErrorTypeStop
	ProcessErrorTypeStatus
	ProcessErrorTypeNotFound
)

func (e *ProcessError) Error() string {
	switch e.Type {
	case ProcessErrorTypeConfiguration:
		return fmt.Sprintf("configuration error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeStart:
		return fmt.Sprintf("start error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeStop:
		return fmt.Sprintf("stop error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeStatus:
		return fmt.Sprintf("status check error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeNotFound:
		return fmt.Sprintf("process not found (%s): %v", e.Context, e.Err)
	default:
		return fmt.Sprintf("process error (%s): %v", e.Context, e.Err)
	}
}

// Helper functions for cross-platform process management
func getTerminationSignal() os.Signal {
	switch runtime.GOOS {
	case "windows":
		return os.Interrupt
	default:
		return syscall.SIGTERM
	}
}

// killProcess forcefully terminates a process by PID
func killProcess(pid int) error {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
		return cmd.Run()
	default:
		// Unix-like systems
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return process.Kill()
	}
}

// terminateProcess gracefully terminates a process by PID with a timeout
func terminateProcess(pid int, grace time.Duration) error {
	switch runtime.GOOS {
	case "windows":
		// On Windows, try gentle termination first, then force kill if timeout
		cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid))
		if err := cmd.Run(); err != nil {
			return err
		}

		// Wait for process to exit gracefully
		if grace > 0 {
			time.Sleep(grace)
			// Check if process still exists
			checkCmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV")
			output, err := checkCmd.Output()
			if err == nil && strings.Contains(string(output), strconv.Itoa(pid)) {
				// Process still exists, force kill it
				return killProcess(pid)
			}
		}
		return nil
	default:
		// Unix-like systems
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}

		// Send SIGTERM
		if err := process.Signal(syscall.SIGTERM); err != nil {
			return err
		}

		// Wait for graceful shutdown with timeout
		if grace > 0 {
			done := make(chan error, 1)
			go func() {
				_, err := process.Wait()
				done <- err
			}()

			select {
			case <-done:
				return nil
			case <-time.After(grace):
				// Grace period expired, force kill
				return process.Kill()
			}
		}

		return nil
	}
}

// findProcessesByName finds all processes with the given name
func findProcessesByName(name string) ([]int, error) {
	var pids []int

	switch runtime.GOOS {
	case "windows":
		// Use tasklist command on Windows
		cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name, "/FO", "CSV", "/NH")
		output, err := cmd.Output()
		if err != nil {
			return nil, err
		}

		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.Contains(line, name) {
				// Parse CSV: "ProcessName","PID","SessionName","Session#","MemUsage"
				parts := strings.Split(line, ",")
				if len(parts) >= 2 {
					pidStr := strings.Trim(parts[1], "\"")
					if pid, err := strconv.Atoi(pidStr); err == nil {
						pids = append(pids, pid)
					}
				}
			}
		}
	case "linux":
		return findLinuxProcessesByName(name)
	default:
		return findProcessesByNameWithPgrep(name)
	}

	return pids, nil
}

func findProcessesByNameWithPgrep(name string) ([]int, error) {
	var pids []int
	cmd := exec.Command("pgrep", "-x", name)
	output, err := cmd.Output()
	if err != nil {
		// pgrep returns exit code 1 if no processes found, which is not an error for us
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			return pids, nil
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line != "" {
			if pid, err := strconv.Atoi(line); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	return pids, nil
}

// linuxProcRoot is injectable so the scan's error handling is testable on
// any platform.
var linuxProcRoot = "/proc"

// findLinuxProcessesByName scans the process table. Inspection failures
// (e.g. EACCES under hidepid) must surface as errors so liveness reports
// unknown, never a false stopped (design/04); only normal process-
// disappearance races are ignored. Any positive match still wins — a found
// process is running-evidence regardless of unreadable neighbors.
func findLinuxProcessesByName(name string) ([]int, error) {
	var pids []int

	entries, err := os.ReadDir(linuxProcRoot)
	if err != nil {
		return nil, err
	}

	var inspectErr error
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		match, err := linuxProcessMatchesName(pid, name)
		if err != nil {
			inspectErr = err
			continue
		}
		if match {
			pids = append(pids, pid)
		}
	}

	if len(pids) == 0 && inspectErr != nil {
		return nil, fmt.Errorf("process table not fully inspectable: %w", inspectErr)
	}
	return pids, nil
}

func linuxProcessMatchesName(pid int, name string) (bool, error) {
	procDir := filepath.Join(linuxProcRoot, strconv.Itoa(pid))

	var inspectErr error
	if comm, err := os.ReadFile(filepath.Join(procDir, "comm")); err == nil {
		if strings.TrimSpace(string(comm)) == name {
			return true, nil
		}
	} else if !isProcessGoneError(err) {
		inspectErr = err
	}

	cmdline, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
	if err != nil {
		if isProcessGoneError(err) {
			// disappeared mid-scan: an ordinary race, not an inspection failure
			return false, nil
		}
		return false, err
	}
	if len(cmdline) == 0 {
		return false, inspectErr
	}

	argv0End := strings.IndexByte(string(cmdline), 0)
	if argv0End < 0 {
		argv0End = len(cmdline)
	}
	argv0 := string(cmdline[:argv0End])

	if argv0 == name || filepath.Base(argv0) == name {
		return true, nil
	}
	return false, inspectErr
}

func isProcessGoneError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}
