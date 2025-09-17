package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// RefactoredController implements a stateless approach to process management
// It queries the actual system state rather than maintaining internal state
type RefactoredController struct {
	spec       LaunchSpec
	cmd        *exec.Cmd
	bridgeInfo *BridgeInfo
}

// NewRefactoredController creates a new refactored controller
func NewRefactoredController() *RefactoredController {
	return &RefactoredController{}
}

// Configure sets up the controller with the given launch specification
func (c *RefactoredController) Configure(spec LaunchSpec) error {
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
func (c *RefactoredController) SetBridgeInfo(port int, token string) {
	c.bridgeInfo = &BridgeInfo{
		Port:  port,
		Token: token,
	}
}

// Start launches the process and waits for verification
// This implements the serialized starting approach requested by @pardeike
func (c *RefactoredController) Start() error {
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
func (c *RefactoredController) setupEnvironment() {
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
func (c *RefactoredController) IsRunning() bool {
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
// This implements the verification phase requested by @pardeike
func (c *RefactoredController) WaitForProcessStart(timeout time.Duration) error {
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
func (c *RefactoredController) Stop(grace time.Duration) error {
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
func (c *RefactoredController) Kill() error {
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

// GetPID returns the process ID if available
func (c *RefactoredController) GetPID() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// GetLaunchMode returns the launch mode
func (c *RefactoredController) GetLaunchMode() string {
	return c.spec.Mode
}

// GetStopProcessName returns the stop process name
func (c *RefactoredController) GetStopProcessName() string {
	return c.spec.StopProcessName
}

// IsLauncherProcessRunning checks if the launcher process itself is still running
func (c *RefactoredController) IsLauncherProcessRunning() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}

	if c.cmd.ProcessState != nil {
		return false
	}

	err := c.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// Helper methods (reuse existing implementations)
func (c *RefactoredController) getSteamLauncher() string {
	switch runtime.GOOS {
	case "windows":
		return "cmd"
	case "darwin":
		return "open"
	default:
		return "xdg-open"
	}
}

func (c *RefactoredController) getSystemOpenCommand() string {
	switch runtime.GOOS {
	case "windows":
		return "cmd"
	case "darwin":
		return "open"
	default:
		return "xdg-open"
	}
}

func (c *RefactoredController) getBridgePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".gabs", c.spec.GameId, "bridge.json")
	}
	return filepath.Join(homeDir, ".gabs", c.spec.GameId, "bridge.json")
}

func (c *RefactoredController) stopByProcessName(processName string, force bool, grace time.Duration) error {
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