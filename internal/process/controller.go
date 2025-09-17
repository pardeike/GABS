package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type LaunchSpec struct {
	GameId          string
	Mode            string // DirectPath|SteamAppId|EpicAppId|CustomCommand
	PathOrId        string
	Args            []string
	WorkingDir      string
	StopProcessName string // Optional process name for stopping the game
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
	case "SteamAppId", "EpicAppId", "CustomCommand":
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
		cmdName = c.getSteamLauncher()
		if runtime.GOOS == "windows" {
			cmdArgs = []string{"/c", "start", fmt.Sprintf("steam://rungameid/%s", c.spec.PathOrId)}
		} else {
			cmdArgs = []string{fmt.Sprintf("steam://rungameid/%s", c.spec.PathOrId)}
		}
	case "EpicAppId":
		cmdName = c.getSystemOpenCommand()
		cmdArgs = []string{fmt.Sprintf("com.epicgames.launcher://apps/%s?action=launch&silent=true", c.spec.PathOrId)}
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

	// Start the process
	if err := c.cmd.Start(); err != nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: fmt.Sprintf("failed to start %s (mode: %s, target: %s)", c.spec.GameId, c.spec.Mode, c.spec.PathOrId),
			Err:     err,
		}
	}

	return nil
}

// setupEnvironment configures environment variables for the process
func (c *Controller) setupEnvironment() {
	bridgePath := c.getBridgePath()
	bridgeEnvVars := []string{
		fmt.Sprintf("GABS_GAME_ID=%s", c.spec.GameId),
		fmt.Sprintf("GABS_BRIDGE_PATH=%s", bridgePath),
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
	c.cmd.Env = append(env, bridgeEnvVars...)
}

// IsRunning queries the actual system state to determine if the process is running
// This is stateless - it directly checks the real process state
func (c *Controller) IsRunning() bool {
	// For Steam/Epic launchers, check for the actual game process by name if configured
	if c.spec.Mode == "SteamAppId" || c.spec.Mode == "EpicAppId" {
		if c.spec.StopProcessName != "" {
			pids, err := findProcessesByName(c.spec.StopProcessName)
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
		return false
	}

	// Check if the process has already been waited for
	if c.cmd.ProcessState != nil {
		return false
	}

	// Try to signal the process with signal 0 (doesn't affect the process, just checks existence)
	// This is the most reliable cross-platform approach
	err := c.cmd.Process.Signal(syscall.Signal(0))
	if err != nil {
		// Process is dead, try to reap it to update ProcessState
		go func() {
			c.cmd.Wait() // This will set ProcessState for future calls
		}()
		return false
	}
	return true
}

// WaitForProcessStart waits for the process to be detectable in the system
func (c *Controller) WaitForProcessStart(timeout time.Duration) error {
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
			if c.IsRunning() {
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
	done := make(chan error, 1)
	go func() {
		done <- c.cmd.Wait()
	}()

	select {
	case <-done:
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

	err := c.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// Helper methods
func (c *Controller) getSteamLauncher() string {
	switch runtime.GOOS {
	case "windows":
		return "cmd"
	case "darwin":
		return "open"
	default:
		return "xdg-open"
	}
}

func (c *Controller) getSystemOpenCommand() string {
	switch runtime.GOOS {
	case "windows":
		return "cmd"
	case "darwin":
		return "open"
	default:
		return "xdg-open"
	}
}

func (c *Controller) getBridgePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".gabs", c.spec.GameId, "bridge.json")
	}
	return filepath.Join(homeDir, ".gabs", c.spec.GameId, "bridge.json")
}

func (c *Controller) stopByProcessName(processName string, force bool, grace time.Duration) error {
	pids, err := findProcessesByName(processName)
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
	default:
		// Use pgrep command on Unix-like systems
		cmd := exec.Command("pgrep", "-x", name)
		output, err := cmd.Output()
		if err != nil {
			// pgrep returns exit code 1 if no processes found, which is not an error for us
			if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
				return pids, nil // Return empty slice, no error
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
	}

	return pids, nil
}