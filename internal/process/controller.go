package process

import (
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
	Host  string
	Port  int
	Token string
	Mode  string
}

type Controller struct {
	spec       LaunchSpec
	cmd        *exec.Cmd
	bridgeInfo *BridgeInfo
}

func (c *Controller) Configure(spec LaunchSpec) error {
	// Validate spec and prepare platform-specific launch
	if spec.GameId == "" {
		return fmt.Errorf("GameId is required")
	}
	
	switch spec.Mode {
	case "DirectPath", "":
		if spec.PathOrId == "" {
			return fmt.Errorf("PathOrId is required for DirectPath mode")
		}
	case "SteamAppId", "EpicAppId", "CustomCommand":
		if spec.PathOrId == "" {
			return fmt.Errorf("PathOrId is required for %s mode", spec.Mode)
		}
	default:
		return fmt.Errorf("unsupported launch mode: %s", spec.Mode)
	}
	
	c.spec = spec
	return nil
}

// SetBridgeInfo sets the bridge connection information that will be passed to the game via environment variables
func (c *Controller) SetBridgeInfo(host string, port int, token, mode string) {
	c.bridgeInfo = &BridgeInfo{
		Host:  host,
		Port:  port,
		Token: token,
		Mode:  mode,
	}
}

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
		cmdArgs = []string{fmt.Sprintf("steam://rungameid/%s", c.spec.PathOrId)}
	case "EpicAppId":
		// Epic Games Store URL format
		cmdName = c.getSystemOpenCommand()
		cmdArgs = []string{fmt.Sprintf("com.epicgames.launcher://apps/%s?action=launch&silent=true", c.spec.PathOrId)}
	case "CustomCommand":
		cmdName = c.spec.PathOrId
		cmdArgs = c.spec.Args
	default:
		return fmt.Errorf("unsupported launch mode: %s", c.spec.Mode)
	}

	// Create command
	c.cmd = exec.Command(cmdName, cmdArgs...)
	if c.spec.WorkingDir != "" {
		c.cmd.Dir = c.spec.WorkingDir
	}
	
	// Set environment variables to help mods find the bridge file
	// Hybrid approach: pass both file path and essential connection info directly
	bridgePath := c.getBridgePath()
	bridgeEnvVars := []string{
		fmt.Sprintf("GABS_GAME_ID=%s", c.spec.GameId),
		fmt.Sprintf("GABS_BRIDGE_PATH=%s", bridgePath),
	}
	
	// If bridge connection info is available, also pass it directly in environment variables
	// This provides immediate access without file I/O and handles cases where file access might fail
	if c.bridgeInfo != nil {
		bridgeEnvVars = append(bridgeEnvVars,
			fmt.Sprintf("GABS_HOST=%s", c.bridgeInfo.Host),
			fmt.Sprintf("GABS_PORT=%d", c.bridgeInfo.Port),
			fmt.Sprintf("GABS_TOKEN=%s", c.bridgeInfo.Token),
			fmt.Sprintf("GABS_MODE=%s", c.bridgeInfo.Mode),
		)
	}
	
	c.cmd.Env = append(os.Environ(), bridgeEnvVars...)

	// For Steam/Epic launchers, we need different handling since the launcher
	// process exits quickly but the game continues running independently
	if c.spec.Mode == "SteamAppId" || c.spec.Mode == "EpicAppId" {
		// Start the launcher and let it exit
		if err := c.cmd.Start(); err != nil {
			return fmt.Errorf("failed to start %s launcher: %w", c.spec.Mode, err)
		}
		
		// Don't wait for launcher to finish - it's just a trigger
		// The actual game process runs independently
		// Note: This means IsRunning() will return false for Steam/Epic games
		// which is technically correct since we're not managing the game process directly
		return nil
	}

	// For direct processes, start normally and track the actual process
	return c.cmd.Start()
}

func (c *Controller) Stop(grace time.Duration) error {
	if c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("no process to stop")
	}

	// If a specific stop process name is configured, try to find and stop that process
	if c.spec.StopProcessName != "" {
		if err := c.stopByProcessName(c.spec.StopProcessName, false, grace); err == nil {
			// Successfully stopped by process name
			return nil
		}
		// If stopping by process name failed, continue with the normal process stopping
	}

	// Try graceful termination first
	if err := c.cmd.Process.Signal(getTerminationSignal()); err != nil {
		// If graceful termination fails, try force kill
		return c.cmd.Process.Kill()
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
		return c.cmd.Process.Kill()
	}
}

func (c *Controller) Kill() error {
	// If a specific stop process name is configured, try to kill that process first
	if c.spec.StopProcessName != "" {
		if err := c.stopByProcessName(c.spec.StopProcessName, true, 0); err == nil {
			// Successfully killed by process name
			return nil
		}
		// If killing by process name failed, continue with the normal process killing
	}

	if c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("no process to kill")
	}
	return c.cmd.Process.Kill()
}

// IsRunning checks if the controlled process is still running
func (c *Controller) IsRunning() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	
	// Special case: Steam/Epic launchers exit quickly but game continues
	// For these cases, we assume the game is running if we have a recent launch
	// This is imperfect but matches the reality that we don't track the actual game process
	if c.spec.Mode == "SteamAppId" || c.spec.Mode == "EpicAppId" {
		// For launcher-based games, we can't easily track the actual game process
		// Return true for a reasonable period after launch, then false
		// This is a limitation of the launcher approach - we don't manage the game directly
		// TODO: Could improve by trying to find game process by name/other heuristics
		return false // For now, be conservative and assume launcher games manage themselves
	}
	
	// For direct processes, check if the process is still alive
	// First try to see if the process has already been waited for
	if c.cmd.ProcessState != nil {
		// Process has exited
		return false
	}
	
	// Try to signal the process with signal 0 (doesn't affect the process, just checks existence)
	// This is the most reliable cross-platform approach
	err := c.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// GetPID returns the process ID if available
func (c *Controller) GetPID() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// GetLaunchMode returns the launch mode for this controller
func (c *Controller) GetLaunchMode() string {
	return c.spec.Mode
}

func (c *Controller) Restart() error {
	// Stop then Start, preserving spec
	if err := c.Stop(3 * time.Second); err != nil {
		// Continue with restart even if stop fails
	}
	return c.Start()
}

// Platform-specific helpers

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

func getTerminationSignal() os.Signal {
	switch runtime.GOOS {
	case "windows":
		return os.Interrupt
	default:
		return syscall.SIGTERM
	}
}

// stopByProcessName tries to find and stop a process by its name
func (c *Controller) stopByProcessName(processName string, force bool, grace time.Duration) error {
	pids, err := findProcessesByName(processName)
	if err != nil {
		return fmt.Errorf("failed to find processes named '%s': %w", processName, err)
	}

	if len(pids) == 0 {
		return fmt.Errorf("no processes found with name '%s'", processName)
	}

	// Try to stop all found processes
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

// findProcessesByName finds all processes with the given name and returns their PIDs
func findProcessesByName(processName string) ([]int, error) {
	switch runtime.GOOS {
	case "windows":
		return findProcessesWindows(processName)
	case "darwin":
		return findProcessesDarwin(processName)
	default:
		return findProcessesLinux(processName)
	}
}

// findProcessesWindows finds processes on Windows using tasklist
func findProcessesWindows(processName string) ([]int, error) {
	// Add .exe extension if not present
	if !strings.HasSuffix(strings.ToLower(processName), ".exe") {
		processName += ".exe"
	}

	cmd := exec.Command("tasklist", "/FO", "CSV", "/NH")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run tasklist: %w", err)
	}

	var pids []int
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse CSV format: "ImageName","PID","SessionName","Session#","MemUsage"
		parts := strings.Split(line, ",")
		if len(parts) >= 2 {
			imageName := strings.Trim(parts[0], "\"")
			pidStr := strings.Trim(parts[1], "\"")

			if strings.EqualFold(imageName, processName) {
				if pid, err := strconv.Atoi(pidStr); err == nil {
					pids = append(pids, pid)
				}
			}
		}
	}

	return pids, nil
}

// findProcessesDarwin finds processes on macOS using ps
func findProcessesDarwin(processName string) ([]int, error) {
	cmd := exec.Command("ps", "axo", "pid,comm")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run ps: %w", err)
	}

	var pids []int
	lines := strings.Split(string(output), "\n")
	for _, line := range lines[1:] { // Skip header
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) >= 2 {
			pidStr := parts[0]
			comm := strings.Join(parts[1:], " ")

			// Match process name (with or without path)
			if strings.Contains(comm, processName) || strings.HasSuffix(comm, processName) {
				if pid, err := strconv.Atoi(pidStr); err == nil {
					pids = append(pids, pid)
				}
			}
		}
	}

	return pids, nil
}

// findProcessesLinux finds processes on Linux using ps
func findProcessesLinux(processName string) ([]int, error) {
	cmd := exec.Command("ps", "axo", "pid,comm")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run ps: %w", err)
	}

	var pids []int
	lines := strings.Split(string(output), "\n")
	for _, line := range lines[1:] { // Skip header
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) >= 2 {
			pidStr := parts[0]
			comm := strings.Join(parts[1:], " ")

			// Match process name
			if strings.Contains(comm, processName) || strings.HasSuffix(comm, processName) {
				if pid, err := strconv.Atoi(pidStr); err == nil {
					pids = append(pids, pid)
				}
			}
		}
	}

	return pids, nil
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

// getBridgePath returns the path to the bridge.json file for this game
func (c *Controller) getBridgePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to relative path if we can't get home directory
		return filepath.Join(".gabs", c.spec.GameId, "bridge.json")
	}
	return filepath.Join(homeDir, ".gabs", c.spec.GameId, "bridge.json")
}
