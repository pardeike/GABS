package process

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

type LaunchSpec struct {
	GameId     string
	Mode       string // DirectPath|SteamAppId|EpicAppId|CustomCommand
	PathOrId   string
	Args       []string
	WorkingDir string
}

type Controller struct {
	spec LaunchSpec
	cmd  *exec.Cmd
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

	// Start the process
	return c.cmd.Start()
}

func (c *Controller) Stop(grace time.Duration) error {
	if c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("no process to stop")
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
	if c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("no process to kill")
	}
	return c.cmd.Process.Kill()
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
