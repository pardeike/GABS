package process

import (
	"context" 
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ImprovedController is a better version of Controller with proper state management
type ImprovedController struct {
	spec       LaunchSpec
	cmd        *exec.Cmd
	bridgeInfo *BridgeInfo
	state      *ProcessStateTracker
	
	// For launcher processes, we need to handle them differently
	isLauncher bool
	
	// Context for managing lifecycle operations
	ctx    context.Context
	cancel context.CancelFunc
}

// NewImprovedController creates a new improved controller
func NewImprovedController() *ImprovedController {
	return &ImprovedController{}
}

// Configure sets up the controller with the given launch specification
func (c *ImprovedController) Configure(spec LaunchSpec) error {
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
		c.isLauncher = false
	case "SteamAppId", "EpicAppId", "CustomCommand":
		if spec.PathOrId == "" {
			return &ProcessError{
				Type:    ProcessErrorTypeConfiguration,
				Context: fmt.Sprintf("PathOrId is required for mode %s", spec.Mode),
				Err:     fmt.Errorf("PathOrId cannot be empty for %s mode", spec.Mode),
			}
		}
		c.isLauncher = (spec.Mode == "SteamAppId" || spec.Mode == "EpicAppId")
	default:
		return &ProcessError{
			Type:    ProcessErrorTypeConfiguration,
			Context: fmt.Sprintf("unsupported launch mode: %s", spec.Mode),
			Err:     fmt.Errorf("unsupported launch mode: %s", spec.Mode),
		}
	}

	c.spec = spec
	c.state = NewProcessStateTracker(spec.GameId, spec.Mode)
	c.ctx, c.cancel = context.WithCancel(context.Background())
	
	return nil
}

// SetBridgeInfo sets the bridge connection information
func (c *ImprovedController) SetBridgeInfo(port int, token string) {
	c.bridgeInfo = &BridgeInfo{
		Port:  port,
		Token: token,
	}
}

// Start begins the process with proper state management and error handling
func (c *ImprovedController) Start() error {
	if c.state == nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: "controller not configured",
			Err:     fmt.Errorf("Configure() must be called before Start()"),
		}
	}

	// Check if already running
	if c.state.IsRunning() {
		return &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: fmt.Sprintf("game %s already running", c.spec.GameId),
			Err:     fmt.Errorf("process is already running"),
		}
	}

	// Set state to starting
	c.state.SetState(ProcessStateStarting)

	// Prepare command based on launch mode
	if err := c.setupCommand(); err != nil {
		c.state.SetError(err, ProcessErrorTypeStart, "command setup failed")
		return err
	}

	// Start the process
	if err := c.cmd.Start(); err != nil {
		processErr := &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: fmt.Sprintf("failed to start %s (mode: %s, target: %s)", c.spec.GameId, c.spec.Mode, c.spec.PathOrId),
			Err:     err,
		}
		c.state.SetError(processErr, ProcessErrorTypeStart, "process start failed")
		return processErr
	}

	// Record PID and set state to running
	c.state.SetPID(c.cmd.Process.Pid)
	
	// Handle different launch modes differently
	if c.isLauncher {
		// For launcher processes, we set state based on tracking capability
		if c.spec.StopProcessName != "" {
			// We can track the actual game, so we start monitoring immediately
			go c.monitorLauncherProcess()
			// Also start monitoring the game by name immediately
			go c.monitorGameByName()
		} else {
			// We can't track the game, so we set to unknown after launcher exits
			c.state.SetState(ProcessStateUnknown)
			go c.waitForLauncherExit()
		}
	} else {
		// For direct processes, monitor the actual process
		c.state.SetState(ProcessStateRunning)
		go c.monitorDirectProcess()
	}

	return nil
}

// setupCommand prepares the exec.Cmd based on launch mode
func (c *ImprovedController) setupCommand() error {
	var cmdName string
	var cmdArgs []string

	switch c.spec.Mode {
	case "DirectPath", "":
		cmdName = c.spec.PathOrId
		cmdArgs = c.spec.Args
	case "SteamAppId":
		cmdName = c.getSteamLauncher()
		cmdArgs = []string{fmt.Sprintf("steam://rungameid/%s", c.spec.PathOrId)}
	case "EpicAppId":
		cmdName = c.getSystemOpenCommand()
		cmdArgs = []string{fmt.Sprintf("com.epicgames.launcher://apps/%s?action=launch&silent=true", c.spec.PathOrId)}
	case "CustomCommand":
		cmdName = c.spec.PathOrId
		cmdArgs = c.spec.Args
	default:
		return fmt.Errorf("unsupported launch mode: %s", c.spec.Mode)
	}

	// Create command
	c.cmd = exec.CommandContext(c.ctx, cmdName, cmdArgs...)
	if c.spec.WorkingDir != "" {
		c.cmd.Dir = c.spec.WorkingDir
	}

	// Set environment variables
	c.setEnvironmentVariables()

	return nil
}

// setEnvironmentVariables configures environment for the process
func (c *ImprovedController) setEnvironmentVariables() {
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

	// Handle Windows environment
	env := os.Environ()
	if os.Getenv("SystemRoot") == "" {
		env = append(env, "SystemRoot=C:\\Windows", "WINDIR=C:\\Windows")
	}
	c.cmd.Env = append(env, bridgeEnvVars...)
}

// monitorDirectProcess monitors a direct process and updates state
func (c *ImprovedController) monitorDirectProcess() {
	defer func() {
		if r := recover(); r != nil {
			c.state.SetError(fmt.Errorf("monitor panic: %v", r), ProcessErrorTypeStatus, "process monitor")
		}
	}()

	// Monitor the process
	err := c.cmd.Wait()
	
	// Process has exited
	if err != nil {
		// Process exited with error
		c.state.SetError(err, ProcessErrorTypeStatus, "process exited with error")
	}
	
	c.state.SetState(ProcessStateStopped)
}

// monitorLauncherProcess monitors launcher processes and the actual game
func (c *ImprovedController) monitorLauncherProcess() {
	defer func() {
		if r := recover(); r != nil {
			c.state.SetError(fmt.Errorf("launcher monitor panic: %v", r), ProcessErrorTypeStatus, "launcher monitor")
		}
	}()

	// Wait for launcher to exit (this should be quick)
	c.cmd.Wait()
	
	// Now monitor the actual game process by name
	if c.spec.StopProcessName != "" {
		c.monitorGameByName()
	}
}

// waitForLauncherExit waits for launcher to exit and sets appropriate state
func (c *ImprovedController) waitForLauncherExit() {
	defer func() {
		if r := recover(); r != nil {
			c.state.SetError(fmt.Errorf("launcher wait panic: %v", r), ProcessErrorTypeStatus, "launcher wait")
		}
	}()

	// Wait for launcher to exit
	c.cmd.Wait()
	
	// Since we can't track the game, set to unknown
	c.state.SetState(ProcessStateUnknown)
}

// monitorGameByName monitors the actual game process by process name
func (c *ImprovedController) monitorGameByName() {
	// Wait a bit for the launcher to potentially start the game
	time.Sleep(500 * time.Millisecond)
	
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			pids, err := findProcessesByName(c.spec.StopProcessName)
			if err != nil {
				c.state.SetError(err, ProcessErrorTypeStatus, "failed to check game process")
				continue
			}
			
			if len(pids) > 0 {
				// Game process found - set to running if not already
				if c.state.GetState() != ProcessStateRunning {
					c.state.SetState(ProcessStateRunning)
				}
			} else {
				// No game process found
				currentState := c.state.GetState()
				if currentState == ProcessStateRunning {
					// Game was running but now stopped
					c.state.SetState(ProcessStateStopped)
					return
				}
				// If we haven't found it yet, keep looking for a bit
				if currentState == ProcessStateStarting {
					// Still looking for the game to start
					continue
				}
			}
		}
	}
}

// Stop gracefully stops the process
func (c *ImprovedController) Stop(grace time.Duration) error {
	if c.state == nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStop,
			Context: "controller not configured",
			Err:     fmt.Errorf("no controller configured"),
		}
	}

	if !c.state.IsRunning() {
		return &ProcessError{
			Type:    ProcessErrorTypeStop,
			Context: "process not running",
			Err:     fmt.Errorf("process is not running"),
		}
	}

	c.state.SetState(ProcessStateStopping)

	// Try to stop by process name first if configured
	if c.spec.StopProcessName != "" {
		if err := c.stopByProcessName(c.spec.StopProcessName, false, grace); err == nil {
			c.state.SetState(ProcessStateStopped)
			return nil
		}
	}

	// Fall back to stopping the managed process
	if c.cmd == nil || c.cmd.Process == nil {
		c.state.SetState(ProcessStateStopped)
		return nil
	}

	// Try graceful termination first
	if err := c.cmd.Process.Signal(getTerminationSignal()); err != nil {
		// If graceful termination fails, try force kill
		killErr := c.cmd.Process.Kill()
		if killErr != nil {
			processErr := &ProcessError{
				Type:    ProcessErrorTypeStop,
				Context: fmt.Sprintf("failed to stop %s", c.spec.GameId),
				Err:     killErr,
			}
			c.state.SetError(processErr, ProcessErrorTypeStop, "force kill failed")
			return processErr
		}
		c.state.SetState(ProcessStateStopped)
		return nil
	}

	// Wait for graceful shutdown with timeout
	done := make(chan error, 1)
	go func() {
		done <- c.cmd.Wait()
	}()

	select {
	case <-done:
		c.state.SetState(ProcessStateStopped)
		return nil
	case <-time.After(grace):
		// Grace period expired, force kill
		if err := c.cmd.Process.Kill(); err != nil {
			processErr := &ProcessError{
				Type:    ProcessErrorTypeStop,
				Context: fmt.Sprintf("failed to force kill %s after grace period", c.spec.GameId),
				Err:     err,
			}
			c.state.SetError(processErr, ProcessErrorTypeStop, "force kill after timeout failed")
			return processErr
		}
		c.state.SetState(ProcessStateStopped)
		return nil
	}
}

// Kill forcefully terminates the process
func (c *ImprovedController) Kill() error {
	if c.state == nil {
		return &ProcessError{
			Type:    ProcessErrorTypeStop,
			Context: "controller not configured",
			Err:     fmt.Errorf("no controller configured"),
		}
	}

	c.state.SetState(ProcessStateStopping)

	// Try to kill by process name first if configured
	if c.spec.StopProcessName != "" {
		if err := c.stopByProcessName(c.spec.StopProcessName, true, 0); err == nil {
			c.state.SetState(ProcessStateStopped)
			return nil
		}
	}

	if c.cmd == nil || c.cmd.Process == nil {
		c.state.SetState(ProcessStateStopped)
		return nil
	}

	err := c.cmd.Process.Kill()
	if err != nil {
		processErr := &ProcessError{
			Type:    ProcessErrorTypeStop,
			Context: fmt.Sprintf("failed to kill %s", c.spec.GameId),
			Err:     err,
		}
		c.state.SetError(processErr, ProcessErrorTypeStop, "kill failed")
		return processErr
	}

	c.state.SetState(ProcessStateStopped)
	return nil
}

//IsRunning returns true if the process is currently running
func (c *ImprovedController) IsRunning() bool {
	if c.state == nil {
		return false
	}
	
	state := c.state.GetState()
	
	// For launcher processes with tracking, rely on the monitoring goroutine
	// Don't do immediate process lookups here to avoid blocking/deadlocks
	if c.isLauncher && c.spec.StopProcessName != "" {
		return state == ProcessStateRunning || state == ProcessStateStarting
	}
	
	// For launcher processes without tracking
	if c.isLauncher {
		return state == ProcessStateUnknown
	}
	
	// For direct processes, we rely on the monitoring goroutine
	return state == ProcessStateRunning || state == ProcessStateStarting
}

// GetState returns the current process state
func (c *ImprovedController) GetState() ProcessState {
	if c.state == nil {
		return ProcessStateStopped
	}
	return c.state.GetState()
}

// GetPID returns the process ID
func (c *ImprovedController) GetPID() int {
	if c.state == nil {
		return 0
	}
	return c.state.GetPID()
}

// GetLaunchMode returns the launch mode
func (c *ImprovedController) GetLaunchMode() string {
	return c.spec.Mode
}

// GetStopProcessName returns the stop process name
func (c *ImprovedController) GetStopProcessName() string {
	return c.spec.StopProcessName
}

// IsLauncherProcessRunning checks if the launcher process is still running
func (c *ImprovedController) IsLauncherProcessRunning() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}

	// Check if the process has already been waited for
	if c.cmd.ProcessState != nil {
		return false
	}

	// Try to signal the launcher process
	err := c.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// Cleanup releases resources
func (c *ImprovedController) Cleanup() {
	if c.cancel != nil {
		c.cancel()
	}
}

// Helper methods (reuse existing implementations)
func (c *ImprovedController) getSteamLauncher() string {
	return (&Controller{}).getSteamLauncher()
}

func (c *ImprovedController) getSystemOpenCommand() string {
	return (&Controller{}).getSystemOpenCommand()
}

func (c *ImprovedController) getBridgePath() string {
	return (&Controller{spec: c.spec}).getBridgePath()
}

func (c *ImprovedController) stopByProcessName(processName string, force bool, grace time.Duration) error {
	return (&Controller{spec: c.spec}).stopByProcessName(processName, force, grace)
}